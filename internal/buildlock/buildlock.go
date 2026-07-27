// Package buildlock provides a per-project, fail-closed advisory file lock that
// serializes the image-build + container-create critical section across
// concurrent claude-bunker invocations. It uses a kernel advisory lock (flock
// on Unix, LockFileEx on Windows) on a persistent lock file, so the lock is
// released automatically when the holding process exits — even on crash or
// SIGKILL — with no stale files, heartbeats, or PID bookkeeping. Fail-closed:
// Acquire returns an error on timeout and the caller must not proceed unlocked.
package buildlock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Devon-White/claude-bunker/internal/config"
)

// Tunables are package vars so tests can drive timing deterministically.
var (
	timeNow        = time.Now
	acquireTimeout = 10 * time.Minute       // fail-closed deadline (builds can be long)
	retryEvery     = 100 * time.Millisecond // poll cadence while waiting for the lock
)

// errWouldBlock is returned by tryLock when another process holds the lock. The
// platform files translate the OS-specific "would block" error to this sentinel.
var errWouldBlock = errors.New("build lock is held by another process")

// Lock is a held build/create lock. Release is idempotent and goroutine-safe.
type Lock struct {
	f *os.File

	mu       sync.Mutex
	released bool
}

// LockPath returns <CacheDir>/<containerName>.build.lock (next to the .fp file).
func LockPath(containerName string) (string, error) {
	dir, err := config.CacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, containerName+".build.lock"), nil
}

// Acquire blocks until it holds the exclusive build/create lock for
// containerName, or the acquire deadline passes. It FAILS CLOSED: on timeout it
// returns an error and the caller must NOT proceed unlocked. The lock is a
// kernel advisory lock, released automatically if this process dies.
func Acquire(containerName string) (*Lock, error) {
	path, err := LockPath(containerName)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create cache dir: %w", err)
	}
	// O_CREATE (not O_EXCL): the lock file persists; the advisory LOCK is what is
	// exclusive, not the file's existence. The file is never deleted while others
	// may hold it (deleting frees the name so a new process would lock a fresh
	// inode and bypass this lock).
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock %s: %w", path, err)
	}

	deadline := timeNow().Add(acquireTimeout)
	for {
		lockErr := tryLock(f)
		if lockErr == nil {
			return &Lock{f: f}, nil
		}
		if !errors.Is(lockErr, errWouldBlock) {
			_ = f.Close()
			return nil, fmt.Errorf("lock %s: %w", path, lockErr)
		}
		if !timeNow().Before(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("timed out after %s waiting for build lock %s (another claude-bunker may be building this project)", acquireTimeout, path)
		}
		time.Sleep(retryEvery)
	}
}

// Release drops the lock. Idempotent and goroutine-safe. It unlocks and closes
// the file but does NOT remove it (removing a locked path lets a new process
// create a fresh inode and bypass the lock; a stale empty lock file is harmless).
func (l *Lock) Release() {
	l.mu.Lock()
	if l.released {
		l.mu.Unlock()
		return
	}
	l.released = true
	l.mu.Unlock()

	_ = unlock(l.f)
	_ = l.f.Close()
}
