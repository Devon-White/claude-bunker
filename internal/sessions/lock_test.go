package sessions

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Two independent store instances over the SAME file, writing different keys
// concurrently, must not lose each other's writes.
func TestConcurrentStoresDoNotClobber(t *testing.T) {
	// TestMain points CLAUDE_BUNKER_STORE_DIR at a temp dir; both stores share it.
	a := newJSONMapStore("concurrent.json")
	b := newJSONMapStore("concurrent.json")

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func(n int) { defer wg.Done(); _ = a.Set("a"+itoa(n), "1") }(i)
		go func(n int) { defer wg.Done(); _ = b.Set("b"+itoa(n), "1") }(i)
	}
	wg.Wait()

	// A fresh reader must see all 100 keys.
	c := newJSONMapStore("concurrent.json")
	all := c.All()
	if len(all) != 100 {
		t.Fatalf("lost writes: want 100 keys, got %d", len(all))
	}
}

// TestFileLockGivesUpAfterDeadline exercises the give-up branch of
// withFileLock deterministically via the timeNow seam: a fresh (non-stale)
// lock file makes every acquire attempt fail, and the stubbed clock jumps
// straight past the 15s give-up deadline while staying within the 10s
// stale-reclaim window, so the stale-reclaim branch never fires and fn runs
// unlocked as a last resort.
func TestFileLockGivesUpAfterDeadline(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "givingup.json")
	lockPath := path + ".lock"

	// Seed a lock file "by hand" so acquire (O_CREATE|O_EXCL) always fails.
	// Its mtime is real wall-clock time, so it must look fresh relative to
	// whatever the stubbed clock reports.
	if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
		t.Fatalf("seeding lock file: %v", err)
	}
	defer os.Remove(lockPath)

	// base is set far enough in the past that base+30s still lands only a
	// few seconds after the lock file's real mtime (comfortably under the
	// 10s staleAfter threshold), while still being past the 15s deadline
	// computed from base.
	base := time.Now().Add(-25 * time.Second)
	callCount := 0
	origTimeNow := timeNow
	timeNow = func() time.Time {
		callCount++
		if callCount == 1 {
			return base
		}
		return base.Add(30 * time.Second)
	}
	defer func() { timeNow = origTimeNow }()

	var ran bool
	if err := withFileLock(path, func() error {
		ran = true
		return nil
	}); err != nil {
		t.Fatalf("withFileLock returned error: %v", err)
	}
	if !ran {
		t.Fatal("expected fn to run via the give-up path despite the lock file existing")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
