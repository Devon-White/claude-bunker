# Phase 3d — Build/Create Concurrency Lock Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the `resolveContainer` check-then-create TOCTOU (spec §9.1) so two concurrent `claude-bunker` invocations in the same project cannot both build the image and create the container — via a per-project, fail-closed file lock that covers only the build+create critical section.

**Architecture:** A new purpose-built `internal/buildlock` package provides `Acquire`/`Release` with fail-closed timeout, PID-liveness + mtime-heartbeat stale reclamation, and an ownership nonce. `cmd/run.go` acquires the lock inside the `containerID == ""` branch, **re-runs `resolveContainer` under the lock** (idempotent re-probe converts a would-be double-create into a reuse), creates only if still empty, and releases before `exec`. Release is hooked into `runner.cleanup()` so build-time `die()` and post-signal teardown also release; a hard kill mid-build is covered by the lock's own stale reclamation. The lock file lives at `<config.CacheDir()>/<containerName>.build.lock`, next to the `.fp` fingerprint.

**Tech Stack:** Go 1.26; stdlib (`crypto/rand`, `encoding/json`, `syscall`, `sync`, `time`); `golang.org/x/sys/windows` (already a dependency, used by `vt_windows.go`). No new dependencies.

## Global Constraints

- Go 1.26; no new dependencies.
- **Dependency direction:** `internal/buildlock` imports `internal/config` (for `CacheDir`); `config` MUST NOT import `buildlock` (config currently only imports `internal/log`) — a reverse edge is an import cycle.
- **FAIL CLOSED:** `Acquire` returns an error on timeout. The caller (`cmd/run.go`) aborts the build via `die()` — it NEVER proceeds unlocked. (This is the opposite of `internal/sessions`' `withFileLock`, which degrades open; do NOT reuse that lock.)
- **Scope = build+create only.** The lock covers image build + container create; it MUST be **released before `r.exec`** so the interactive session/attach never holds it (preserves §7.5 multi-session concurrency). Only acquire inside the `if r.containerID == ""` branch — never serialize pure-reuse invocations.
- **Re-check under the lock is mandatory.** After `Acquire`, re-run `resolveContainer()` and create only if `r.containerID == ""`. Wrapping only `buildAndCreate` does NOT close the TOCTOU (the check happened before the lock).
- **Release is idempotent** — it is legitimately called more than once (inline pre-exec success path + `cleanup()` + possibly the signal handler).
- **`cleanup()` releases the lock ABOVE its early-return guard** (`r.cleanedUp || !r.teardown || r.containerID == ""`), right after `platform.RestoreSaved()`, because build-time `die()` routes through `cleanup()` while `teardown` is still false.
- **Mutex ordering:** `releaseBuildLock` reads and nils `r.buildLock` under `r.mu`, then calls `l.Release()` OUTSIDE `r.mu` — never hold `r.mu` across `Release()` (deadlocks with `cleanup()`).
- **Stale reclamation is the kill-during-build backstop.** `setupSignals` is installed only AFTER the build block, so a Ctrl-C/SIGKILL mid-build runs no Go cleanup and leaks the lock file. PID-liveness (primary) + mtime-heartbeat staleness (fallback) recover it — do NOT rely on `defer`/`cleanup` alone.
- **Platform PID-liveness** via `_unix.go` / `_windows.go` build tags (matches the repo convention; `syscall.Kill(pid, 0)` on Unix, `OpenProcess`+`GetExitCodeProcess` on Windows).
- **Test isolation:** the `CLAUDE_BUNKER_CACHE_DIR` env seam points the cache dir at `t.TempDir()`; package-var seams (`timeNow`, `acquireTimeout`, `heartbeatEvery`, `staleAfter`, `retryEvery`, `pidAlive`) are saved and restored via `t.Cleanup`. Never touch the real `~/.cache`. TDD (failing test first), exact commands, no placeholders.

## Task Order & Dependencies

1. **Task 1 — `config.CacheDir()` export + `CLAUDE_BUNKER_CACHE_DIR` seam** (internal/config/fingerprint.go). Behavior-preserving. No dependency.
2. **Task 2 — `internal/buildlock` package + platform PID-liveness + unit tests** (new package). Consumes `config.CacheDir()` from Task 1.
3. **Task 3 — wire the lock into `cmd/run.go`** (runner field, `releaseBuildLock`, critical-section rewrite, `cleanup()` hook). Consumes `buildlock.Acquire`/`Release` from Task 2.

---

### Task 1: Export `config.CacheDir()` with a test-override seam

**Files:**
- Modify: `internal/config/fingerprint.go` (rename unexported `cacheDir` → exported `CacheDir`; add the `CLAUDE_BUNKER_CACHE_DIR` env seam; update the single caller `fingerprintPath`)
- Test: `internal/config/fingerprint_test.go` (add `TestCacheDir`)

**Interfaces:**
- Consumes: nothing.
- Produces: `func CacheDir() (string, error)` — returns `$CLAUDE_BUNKER_CACHE_DIR` if set, else `<UserHomeDir>/.cache/claude-bunker`. Used by Task 2's `buildlock.LockPath` and by the existing fingerprint path.

- [ ] **Step 1: Write the failing test**

Add to `internal/config/fingerprint_test.go`:

```go
func TestCacheDir(t *testing.T) {
	t.Run("honors CLAUDE_BUNKER_CACHE_DIR", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("CLAUDE_BUNKER_CACHE_DIR", dir)
		got, err := CacheDir()
		if err != nil {
			t.Fatalf("CacheDir() error: %v", err)
		}
		if got != dir {
			t.Errorf("CacheDir() = %q, want %q", got, dir)
		}
	})
	t.Run("falls back to ~/.cache/claude-bunker", func(t *testing.T) {
		t.Setenv("CLAUDE_BUNKER_CACHE_DIR", "")
		home := t.TempDir()
		t.Setenv("HOME", home)
		got, err := CacheDir()
		if err != nil {
			t.Fatalf("CacheDir() error: %v", err)
		}
		want := filepath.Join(home, ".cache", "claude-bunker")
		if got != want {
			t.Errorf("CacheDir() = %q, want %q", got, want)
		}
	})
}
```

(If `fingerprint_test.go` doesn't already import `path/filepath`, add it.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run TestCacheDir -v`
Expected: FAIL — `undefined: CacheDir`.

- [ ] **Step 3: Implement**

In `internal/config/fingerprint.go`, replace the unexported `cacheDir` helper (the function that returns `<UserHomeDir>/.cache/claude-bunker`) with:

```go
// CacheDir returns the claude-bunker cache directory, where per-container
// fingerprints (<containerName>.fp) and build locks (<containerName>.build.lock)
// live. It honors CLAUDE_BUNKER_CACHE_DIR (used for test isolation and custom
// setups) and otherwise falls back to <UserHomeDir>/.cache/claude-bunker.
func CacheDir() (string, error) {
	if d := os.Getenv("CLAUDE_BUNKER_CACHE_DIR"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cache", "claude-bunker"), nil
}
```

Then update the single caller inside `fingerprintPath` (and any other `cacheDir()` call site in the file — grep `cacheDir(` first): change `cacheDir()` → `CacheDir()`. `os` and `path/filepath` are already imported.

- [ ] **Step 4: Run tests + build**

Run: `go test ./internal/config/ -run TestCacheDir -v` (PASS) then `go build ./...` then `go test ./internal/config/...`
Expected: PASS; build clean; existing fingerprint tests still green (the HOME-based tests keep working — the env var only takes precedence when set).

- [ ] **Step 5: Commit**

```bash
git add internal/config/fingerprint.go internal/config/fingerprint_test.go
git commit -m "feat(config): export CacheDir() with CLAUDE_BUNKER_CACHE_DIR test seam"
```

---

### Task 2: `internal/buildlock` package (fail-closed lock + PID-liveness + tests)

**Files:**
- Create: `internal/buildlock/buildlock.go` (core `Lock`/`Acquire`/`Release`/`LockPath` + tunable seams)
- Create: `internal/buildlock/buildlock_unix.go` (`//go:build !windows` PID-liveness)
- Create: `internal/buildlock/buildlock_windows.go` (`//go:build windows` PID-liveness)
- Test: `internal/buildlock/buildlock_test.go`

**Interfaces:**
- Consumes: `config.CacheDir() (string, error)` (Task 1).
- Produces:
  - `func LockPath(containerName string) (string, error)` — `<CacheDir>/<containerName>.build.lock`.
  - `func Acquire(containerName string) (*Lock, error)` — blocks until held or the fail-closed deadline; reclaims a dead/stale holder.
  - `func (l *Lock) Release()` — idempotent; removes the file only if the on-disk nonce still matches.
  - Package-var seams (unexported, for tests): `timeNow`, `acquireTimeout`, `heartbeatEvery`, `staleAfter`, `retryEvery`, `pidAlive`.

- [ ] **Step 1: Write the failing test**

Create `internal/buildlock/buildlock_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/buildlock/ -v`
Expected: FAIL — package/`Acquire`/`Release`/`LockPath` undefined (build failure).

- [ ] **Step 3: Implement the core (`buildlock.go`)**

Create `internal/buildlock/buildlock.go`:

```go
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
```

- [ ] **Step 4: Implement the platform PID-liveness files**

Create `internal/buildlock/buildlock_unix.go`:

```go
//go:build !windows

package buildlock

import "syscall"

// defaultPidAlive reports whether a process with pid is alive. Signal 0 does
// error-checking without sending a signal: nil => alive; EPERM => alive but
// owned by another user; ESRCH => no such process (dead).
func defaultPidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
```

Create `internal/buildlock/buildlock_windows.go`:

```go
//go:build windows

package buildlock

import "golang.org/x/sys/windows"

// defaultPidAlive reports whether a process with pid is alive on Windows.
// os.FindProcess always "succeeds", so query the process exit code instead:
// STILL_ACTIVE (259) means running.
func defaultPidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	const stillActive = 259 // STILL_ACTIVE
	return code == stillActive
}
```

- [ ] **Step 5: Run tests + build**

Run: `go test ./internal/buildlock/ -v` (all pass) then `go build ./...` then `go vet ./internal/buildlock/...`
Expected: PASS; build clean; vet clean. (`TestAcquireFailsClosedOnTimeout` runs ~80ms.)

- [ ] **Step 6: Commit**

```bash
git add internal/buildlock/
git commit -m "feat(buildlock): fail-closed build/create lock (PID-liveness, heartbeat, nonce)"
```

---

### Task 3: Wire the build/create lock into `cmd/run.go`

**Files:**
- Modify: `cmd/run.go` (add `buildLock` runner field; `releaseBuildLock` helper; rewrite the build/create critical section; hook release into `cleanup()`)
- Test: `cmd/run_test.go` (add `TestReleaseBuildLockIdempotent` / nil-safety)

**Interfaces:**
- Consumes: `buildlock.Acquire(containerName) (*buildlock.Lock, error)`, `(*buildlock.Lock).Release()` (Task 2); the existing idempotent `resolveContainer()` and `buildAndCreate()`; `runner.cleanup()`; `die()`.
- Produces: `runner.buildLock *buildlock.Lock`; `func (r *runner) releaseBuildLock()`; a lock-guarded, re-probed build/create critical section.

- [ ] **Step 1: Write the failing test**

Add to `cmd/run_test.go`:

```go
func TestReleaseBuildLockIdempotent(t *testing.T) {
	// releaseBuildLock must be safe when no lock is held (nil field) and when
	// called more than once — it runs on the success path AND from cleanup().
	r := &runner{}
	r.releaseBuildLock() // nil lock — must not panic
	r.releaseBuildLock() // still nil — must not panic

	t.Setenv("CLAUDE_BUNKER_CACHE_DIR", t.TempDir())
	l, err := buildlock.Acquire("test-proj")
	if err != nil {
		t.Fatalf("Acquire error: %v", err)
	}
	r.buildLock = l
	r.releaseBuildLock() // releases and nils the field
	if r.buildLock != nil {
		t.Error("releaseBuildLock must nil the buildLock field")
	}
	r.releaseBuildLock() // second call — idempotent, must not panic
}
```

(Add the `buildlock` import to `cmd/run_test.go`: `"github.com/Devon-White/claude-bunker/internal/buildlock"`.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/ -run TestReleaseBuildLockIdempotent -v`
Expected: FAIL — `r.releaseBuildLock` undefined; `runner has no field buildLock`.

- [ ] **Step 3: Add the runner field + import**

In `cmd/run.go`, add the import `"github.com/Devon-White/claude-bunker/internal/buildlock"`. In the `runner` struct, add a field beside `cachedDockerfile`/`cachedScripts`:

```go
	// buildLock is held only across the build/create critical section and
	// released before exec (the interactive session must never hold it).
	buildLock *buildlock.Lock
```

- [ ] **Step 4: Add the `releaseBuildLock` helper**

Add this method (near `cleanup`):

```go
// releaseBuildLock releases the build/create lock if held. It is idempotent and
// safe to call from the success path, cleanup(), and the signal handler. The
// field is read and nil'd under r.mu, then Release() runs OUTSIDE r.mu so it
// never deadlocks with cleanup()'s own r.mu.
func (r *runner) releaseBuildLock() {
	r.mu.Lock()
	l := r.buildLock
	r.buildLock = nil
	r.mu.Unlock()
	if l != nil {
		l.Release()
	}
}
```

- [ ] **Step 5: Rewrite the build/create critical section**

Replace the current `if r.containerID == "" { r.buildAndCreate() }` block with the acquire → re-probe → create → release form:

```go
	if r.containerID == "" {
		lock, err := buildlock.Acquire(r.containerName)
		if err != nil {
			die("Could not acquire build lock: " + err.Error())
		}
		r.mu.Lock()
		r.buildLock = lock
		r.mu.Unlock()

		// Re-probe UNDER the lock: while we waited, a concurrent invocation may
		// have built the image and created the container. resolveContainer is
		// idempotent (read-only Docker probes + fingerprint recompute); a
		// now-running matching container flips r.reused=true and sets
		// r.containerID, so we skip buildAndCreate entirely — closing the TOCTOU.
		r.resolveContainer()
		if r.containerID == "" {
			r.buildAndCreate()
		}
		r.releaseBuildLock() // success path: release BEFORE exec
	}
```

- [ ] **Step 6: Hook release into `cleanup()`**

In `runner.cleanup()`, add the release call immediately after `platform.RestoreSaved()` and BEFORE `r.mu.Lock()` / the early-return guard:

```go
func (r *runner) cleanup() {
	platform.RestoreSaved()
	r.releaseBuildLock() // unconditional: build-time die()->dieCode->cleanup lands here while teardown is still false

	r.mu.Lock()
	if r.cleanedUp || !r.teardown || r.containerID == "" {
		r.mu.Unlock()
		return
	}
	// ... unchanged ...
```

- [ ] **Step 7: Run tests + build**

Run: `go test ./cmd/ -run TestReleaseBuildLockIdempotent -v` (PASS) then `go build ./...` then `go vet ./cmd/...` then `go test ./cmd/...`
Expected: PASS; build clean; vet clean; cmd suite green. `gofmt -l cmd/run.go cmd/run_test.go` prints nothing.

- [ ] **Step 8: Manual smoke (if a Docker daemon is available)**

Run two concurrent invocations in the same fresh project and confirm one builds while the other waits and then reuses (no double build), e.g. build the binary and run `./claude-bunker --dry-run` (dry-run never acquires — confirms the lock is only in the create branch) plus a real run if practical. Report what you observe. If Docker is unavailable, note that the concurrency behavior is covered by the `buildlock` unit tests and the re-probe logic.

- [ ] **Step 9: Commit**

```bash
git add cmd/run.go cmd/run_test.go
git commit -m "feat(run): serialize build/create with a fail-closed lock, re-probe under it"
```

---

## Self-Review Checklist (run after implementation, before merge)

- **§9.1 coverage:** lock covers build+create only; released before exec; re-check under the lock; fail-closed timeout; stale reclamation (PID + heartbeat); per-project key (`containerName`); cache-dir location (not workspace). ✅ mapped to Tasks 2–3.
- **TOCTOU actually closed:** the re-run of `resolveContainer()` under the lock is present (Task 3 Step 5) — not just wrapping `buildAndCreate`.
- **Release coverage:** success (inline), build-failure `die()` (via `cleanup()` above the guard), post-signal teardown (handler → `cleanup()`), and kill-during-build (stale reclamation). No path proceeds unlocked.
- **No import cycle:** `buildlock` → `config` only.
- **Dry-run unaffected:** the dry-run planning pass returns before the `containerID == ""` block, so the lock is never acquired on `--dry-run`.
