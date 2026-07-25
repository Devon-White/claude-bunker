package sessions

import (
	"os"
	"time"
)

// withFileLock runs fn while holding an exclusive advisory lock on <path>.lock.
// Cross-platform (uses O_CREATE|O_EXCL as the lock primitive). Bounded retry;
// a stale lock older than staleAfter is reclaimed so a crashed process can't
// wedge the store forever.
func withFileLock(path string, fn func() error) error {
	lockPath := path + ".lock"
	const staleAfter = 10 * time.Second
	deadline := timeNow().Add(5 * time.Second)

	for {
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			f.Close()
			defer os.Remove(lockPath)
			return fn()
		}
		// Reclaim a stale lock left by a crashed process.
		if info, statErr := os.Stat(lockPath); statErr == nil {
			if timeNow().Sub(info.ModTime()) > staleAfter {
				os.Remove(lockPath)
				continue
			}
		}
		if timeNow().After(deadline) {
			// Give up on the lock rather than block forever; run fn anyway so a
			// wedged lock degrades to best-effort instead of a hang.
			return fn()
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// timeNow is a seam for deterministic tests (not currently overridden).
var timeNow = time.Now
