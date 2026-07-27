package buildlock

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// withCacheDir points config.CacheDir() at a temp dir and restores the tunable
// seams after the test.
func withCacheDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CLAUDE_BUNKER_CACHE_DIR", dir)
	origTimeout, origRetry := acquireTimeout, retryEvery
	t.Cleanup(func() { acquireTimeout, retryEvery = origTimeout, origRetry })
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
	if _, err := os.Stat(filepath.Join(dir, "proj.build.lock")); err != nil {
		t.Fatalf("lock file should exist after Acquire: %v", err)
	}
	l.Release()
	l.Release() // idempotent — must not panic

	// After release the lock is free: a second Acquire succeeds immediately.
	l2, err := Acquire("proj")
	if err != nil {
		t.Fatalf("second Acquire after Release should succeed: %v", err)
	}
	l2.Release()
}

func TestAcquireFailsClosedWhileHeld(t *testing.T) {
	withCacheDir(t)
	acquireTimeout = 120 * time.Millisecond
	retryEvery = 10 * time.Millisecond

	held, err := Acquire("proj")
	if err != nil {
		t.Fatalf("first Acquire error: %v", err)
	}
	defer held.Release()

	start := time.Now()
	if _, err := Acquire("proj"); err == nil {
		t.Fatal("second Acquire must FAIL CLOSED while the lock is held")
	}
	if elapsed := time.Since(start); elapsed < 90*time.Millisecond {
		t.Errorf("Acquire returned in %s; expected it to wait ~acquireTimeout before failing", elapsed)
	}
}

func TestReleaseFreesForNextHolder(t *testing.T) {
	withCacheDir(t)
	acquireTimeout = 2 * time.Second
	retryEvery = 5 * time.Millisecond

	a, err := Acquire("proj")
	if err != nil {
		t.Fatal(err)
	}

	acquired := make(chan *Lock, 1)
	go func() {
		l, err := Acquire("proj") // blocks until a releases
		if err != nil {
			t.Errorf("waiter Acquire: %v", err)
			acquired <- nil
			return
		}
		acquired <- l
	}()

	time.Sleep(30 * time.Millisecond) // let the waiter start polling
	a.Release()

	select {
	case l := <-acquired:
		if l == nil {
			t.Fatal("waiter failed to acquire after release")
		}
		l.Release()
	case <-time.After(1 * time.Second):
		t.Fatal("waiter did not acquire the lock within 1s after release")
	}
}

func TestConcurrentAcquireIsMutuallyExclusive(t *testing.T) {
	withCacheDir(t)
	acquireTimeout = 5 * time.Second
	retryEvery = 2 * time.Millisecond

	var inside int32
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l, err := Acquire("proj")
			if err != nil {
				t.Errorf("Acquire error: %v", err)
				return
			}
			if n := atomic.AddInt32(&inside, 1); n > 1 {
				t.Errorf("two concurrent lock holders (inside=%d) — mutual exclusion broken", n)
			}
			time.Sleep(3 * time.Millisecond)
			atomic.AddInt32(&inside, -1)
			l.Release()
		}()
	}
	wg.Wait()
}
