// Package buildlock provides a per-project, fail-closed file lock that guards
// the image-build + container-create critical section against concurrent
// claude-bunker invocations. Unlike internal/sessions' best-effort lock, it
// fails closed on timeout, survives multi-minute builds via an mtime heartbeat,
// reclaims a crashed holder via PID-liveness, and uses an ownership nonce so a
// reclaimed holder never deletes a successor's lock.
package buildlock

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
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
	acquireTimeout = 10 * time.Minute      // fail-closed deadline (builds can be long)
	heartbeatEvery = 3 * time.Second       // mtime touch cadence while held
	staleAfter     = 30 * time.Second      // heartbeat-staleness fallback (>> heartbeatEvery)
	retryEvery     = 50 * time.Millisecond // poll cadence while waiting
	pidAlive       = defaultPidAlive       // platform PID-liveness (overridable in tests)
)

// lockData is the JSON identity written into the lock file.
type lockData struct {
	PID       int    `json:"pid"`
	Nonce     string `json:"nonce"`
	StartedAt int64  `json:"started_at"`
}

// Lock is a held build/create lock. Release is idempotent and goroutine-safe.
type Lock struct {
	path  string
	nonce string
	stop  chan struct{}

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

// Acquire blocks until it holds the build/create lock for containerName, or the
// acquire deadline passes. It FAILS CLOSED: on timeout it returns an error and
// the caller must NOT proceed unlocked. A lock whose holder PID is dead, or
// whose heartbeat mtime is stale, is reclaimed.
func Acquire(containerName string) (*Lock, error) {
	path, err := LockPath(containerName)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create cache dir: %w", err)
	}
	nonce, err := newNonce()
	if err != nil {
		return nil, err
	}

	deadline := timeNow().Add(acquireTimeout)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			data, _ := json.Marshal(lockData{PID: os.Getpid(), Nonce: nonce, StartedAt: timeNow().Unix()})
			_, _ = f.Write(data)
			_ = f.Close()
			l := &Lock{path: path, nonce: nonce, stop: make(chan struct{})}
			go l.heartbeat()
			return l, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("open lock %s: %w", path, err)
		}

		if reclaimable(path) {
			_ = os.Remove(path) // best-effort; O_EXCL retry re-arbitrates the winner
			continue
		}
		if !timeNow().Before(deadline) {
			return nil, fmt.Errorf("timed out after %s waiting for build lock %s (another claude-bunker may be building this project; if none is, remove that file)", acquireTimeout, path)
		}
		time.Sleep(retryEvery)
	}
}

// reclaimable reports whether an existing lock file may be taken over: it is
// unparseable, its holder PID is dead, or its heartbeat mtime is stale.
func reclaimable(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return true // vanished between OpenFile and here — retry the O_EXCL create
	}
	var data lockData
	if json.Unmarshal(b, &data) != nil || data.PID == 0 {
		return true // corrupt/legacy
	}
	if !pidAlive(data.PID) {
		return true
	}
	if fi, err := os.Stat(path); err == nil && timeNow().Sub(fi.ModTime()) > staleAfter {
		return true // heartbeat died (PID may have been reused by an unrelated process)
	}
	return false
}

// heartbeat keeps the lock file's mtime fresh so a live holder is never judged
// stale during a long build.
func (l *Lock) heartbeat() {
	t := time.NewTicker(heartbeatEvery)
	defer t.Stop()
	for {
		select {
		case <-l.stop:
			return
		case <-t.C:
			now := timeNow()
			_ = os.Chtimes(l.path, now, now)
		}
	}
}

// Release drops the lock. It stops the heartbeat and removes the lock file only
// if the file still carries our nonce (so a successor that reclaimed a
// wrongly-judged-stale lock is never deleted). Idempotent and goroutine-safe.
func (l *Lock) Release() {
	l.mu.Lock()
	if l.released {
		l.mu.Unlock()
		return
	}
	l.released = true
	l.mu.Unlock()

	close(l.stop)

	b, err := os.ReadFile(l.path)
	if err != nil {
		return
	}
	var data lockData
	if json.Unmarshal(b, &data) == nil && data.Nonce == l.nonce {
		_ = os.Remove(l.path)
	}
}

func newNonce() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
