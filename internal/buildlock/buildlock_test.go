package buildlock

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// withCacheDir points config.CacheDir() at a temp dir and restores tunable
// seams after the test.
func withCacheDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CLAUDE_BUNKER_CACHE_DIR", dir)
	// Save/restore package-var seams so tests don't bleed into each other.
	origTimeout, origRetry, origPid, origStale := acquireTimeout, retryEvery, pidAlive, staleAfter
	t.Cleanup(func() {
		acquireTimeout, retryEvery, pidAlive, staleAfter = origTimeout, origRetry, origPid, origStale
	})
	return dir
}

func TestLockPath(t *testing.T) {
	dir := withCacheDir(t)
	got, err := LockPath("proj-abc")
	if err != nil {
		t.Fatalf("LockPath error: %v", err)
	}
	want := filepath.Join(dir, "proj-abc.build.lock")
	if got != want {
		t.Errorf("LockPath = %q, want %q", got, want)
	}
}

func TestAcquireReleaseRoundTrip(t *testing.T) {
	dir := withCacheDir(t)
	l, err := Acquire("proj")
	if err != nil {
		t.Fatalf("Acquire error: %v", err)
	}
	path := filepath.Join(dir, "proj.build.lock")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("lock file should exist after Acquire: %v", err)
	}
	l.Release()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("lock file should be gone after Release; stat err = %v", err)
	}
	l.Release() // idempotent — must not panic
}

func TestAcquireFailsClosedOnTimeout(t *testing.T) {
	withCacheDir(t)
	acquireTimeout = 80 * time.Millisecond
	retryEvery = 10 * time.Millisecond
	pidAlive = func(int) bool { return true } // holder always "alive" → never reclaim

	held, err := Acquire("proj")
	if err != nil {
		t.Fatalf("first Acquire error: %v", err)
	}
	defer held.Release()

	start := time.Now()
	if _, err := Acquire("proj"); err == nil {
		t.Fatal("second Acquire must FAIL CLOSED while the lock is held")
	}
	if elapsed := time.Since(start); elapsed < 60*time.Millisecond {
		t.Errorf("Acquire returned in %s, expected it to wait ~acquireTimeout before failing", elapsed)
	}
}

func TestAcquireReclaimsDeadHolder(t *testing.T) {
	dir := withCacheDir(t)
	pidAlive = func(int) bool { return false } // recorded holder is "dead"

	// Seed a lock file as if a crashed process holds it.
	path := filepath.Join(dir, "proj.build.lock")
	if err := os.WriteFile(path, []byte(`{"pid":999999,"nonce":"deadbeef","started_at":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := Acquire("proj") // must reclaim and succeed
	if err != nil {
		t.Fatalf("Acquire should reclaim a dead holder's lock: %v", err)
	}
	l.Release()
}

func TestReleaseHonorsNonce(t *testing.T) {
	dir := withCacheDir(t)
	l, err := Acquire("proj")
	if err != nil {
		t.Fatalf("Acquire error: %v", err)
	}
	// Simulate a later holder having reclaimed and rewritten the lock with a
	// different nonce. Our Release must NOT delete the new holder's file.
	path := filepath.Join(dir, "proj.build.lock")
	if err := os.WriteFile(path, []byte(`{"pid":1,"nonce":"someone-else","started_at":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	l.Release()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("Release must not remove a lock owned by a different nonce; stat err = %v", err)
	}
}
