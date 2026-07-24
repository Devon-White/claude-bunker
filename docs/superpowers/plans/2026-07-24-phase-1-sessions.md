# Phase 1 — Session Management Rewrite Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rebase claude-bunker's session tracking on Claude Code's official `claude agents --json` surface, deleting the fragile JSONL title hack, the PID-alignment heuristics, and the forgeable workspace Unix socket; fix the attach/teardown sibling-kill bug; and fix the Windows/rollback bugs in history seeding.

**Architecture:** A running container's claude sessions are enumerated by exec'ing `claude agents --json --cwd /workspace` inside it (validated to work: returns a JSON array of `{sessionId, pid, cwd, kind, name, status, state}` with no TTY/auth needed). That becomes the single source of truth for session identity and names. bash (shell) sessions still come from `docker top`. The host keeps a small **file-locked** store for bunker-set names (per-session title keyed by `sessionId`, per-container display name keyed by container ID) as a fallback/override, since there is no CLI verb to rename a *running* Claude session. The watcher becomes Docker-events + 3-second polling; the socket, the hook script, and its managed-settings registration are removed.

**Tech Stack:** Go 1.26, Cobra, bubbletea/lipgloss (TUI), docker/docker (moby) client. Table-driven tests with `t.Run`.

## Global Constraints

- Go 1.26+; single static binary — do NOT add new runtime dependencies.
- **Session source of truth:** `claude agents --json --cwd /workspace` exec'd *inside* the container. Verified in-container behavior: prints a JSON array (empty `[]` when no sessions), exit 0, no TTY, no auth required. PIDs in that output are container-namespace PIDs.
- **No live-rename verb exists.** `claude -n <name>` names a session only at launch; there is no `claude agents rename`. So a bunker-initiated rename of a *running* session updates only bunker's own store — it cannot change Claude's live name. Do NOT reintroduce any JSONL `custom-title` writing.
- Container constants (already defined in `internal/container`): `ContainerUser` = "claude-bunker", `ContainerWorkspace` = "/workspace", `ContainerHome` = "/home/claude-bunker". Use `ctr.ExecNonInteractive(ctx, *client.Client, id, user, argv)` for in-container commands.
- **Windows first-class:** container paths are always POSIX (`/workspace`). Use the `path` package (slash semantics), never `path/filepath`, when constructing or encoding *container* paths.
- **Test store isolation** already exists: `internal/sessions` has a `TestMain` (Phase 0) that points `CLAUDE_BUNKER_STORE_DIR` at a temp dir. New sessions tests inherit it — never touch the real `~/.claude`.
- `HasOtherActiveSessions(ctx, cli, id, myExecID) (bool, error)` and `HasAnyActiveSessions(ctx, cli, id) (bool, error)` (Phase 0) return an error on inspect failure; callers must fail closed (leave the container running when they cannot tell).
- Run `go build ./...` and `go test ./...` from repo root. The two macOS socket tests (`TestSocketListener_BasicEvent`, `TestWatcher_SocketTriggersRefresh`) are DELETED by this phase (Task 6) — after that, `go test ./internal/sessions/` should be fully green on macOS.
- Commit after each task.

---

## File Structure

- `internal/sessions/agents.go` — **new.** `AgentSession` type, `parseAgents([]byte)`, `FetchAgents(ctx, cli, id)`. The `claude agents --json` adapter. One responsibility: turn the CLI's JSON into typed sessions.
- `internal/sessions/agents_test.go` — **new.** Parser + fetch-with-stub tests.
- `internal/sessions/store.go` — **modify.** Add cross-process file locking to `jsonMapStore`.
- `internal/sessions/lock.go` — **new.** A tiny cross-platform lockfile helper.
- `internal/sessions/titles.go` — **replace.** Delete the JSONL syncer; leave a slim `SessionTitleStore` (sessionId → bunker-set title).
- `internal/sessions/manager.go` — **modify.** `FetchSnapshot`/session resolution rebuilt on `FetchAgents`; delete `sessionIDCache`, `resolveSessionTitles` PID heuristics; drop the `TitleSyncer` field.
- `internal/sessions/socket.go` — **delete.** `internal/sessions/watcher.go` — **modify** (poll + events only). `internal/sessions/watcher_test.go` — **modify** (drop socket tests).
- `internal/sessions/state.go` — **modify.** Remove the `Hook*` event-name constants (no longer used).
- `internal/sandbox/seed.go` — **modify.** Remove the `hooks` block from managed-settings + `EnsureHooksConfigured`; fix `encodeProjectPath` for container paths; add the history-seed newer-file guard.
- `internal/container/embed.go`, `constants.go`, `scripts/base.dockerfile.tmpl`, `scripts/bunker-hook.sh` — **modify/delete.** Remove the hook script from the image build.
- `cmd/sessions_tui.go` — **modify.** Remove socket callback; rebase session rename on the title store; fix `attachAndCleanup` teardown; drop the on-demand PID-heuristic resolver; drop `EnsureHooksConfigured` call.
- `cmd/sessions_attach.go` — **modify.** Fix teardown (guard + graceful stop, no `docker kill`).
- `cmd/sessions_list.go`, `cmd/sessions_stop.go`, `cmd/sessions_logs.go`, `cmd/status.go` — **modify.** Update manager construction for the new API (no `SetTitleSyncer`).

---

## GROUP A — Foundations (additive; no behavior change yet)

### Task 1: `claude agents --json` parser

**Files:**
- Create: `internal/sessions/agents.go`
- Create: `internal/sessions/agents_test.go`

**Interfaces:**
- Produces: `type AgentSession struct { SessionID, Name, CWD, Kind, Status, State string; PID int }`; `func parseAgents(data []byte) ([]AgentSession, error)`.

- [ ] **Step 1: Write the failing test**

Create `internal/sessions/agents_test.go`:

```go
package sessions

import "testing"

const agentsFixture = `[
  {"pid":123,"cwd":"/workspace","kind":"interactive","startedAt":1782936561306,"sessionId":"895a6ba5-1e40-4d94-96f7-9a3c0b1a080a","name":"fix-auth-bug","status":"idle"},
  {"pid":456,"cwd":"/workspace","kind":"interactive","startedAt":1782936799205,"sessionId":"06a6ebf8-5944-49fe-86f3-75930e0925a3","status":"waiting","waitingFor":"permission prompt"},
  {"pid":789,"id":"e5cac754","cwd":"/other","kind":"background","startedAt":1783605036338,"sessionId":"7d5a5dd7-0e07-4051-b6f7-818a1bf10e89","name":"run the test suite","status":"idle","state":"blocked"}
]`

func TestParseAgents(t *testing.T) {
	got, err := parseAgents([]byte(agentsFixture))
	if err != nil {
		t.Fatalf("parseAgents: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 sessions, got %d", len(got))
	}
	if got[0].SessionID != "895a6ba5-1e40-4d94-96f7-9a3c0b1a080a" || got[0].Name != "fix-auth-bug" || got[0].PID != 123 || got[0].Kind != "interactive" || got[0].Status != "idle" {
		t.Errorf("session 0 mismatch: %+v", got[0])
	}
	// Unnamed session: Name must be empty (field omitted in JSON).
	if got[1].Name != "" || got[1].Status != "waiting" {
		t.Errorf("session 1 (unnamed/waiting) mismatch: %+v", got[1])
	}
	// Background session with state.
	if got[2].Kind != "background" || got[2].State != "blocked" || got[2].Name != "run the test suite" {
		t.Errorf("session 2 mismatch: %+v", got[2])
	}
}

func TestParseAgents_Empty(t *testing.T) {
	got, err := parseAgents([]byte("[]\n"))
	if err != nil {
		t.Fatalf("parseAgents([]): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want 0 sessions, got %d", len(got))
	}
}

func TestParseAgents_Malformed(t *testing.T) {
	if _, err := parseAgents([]byte("not json")); err == nil {
		t.Error("expected error on malformed JSON")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/sessions/ -run TestParseAgents -v`
Expected: FAIL — `undefined: parseAgents`, `undefined: AgentSession`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/sessions/agents.go`:

```go
package sessions

import (
	"encoding/json"
	"fmt"
)

// AgentSession is one entry from `claude agents --json` — Claude Code's
// authoritative view of an active session (interactive or background).
type AgentSession struct {
	SessionID string
	Name      string // user-set/AI display name; empty when unnamed
	CWD       string
	Kind      string // "interactive" | "background"
	Status    string // "idle" | "waiting" | ...
	State     string // e.g. "blocked" | "failed" (background); may be empty
	PID       int    // container-namespace PID when the command ran in-container
}

// parseAgents decodes the JSON array printed by `claude agents --json`.
func parseAgents(data []byte) ([]AgentSession, error) {
	var raw []struct {
		SessionID string `json:"sessionId"`
		Name      string `json:"name"`
		CWD       string `json:"cwd"`
		Kind      string `json:"kind"`
		Status    string `json:"status"`
		State     string `json:"state"`
		PID       int    `json:"pid"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parsing claude agents --json: %w", err)
	}
	out := make([]AgentSession, 0, len(raw))
	for _, r := range raw {
		out = append(out, AgentSession{
			SessionID: r.SessionID,
			Name:      r.Name,
			CWD:       r.CWD,
			Kind:      r.Kind,
			Status:    r.Status,
			State:     r.State,
			PID:       r.PID,
		})
	}
	return out, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/sessions/ -run TestParseAgents -v`
Expected: PASS (all three subtests).

- [ ] **Step 5: Commit**

```bash
git add internal/sessions/agents.go internal/sessions/agents_test.go
git commit -m "feat(sessions): parse claude agents --json into AgentSession"
```

---

### Task 2: `FetchAgents` — exec the command in a container

**Files:**
- Modify: `internal/sessions/agents.go`
- Modify: `internal/sessions/agents_test.go`

**Interfaces:**
- Produces: `func FetchAgents(ctx context.Context, cli *client.Client, containerID string) ([]AgentSession, error)` — execs `claude agents --json --cwd /workspace` in the container and returns the parsed, `/workspace`-scoped sessions. Package var seam `var execAgentsJSON = func(ctx, cli, containerID) (string, error)` for tests.

- [ ] **Step 1: Write the failing test**

Add to `internal/sessions/agents_test.go`:

```go
import (
	"context"
	"testing"
	// keep existing imports
)

func TestFetchAgents_FiltersToWorkspace(t *testing.T) {
	orig := execAgentsJSON
	defer func() { execAgentsJSON = orig }()
	// Stub returns the fixture (one /workspace interactive, one /workspace waiting, one /other background).
	execAgentsJSON = func(_ context.Context, _ *client.Client, _ string) (string, error) {
		return agentsFixture, nil
	}

	got, err := FetchAgents(context.Background(), nil, "cid")
	if err != nil {
		t.Fatalf("FetchAgents: %v", err)
	}
	// Only the two /workspace sessions should remain (the /other one is filtered out).
	if len(got) != 2 {
		t.Fatalf("want 2 workspace sessions, got %d: %+v", len(got), got)
	}
	for _, s := range got {
		if s.CWD != "/workspace" {
			t.Errorf("non-workspace session leaked: %+v", s)
		}
	}
}
```

Add `"github.com/docker/docker/client"` and `"context"` to the test imports.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/sessions/ -run TestFetchAgents -v`
Expected: FAIL — `undefined: execAgentsJSON`, `undefined: FetchAgents`.

- [ ] **Step 3: Write minimal implementation**

Add to `internal/sessions/agents.go` (add imports `"context"`, `"github.com/docker/docker/client"`, and `ctr "github.com/Devon-White/claude-bunker/internal/container"`):

```go
// execAgentsJSON runs `claude agents --json --cwd /workspace` in the container
// and returns stdout. Overridable in tests.
var execAgentsJSON = func(ctx context.Context, cli *client.Client, containerID string) (string, error) {
	return ctr.ExecNonInteractive(ctx, cli, containerID, ctr.ContainerUser,
		[]string{"claude", "agents", "--json", "--cwd", ctr.ContainerWorkspace})
}

// FetchAgents enumerates the container's Claude sessions via
// `claude agents --json`, scoped to the /workspace cwd. Returns an empty slice
// (not an error) when there are no sessions.
func FetchAgents(ctx context.Context, cli *client.Client, containerID string) ([]AgentSession, error) {
	out, err := execAgentsJSON(ctx, cli, containerID)
	if err != nil {
		return nil, fmt.Errorf("running claude agents --json: %w", err)
	}
	all, err := parseAgents([]byte(out))
	if err != nil {
		return nil, err
	}
	scoped := make([]AgentSession, 0, len(all))
	for _, s := range all {
		if s.CWD == ctr.ContainerWorkspace {
			scoped = append(scoped, s)
		}
	}
	return scoped, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/sessions/ -run TestFetchAgents -v` then `go build ./...`
Expected: PASS; build clean.

- [ ] **Step 5: Commit**

```bash
git add internal/sessions/agents.go internal/sessions/agents_test.go
git commit -m "feat(sessions): FetchAgents execs claude agents --json in-container"
```

---

### Task 3: Cross-process file locking for the JSON stores

**Files:**
- Create: `internal/sessions/lock.go`
- Modify: `internal/sessions/store.go` (`Set`/`Prune` do a locked read-modify-write)
- Create: `internal/sessions/lock_test.go`

**Interfaces:**
- Produces: `func withFileLock(path string, fn func() error) error` — acquires an exclusive lock on `<path>.lock` (bounded retries), runs `fn`, releases.
- Changes: `jsonMapStore.Set` and `.Prune` re-read the file under the lock before mutating, so a concurrent process's writes are not lost.

- [ ] **Step 1: Write the failing test**

Create `internal/sessions/lock_test.go`:

```go
package sessions

import (
	"sync"
	"testing"
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/sessions/ -run TestConcurrentStoresDoNotClobber -v`
Expected: FAIL — writes are lost (each store keeps a stale in-memory cache and overwrites the file with its own view), so the final count is < 100.

- [ ] **Step 3: Write the lock helper**

Create `internal/sessions/lock.go`:

```go
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
```

- [ ] **Step 4: Make `Set`/`Prune` re-read under the lock**

In `internal/sessions/store.go`, change `Set` and `Prune` so the read-modify-write happens under `withFileLock`, re-reading the file first (so we never overwrite another process's concurrent change). Replace the bodies:

```go
// Set stores a key-value pair and persists to disk. Empty value deletes the key.
func (s *jsonMapStore) Set(key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.path()
	if p == "" {
		// in-memory only
		s.ensureLoaded()
		if value == "" {
			delete(s.cache, key)
		} else {
			s.cache[key] = value
		}
		return nil
	}
	return withFileLock(p, func() error {
		s.reload() // pick up other processes' writes before mutating
		if value == "" {
			delete(s.cache, key)
		} else {
			s.cache[key] = value
		}
		return s.persist()
	})
}

// Prune removes entries for which keep returns false. Persists if anything changed.
func (s *jsonMapStore) Prune(keep func(key string) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.path()
	if p == "" {
		s.ensureLoaded()
		for k := range s.cache {
			if !keep(k) {
				delete(s.cache, k)
			}
		}
		return
	}
	_ = withFileLock(p, func() error {
		s.reload()
		changed := false
		for k := range s.cache {
			if !keep(k) {
				delete(s.cache, k)
				changed = true
			}
		}
		if changed {
			return s.persist()
		}
		return nil
	})
}

// reload forces a re-read from disk into the cache. Must hold mu.
func (s *jsonMapStore) reload() {
	s.loaded = false
	s.ensureLoaded()
}
```

(`ensureLoaded`, `persist`, `path`, `Get`, `All` are unchanged. `Get`/`All` may serve a slightly stale cache; that is acceptable — only the mutating paths need the locked re-read.)

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/sessions/ -run TestConcurrentStoresDoNotClobber -v` then `go test ./internal/sessions/`
Expected: PASS (100 keys survive); the rest of the sessions package still passes (the two macOS socket tests remain the only failures until Task 6).

- [ ] **Step 6: Commit**

```bash
git add internal/sessions/lock.go internal/sessions/lock_test.go internal/sessions/store.go
git commit -m "fix(sessions): cross-process file lock so concurrent stores don't clobber"
```

---

## GROUP B — Engine swap

### Task 4: Rebase the Manager on `FetchAgents`

**Files:**
- Modify: `internal/sessions/manager.go`
- Modify: `internal/sessions/manager_test.go`

**Context:** Today `FetchSnapshot` → `GetProcessTree` (docker top, host PIDs) → `resolveSessionTitles` (exec `~/.claude/sessions/<pid>.json`, PID-sort alignment, JSONL title reads). Replace the claude-session identity/title resolution with `FetchAgents`. Keep `GetProcessTree` only for **bash** (shell) sessions; claude sessions and their subagents now come from `claude agents --json`.

**Interfaces:**
- `Manager` loses the `syncer TitleSyncer` and `sessionIDCache` fields (removed here / in Task 5). `FetchAgents` (Task 2) is called directly.
- Produces: a new unexported `func (m *Manager) claudeSessions(ctx, containerID string) []SessionInfo` that maps `FetchAgents` results to `SessionInfo{PID, Command:"claude", SessionID, Title, Subagents}` where interactive sessions become sessions and `kind:"background"` entries become their `Subagents`.

- [ ] **Step 1: Write the failing test**

Add to `internal/sessions/manager_test.go` a test that stubs `execAgentsJSON` and asserts `claudeSessions` maps interactive→session and background→subagent, with `name`→`Title`:

```go
func TestClaudeSessionsFromAgents(t *testing.T) {
	orig := execAgentsJSON
	defer func() { execAgentsJSON = orig }()
	execAgentsJSON = func(_ context.Context, _ *client.Client, _ string) (string, error) {
		return `[
		  {"pid":10,"cwd":"/workspace","kind":"interactive","sessionId":"sid-1","name":"fix-bug","status":"idle"},
		  {"pid":20,"cwd":"/workspace","kind":"background","sessionId":"sid-2","name":"run tests","status":"idle","state":"blocked"}
		]`, nil
	}
	mgr := NewManager(&mockClient{})
	got := mgr.claudeSessions(context.Background(), "cid")
	if len(got) != 1 {
		t.Fatalf("want 1 interactive claude session, got %d: %+v", len(got), got)
	}
	s := got[0]
	if s.Command != "claude" || s.SessionID != "sid-1" || s.Title != "fix-bug" || s.PID != "10" {
		t.Errorf("session mismatch: %+v", s)
	}
	if len(s.Subagents) != 1 || s.Subagents[0].Name != "run tests" || s.Subagents[0].PID != "20" {
		t.Errorf("subagent mismatch: %+v", s.Subagents)
	}
}
```

Add `"context"` and `"github.com/docker/docker/client"` to the manager_test imports if not present.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/sessions/ -run TestClaudeSessionsFromAgents -v`
Expected: FAIL — `mgr.claudeSessions` undefined.

- [ ] **Step 3: Implement `claudeSessions` and rewire `FetchSnapshot`**

In `internal/sessions/manager.go`:

1. Add the mapper (note: `FetchAgents` needs a `*client.Client`; guard when the manager's client isn't one, e.g. in mock tests, by calling `execAgentsJSON` through `FetchAgents` which tests stub — pass `nil` when not a real client):

```go
import "strconv" // add if missing

// claudeSessions builds SessionInfo entries for the container's Claude sessions
// from `claude agents --json`. Interactive sessions become top-level sessions;
// background sessions (subagents) are nested under the first interactive session,
// or promoted to their own entry if there is no interactive parent.
func (m *Manager) claudeSessions(ctx context.Context, containerID string) []SessionInfo {
	realCli, _ := m.cli.(*client.Client)
	agents, err := FetchAgents(ctx, realCli, containerID)
	if err != nil || len(agents) == 0 {
		return nil
	}
	var sessions []SessionInfo
	var subagents []SubagentInfo
	for _, a := range agents {
		if a.Kind == "background" {
			subagents = append(subagents, SubagentInfo{PID: strconv.Itoa(a.PID), Name: a.Name})
			continue
		}
		sessions = append(sessions, SessionInfo{
			PID:       strconv.Itoa(a.PID),
			Command:   "claude",
			SessionID: a.SessionID,
			Title:     a.Name, // Claude's authoritative name; store fallback applied in Task 5
		})
	}
	if len(sessions) > 0 {
		sessions[0].Subagents = append(sessions[0].Subagents, subagents...)
	} else if len(subagents) > 0 {
		// Background-only: surface them as sessions so the TUI still shows activity.
		for _, sa := range subagents {
			sessions = append(sessions, SessionInfo{PID: sa.PID, Command: "claude", Title: sa.Name})
		}
	}
	return sessions
}
```

2. In `FetchSnapshot`, replace the running-container block that calls `GetProcessTree` + `resolveSessionTitles` with:

```go
		if c.State == "running" {
			inspect, err := m.cli.ContainerInspect(ctx, c.ID)
			if err == nil && inspect.State != nil && inspect.State.StartedAt != "" {
				cs.StartedAt, _ = time.Parse(time.RFC3339Nano, inspect.State.StartedAt)
			}
			// Claude sessions come from `claude agents --json` (authoritative).
			cs.Sessions = m.claudeSessions(ctx, c.ID)
			// bash/shell sessions still come from the process tree.
			cs.Sessions = append(cs.Sessions, m.bashSessions(ctx, c.ID)...)
		}
```

3. Add `bashSessions` by extracting the bash-detection half of the existing `GetProcessTree` (keep `GetProcessTree` but add a filter, or add a thin wrapper):

```go
// bashSessions returns top-level bash (shell) sessions from the process tree.
// Claude sessions are intentionally excluded — those come from claude agents --json.
func (m *Manager) bashSessions(ctx context.Context, containerID string) []SessionInfo {
	all, err := m.GetProcessTree(ctx, containerID)
	if err != nil {
		return nil
	}
	var out []SessionInfo
	for _, s := range all {
		if s.Command == "bash" {
			out = append(out, s)
		}
	}
	return out
}
```

4. Delete the `syncer` and `sessionIDCache` fields, `SetTitleSyncer`, `resolveSessionTitles`, and the `cacheMu` — and the now-unused claude-detection branches of `GetProcessTree` may remain (they simply won't be selected by `bashSessions`), but remove `findSubagents`/`classifySubagent`/`parseAgentName` if nothing else uses them. (Verify with `grep`; delete only what is unreferenced.)

- [ ] **Step 4: Run test + build**

Run: `go test ./internal/sessions/ -run 'TestClaudeSessions|TestFetchSnapshot' -v` then `go build ./...`
Expected: the new test PASSES; build fails ONLY where `SetTitleSyncer`/`TitleSyncer` callers remain (cmd/*). That is expected — Task 5 removes those callers. If `go build ./internal/sessions/` alone is clean, proceed. (Do not fix cmd callers here; Task 5 owns them.)

- [ ] **Step 5: Commit**

```bash
git add internal/sessions/manager.go internal/sessions/manager_test.go
git commit -m "feat(sessions): resolve claude sessions from claude agents --json, drop PID heuristics"
```

*(If `go build ./...` must be green at every commit in your workflow, combine Task 4 and Task 5 into one commit — they are a single engine swap. Otherwise commit Task 4 with `go build ./internal/sessions/` green and let Task 5 restore full-tree green.)*

---

### Task 5: Replace `titles.go` with a slim session-title store; rewire callers

**Files:**
- Replace: `internal/sessions/titles.go`
- Modify: `internal/sessions/manager.go` (apply store fallback for empty names)
- Modify: `cmd/sessions_tui.go`, `cmd/sessions_list.go` (drop `NewTitleSyncer`/`SetTitleSyncer`)

**Interfaces:**
- Produces: `func SetSessionTitle(containerID, sessionID, title string) error` and `func GetSessionTitle(containerID, sessionID string) string`, backed by the existing sessionId-keyed file-locked `titleStore` (kept). Delete `TitleSyncer`, `jsonlTitleSyncer`, `NewTitleSyncer`, `ReadTitleFromContainer`, `writeCustomTitle`, `RefreshTitles`, `ResolveSessionIDs`, `PushTitle`, `allTitlesForContainer`.

- [ ] **Step 1: Write the failing test**

Add `internal/sessions/titles_test.go`:

```go
package sessions

import "testing"

func TestSessionTitleStoreRoundTrip(t *testing.T) {
	if err := SetSessionTitle("cid", "sid-1", "my title"); err != nil {
		t.Fatalf("SetSessionTitle: %v", err)
	}
	if got := GetSessionTitle("cid", "sid-1"); got != "my title" {
		t.Errorf("GetSessionTitle = %q, want %q", got, "my title")
	}
	if got := GetSessionTitle("cid", "absent"); got != "" {
		t.Errorf("absent title = %q, want empty", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/sessions/ -run TestSessionTitleStoreRoundTrip -v`
Expected: FAIL — `GetSessionTitle` signature/behavior differs / `SetSessionTitle` undefined (current `GetSessionTitle` exists but `SetSessionTitle` does not).

- [ ] **Step 3: Replace `titles.go`**

Replace the ENTIRE contents of `internal/sessions/titles.go` with:

```go
package sessions

// Session titles set FROM claude-bunker. Claude Code has no CLI verb to rename a
// running session, so a bunker-side rename is stored here and shown as a fallback/
// override; Claude's own `name` (from `claude agents --json`) wins when present.
//
// Stored at <store dir>/session-titles.json, keyed by "containerID:sessionID".

var titleStore = newJSONMapStore("session-titles.json")

func titleKey(containerID, sessionID string) string { return containerID + ":" + sessionID }

// SetSessionTitle records a bunker-set title for a session.
func SetSessionTitle(containerID, sessionID, title string) error {
	return titleStore.Set(titleKey(containerID, sessionID), title)
}

// GetSessionTitle returns the bunker-set title for a session, or "" if none.
func GetSessionTitle(containerID, sessionID string) string {
	return titleStore.Get(titleKey(containerID, sessionID))
}
```

- [ ] **Step 4: Apply the store fallback in `claudeSessions`**

In `manager.go` `claudeSessions`, when Claude's `name` is empty, fall back to the bunker store. Change the interactive-session mapping:

```go
		title := a.Name
		if title == "" {
			title = GetSessionTitle(containerID, a.SessionID)
		}
		sessions = append(sessions, SessionInfo{
			PID:       strconv.Itoa(a.PID),
			Command:   "claude",
			SessionID: a.SessionID,
			Title:     title,
		})
```

- [ ] **Step 5: Rewire cmd callers**

- `cmd/sessions_list.go:37` — delete the line `mgr.SetTitleSyncer(sessions.NewTitleSyncer(cli))`.
- `cmd/sessions_tui.go:41-42` — delete `syncer := sessions.NewTitleSyncer(cli)` and `mgr.SetTitleSyncer(syncer)`. The model no longer needs a `syncer`; Task 8/9 update the rename path. For now, remove the `syncer` field from `sessionsModel` and its constructor parameter, and update `newSessionsModel` call sites. (Session rename is fixed in Task 9; if it references `m.syncer` before then, temporarily route it through `sessions.SetSessionTitle`.)

- [ ] **Step 6: Run tests + build**

Run: `go build ./...` then `go test ./internal/sessions/`
Expected: build clean (all `TitleSyncer` references gone); sessions tests pass except the two macOS socket tests (removed in Task 6).

- [ ] **Step 7: Commit**

```bash
git add internal/sessions/titles.go internal/sessions/titles_test.go internal/sessions/manager.go cmd/sessions_list.go cmd/sessions_tui.go
git commit -m "refactor(sessions): replace JSONL title syncer with a slim bunker-set title store"
```

---

### Task 6: Watcher → poll + Docker events; delete the socket

**Files:**
- Delete: `internal/sessions/socket.go`
- Modify: `internal/sessions/watcher.go`
- Modify: `internal/sessions/watcher_test.go` (delete socket tests)
- Modify: `cmd/sessions_tui.go` (drop socket callback + `workspace` arg)

**Interfaces:**
- Changes: `func NewWatcher(mgr *Manager) *Watcher` (drop the `workspace` param). Delete `SetHookEventCallback`, `HookEvent`, `SocketListener`, `NewSocketListener`, `watchSocket`. `Subscribe` runs: initial snapshot + Docker events + a 3s poller (the old `pollRefresh`, now always on).

- [ ] **Step 1: Delete the socket and its tests**

```bash
git rm internal/sessions/socket.go
```

In `internal/sessions/watcher_test.go`, delete `TestSocketListener_BasicEvent` and `TestWatcher_SocketTriggersRefresh` and any helpers/imports they alone use. (These are the macOS-failing tests; removing them makes the package green on macOS.)

- [ ] **Step 2: Simplify the watcher**

In `internal/sessions/watcher.go`: remove the `socketListener` and `onHookEvent` fields, `SetHookEventCallback`, and `watchSocket`. Rewrite `NewWatcher` and `Subscribe`:

```go
func NewWatcher(mgr *Manager) *Watcher {
	return &Watcher{mgr: mgr}
}

// pollInterval is how often the watcher refreshes session state from
// `claude agents --json` while subscribed.
const pollInterval = 3 * time.Second

func (w *Watcher) Subscribe(ctx context.Context) <-chan UpdateMsg {
	out := make(chan UpdateMsg, 1)
	var wg sync.WaitGroup
	wg.Add(3) // initial + docker events + poller

	go func() { // 1. initial snapshot
		defer wg.Done()
		snap, err := w.mgr.FetchSnapshot(ctx)
		select {
		case out <- UpdateMsg{Snapshot: snap, Err: err}:
		case <-ctx.Done():
		}
	}()
	go func() { defer wg.Done(); w.watchEvents(ctx, out) }() // 2. container lifecycle
	go func() { defer wg.Done(); w.pollRefresh(ctx, out) }() // 3. session/title poll

	go func() { wg.Wait(); close(out) }()
	return out
}
```

Keep `watchEvents`, `pollRefresh`, `drainEvents` as-is; delete the `Watcher` struct's socket fields.

- [ ] **Step 3: Update the TUI wiring**

In `cmd/sessions_tui.go` `runSessionsTUI`, replace the watcher setup (lines ~44-53):

```go
	watcher := sessions.NewWatcher(mgr)
	updateCh := watcher.Subscribe(ctx)
```

Delete the `SetHookEventCallback` block entirely. Remove the now-unused `workspace := resolveWorkspace()` if nothing else uses it in this function.

- [ ] **Step 4: Run tests + build**

Run: `go build ./...` then `go test ./internal/sessions/`
Expected: build clean; **all sessions tests pass on macOS** (the two socket tests are gone).

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "refactor(sessions): drop workspace socket; watcher is docker-events + 3s poll"
```

---

### Task 7: Remove the hook script and its managed-settings registration

**Files:**
- Modify: `internal/sandbox/seed.go` (remove `hooks` block + `EnsureHooksConfigured`)
- Modify: `internal/container/embed.go`, `constants.go`, `scripts/base.dockerfile.tmpl`; delete `scripts/bunker-hook.sh`
- Modify: `internal/sessions/state.go` (remove `Hook*` constants)
- Modify: `cmd/sessions_tui.go` (`reinjectOnStart` no longer calls `EnsureHooksConfigured`)

**Interfaces:**
- Removes: `EnsureHooksConfigured`, `BunkerHookScriptPath`, `BunkerHookScriptContent`, the `bunker-hook.sh` build-context entry, the `settings["hooks"]` map, and the `Hook*` constants.

- [ ] **Step 1: Remove the hooks block from managed-settings**

In `internal/sandbox/seed.go` `writeManagedSettings`, delete the `hookGroup` closure and the `settings["hooks"] = ...` block (the section that references `sessions.HookStop` etc. and `container.BunkerHookScriptPath`). Delete `EnsureHooksConfigured`. Remove the now-unused `sessions` import if present.

- [ ] **Step 2: Remove the script from the image build**

- `internal/container/embed.go`: remove the `bunkerHookScript` embed, `BunkerHookScriptContent`, and the `{"bunker-hook.sh", ...}` entry in the build-context list.
- `internal/container/constants.go`: remove `BunkerHookScriptPath`.
- `internal/container/scripts/base.dockerfile.tmpl`: remove the `COPY bunker-hook.sh {{.BunkerHookScriptPath}}` line; remove `BunkerHookScriptPath` from `internal/container/baseimage.go`'s template data struct + assignment.
- `git rm internal/container/scripts/bunker-hook.sh`.

- [ ] **Step 3: Remove the Hook constants + fix `reinjectOnStart`**

- `internal/sessions/state.go`: delete the `HookStop`/`HookSessionStart`/... const block.
- `cmd/sessions_tui.go` `reinjectOnStart`: delete the `EnsureHooksConfigured` call (and the `sandbox.SeedOpts`/`io` usage if now unused). Auth re-injection stays.

- [ ] **Step 4: Build + test**

Run: `go build ./...` then `go test ./...`
Expected: build clean; all pass except (still) nothing new — this is deletion of unused machinery. If `genbuild`/build-context tests reference `bunker-hook.sh`, update them to match the new file list.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "refactor(sessions): remove bunker-hook.sh and its managed-settings hooks (polling replaces it)"
```

---

## GROUP C — UX & correctness fixes

### Task 8: Fix attach/teardown sibling-kill (guard + graceful stop, no docker CLI)

**Files:**
- Modify: `cmd/sessions_tui.go` (`attachAndCleanup`)
- Modify: `cmd/sessions_attach.go` (`runSessionsAttach`)
- Create: `cmd/attach_teardown_test.go` (test the shared decision helper)

**Interfaces:**
- Produces: `func teardownAfterSession(ctx context.Context, cli *client.Client, containerID, myExecID string, keep, force bool)` — stops the container via the moby client ONLY when it is safe: skips when `keep`; leaves running when other sessions are active or the check errors (unless `force`).

- [ ] **Step 1: Write the failing test**

The decision (whether to stop) is the testable core. Extract it as a pure predicate and test it. Create `cmd/attach_teardown_test.go`:

```go
package cmd

import (
	"errors"
	"testing"
)

func TestShouldStopAfterSession(t *testing.T) {
	tests := []struct {
		name              string
		keep              bool
		otherActive       bool
		checkErr          error
		force             bool
		wantStop          bool
	}{
		{"keep leaves running", true, false, nil, false, false},
		{"no others -> stop", false, false, nil, false, true},
		{"others active -> leave", false, true, nil, false, false},
		{"check error -> leave (fail closed)", false, false, errors.New("x"), false, false},
		{"check error + force -> stop", false, false, errors.New("x"), true, true},
		{"others active + force -> stop", false, true, nil, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldStopAfterSession(tt.keep, tt.otherActive, tt.checkErr, tt.force)
			if got != tt.wantStop {
				t.Errorf("shouldStopAfterSession = %v, want %v", got, tt.wantStop)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/ -run TestShouldStopAfterSession -v`
Expected: FAIL — `undefined: shouldStopAfterSession`.

- [ ] **Step 3: Implement the predicate + the teardown helper**

Add to `cmd/sessions_tui.go` (or a new `cmd/attach.go`):

```go
// shouldStopAfterSession decides whether to tear down the container when an
// attached session exits. Fails closed: when other sessions are active or the
// check errored, the container is left running unless --force.
func shouldStopAfterSession(keep, otherActive bool, checkErr error, force bool) bool {
	if keep {
		return false
	}
	if checkErr != nil || otherActive {
		return force
	}
	return true
}

// teardownAfterSession stops the container via the Docker API (SIGTERM, 10s
// grace) only when shouldStopAfterSession says it is safe.
func teardownAfterSession(ctx context.Context, cli *client.Client, containerID, myExecID string, keep, force bool) {
	active, err := ctr.HasOtherActiveSessions(ctx, cli, containerID, myExecID)
	if !shouldStopAfterSession(keep, active, err, force) {
		info("Leaving container running (other sessions active or --keep).")
		return
	}
	info("Stopping container...")
	timeout := 10
	_ = cli.ContainerStop(ctx, containerID, dockercontainer.StopOptions{Timeout: &timeout})
}
```

Add imports: `dockercontainer "github.com/docker/docker/api/types/container"` (for `StopOptions`), and ensure `ctr` and `client` are imported.

- [ ] **Step 4: Replace the two `docker kill` sites**

- `cmd/sessions_tui.go` `attachAndCleanup`: capture the exec ID from `ExecInteractive` and replace the `exec.Command("docker","kill",...)` with `teardownAfterSession(ctx, cli, containerID, execID, false, false)`. (The TUI attach has no `--keep`/`--force`; pass `false,false` — the guard still protects siblings.)

```go
func attachAndCleanup(cli *client.Client, containerID string, claudeCmd []string) error {
	ctx := context.Background()
	command := wrapWithAuth(ctx, cli, containerID, claudeCmd)
	exitCode, execID, err := ctr.ExecInteractive(ctx, cli, containerID, ctr.ContainerUser, command)
	if err != nil {
		return err
	}
	teardownAfterSession(ctx, cli, containerID, execID, false, false)
	if exitCode != 0 {
		return fmt.Errorf("session exited with code %d", exitCode)
	}
	return nil
}
```

- `cmd/sessions_attach.go` `runSessionsAttach`: capture `execID` from `ExecInteractive` (currently discarded as `_`) and replace the `exec.Command("docker","kill",...)` block:

```go
	exitCode, execID, err := ctr.ExecInteractive(ctx, dockerCli, c.ID, ctr.ContainerUser, command)
	if err != nil {
		return fmt.Errorf("exec: %w", err)
	}
	keep, _ := cmd.Flags().GetBool("keep")
	teardownAfterSession(ctx, dockerCli, c.ID, execID, keep, false)
```

Remove the now-unused `"os/exec"` import from both files if nothing else uses it.

- [ ] **Step 5: Run tests + build**

Run: `go test ./cmd/ -run TestShouldStopAfterSession -v` then `go build ./...` then `go test ./...`
Expected: predicate test passes; build clean; suites green.

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "fix(sessions): attach/teardown guards on active sessions; graceful moby stop (no docker kill)"
```

---

### Task 9: Rebase TUI session rename on the title store

**Files:**
- Modify: `cmd/sessions_tui.go`

**Context:** `handleRename` currently calls `syncer.SetTitle` (deleted) for session renames, and the `e` handler (lines ~336-362) resolves the session ID on-demand via PID heuristics (deleted). Now the session ID is always present on `SessionInfo` (from `claude agents --json`), and a session rename writes the bunker title store.

**Interfaces:** none new.

- [ ] **Step 1: Simplify the `e` (rename) handler for sessions**

In `handleKey` `case "e":` → `case "session":`, delete the on-demand `ResolveSessionIDs` block (lines ~337-362). Replace with a direct guard:

```go
			case "session":
				s := item.session
				if s.Command != "claude" || s.SessionID == "" {
					m.status = "Cannot rename: session has no Claude session ID yet"
					return m, nil
				}
				currentTitle := s.Title
				if currentTitle == "" {
					currentTitle = s.Command
				}
				ti, cmd := newRenameInput(currentTitle)
				m.rename = &renameState{
					containerID: item.containerID,
					sessionID:   s.SessionID,
					sessionPID:  s.PID,
					displayName: currentTitle,
					textInput:   ti,
				}
				return m, cmd
```

- [ ] **Step 2: Point the rename commit at the title store**

In `handleRename` `case "enter":`, replace the `r.sessionID != ""` branch (the `syncer.SetTitle` block) with:

```go
			if r.sessionID != "" {
				sessionID := r.sessionID
				return m, func() tea.Msg {
					if err := sessions.SetSessionTitle(containerID, sessionID, newName); err != nil {
						return actionErrorMsg{err: fmt.Errorf("rename session: %w", err)}
					}
					snap, err := mgrRef.FetchSnapshot(context.Background())
					if err != nil {
						return actionErrorMsg{err: err}
					}
					return snapshotMsg(sessions.UpdateMsg{Snapshot: snap})
				}
			}
```

Remove the `syncerRef := m.syncer` line and the `syncer` field from `sessionsModel`/`newSessionsModel` if still present after Task 5.

Add a one-line status hint that Claude-side name is unchanged (honest UX): after a successful session rename, the next snapshot shows Claude's `name` if set, else the bunker title — so a user renaming a session Claude already named will see Claude's name win. That is intended; no code needed beyond the fallback in Task 5.

- [ ] **Step 3: Build + smoke test**

Run: `go build ./...` then `go test ./...`
Expected: build clean; suites green. (The TUI itself is not unit-tested; verify it compiles and the sessions package tests pass.)

- [ ] **Step 4: Commit**

```bash
git add cmd/sessions_tui.go
git commit -m "fix(sessions): TUI session rename writes the bunker title store; drop PID-heuristic resolver"
```

---

### Task 10: History-seed — Windows path fix + newer-file guard

**Files:**
- Modify: `internal/sandbox/seed.go`
- Modify: `internal/sandbox/seed_test.go`

**Context:** `encodeProjectPath` (seed.go:344) uses `filepath.Abs`, so the container path `/workspace` becomes `C:\workspace` → `C--workspace` on Windows, seeding history to a directory Claude never reads. And `SeedSessionHistory` copies host files over the persistent volume with no newer-file check, able to roll back in-container progress on recreate.

**Interfaces:** `encodeProjectPath` gains a companion `encodeContainerProjectPath(containerPath string) string` that uses POSIX semantics; the container-side call uses it.

- [ ] **Step 1: Write the failing test**

Add to `internal/sandbox/seed_test.go`:

```go
func TestEncodeContainerProjectPath(t *testing.T) {
	// The container workspace is always POSIX /workspace, regardless of host OS.
	if got := encodeContainerProjectPath("/workspace"); got != "-workspace" {
		t.Errorf("encodeContainerProjectPath(/workspace) = %q, want -workspace", got)
	}
	if got := encodeContainerProjectPath("/home/claude-bunker/x"); got != "-home-claude-bunker-x" {
		t.Errorf("got %q", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/sandbox/ -run TestEncodeContainerProjectPath -v`
Expected: FAIL — `undefined: encodeContainerProjectPath`.

- [ ] **Step 3: Implement the POSIX encoder and use it for the container path**

Add to `internal/sandbox/seed.go` (import `"strings"`; do NOT use `path/filepath` for this):

```go
// encodeContainerProjectPath encodes a POSIX container path the way Claude Code
// encodes project dirs (replace "/" with "-"). Unlike encodeProjectPath, it does
// NOT call filepath.Abs, so it is correct on Windows hosts (the container path is
// always POSIX, e.g. /workspace -> -workspace).
func encodeContainerProjectPath(containerPath string) string {
	return strings.ReplaceAll(containerPath, "/", "-")
}
```

In `SeedSessionHistory`, change the container dir line to use it:

```go
	containerSessionDir := container.ContainerHome + "/.claude/projects/" + encodeContainerProjectPath(container.ContainerWorkspace) + "/"
```

(The host-side `encodeProjectPath(workspace)` at the top of `SeedSessionHistory` stays — the host path IS host-OS-specific and `filepath.Abs` is correct there.)

- [ ] **Step 4: Add the newer-file guard**

Before copying, list the container's existing session-file mtimes and skip any host file whose container counterpart is newer. Add to `SeedSessionHistory`, after computing `containerSessionDir` and before the copy:

```go
	// Guard against rolling back in-container progress: skip host files older
	// than an existing container file of the same name.
	containerMTimes := containerSessionMTimes(ctx, cli, containerID, containerSessionDir)
```

Add the helper (uses a single exec; best-effort — on any error it returns an empty map, i.e. no guard):

```go
// containerSessionMTimes returns basename -> mtime(unix secs) for *.jsonl files
// already in the container's session dir. Best-effort; empty map on error.
func containerSessionMTimes(ctx context.Context, cli *client.Client, containerID, dir string) map[string]int64 {
	out, err := container.ExecNonInteractive(ctx, cli, containerID, container.ContainerUser,
		[]string{"sh", "-c", "cd '" + dir + "' 2>/dev/null && for f in *.jsonl; do [ -f \"$f\" ] && printf '%s %s\\n' \"$(stat -c %Y \"$f\")\" \"$f\"; done"})
	res := map[string]int64{}
	if err != nil {
		return res
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var ts int64
		var name string
		if _, e := fmt.Sscanf(line, "%d %s", &ts, &name); e == nil && name != "" {
			res[name] = ts
		}
	}
	return res
}
```

Then extend the copy's include-filter to also drop host files older than the container's copy:

```go
		func(relPath string, isDir bool) bool {
			if isDir {
				return false
			}
			slash := filepath.ToSlash(relPath)
			if _, ok := allowed[slash]; !ok {
				return true // skip: not selected by size/count limits
			}
			base := slash
			if i := strings.LastIndex(base, "/"); i >= 0 {
				base = base[i+1:]
			}
			if cts, ok := containerMTimes[base]; ok {
				if info, err := os.Stat(filepath.Join(hostSessionDir, relPath)); err == nil {
					if info.ModTime().Unix() <= cts {
						return true // skip: container copy is newer or equal
					}
				}
			}
			return false // include
		}
```

- [ ] **Step 5: Run tests + build**

Run: `go test ./internal/sandbox/ -run 'TestEncode' -v` then `go build ./...` then `go test ./...`
Expected: encoder test passes; build clean; suites green.

- [ ] **Step 6: Commit**

```bash
git add internal/sandbox/seed.go internal/sandbox/seed_test.go
git commit -m "fix(sandbox): POSIX container-path encoding for history seed + newer-file rollback guard"
```

---

## Self-review notes (coverage against spec §7)

- §7.1 two stores + file locking → Task 3 (locking) + Task 5 (sessionId title store) + existing container-name store (names.go). **Deviation flagged:** the spec's "mirror display name to a Docker label `claude-bunker.displayname`" is NOT implemented — Docker does not allow changing labels on an existing container post-create, so the durable name stays in the file-locked host store (keyed by container ID; survives stop/start, lost on recreate). This is the honest, implementable form; the label idea is dropped.
- §7.2 identity from `claude agents --json`, delete titles.go/PID heuristics/`<pid>.json` → Tasks 1,2,4,5.
- §7.3 bidirectional rename bounded by the CLI → Task 9 (bunker→bunker store; Claude's name wins when set; no live push, per the verified constraint).
- §7.4 remove socket, poll + events → Task 6; hook removal → Task 7.
- §7.5 attach/teardown fixes (guard, graceful stop, moby not `docker` CLI) → Task 8. *(The run.go cleanup path already got the moby/guard fixes in Phase 0 Task 5; Task 8 fixes the two remaining `docker kill` sites in the sessions commands.)*
- §7.6 history-seed newer-file guard + Windows path → Task 10.

**Deferred to later phases (not Phase 1):** the `--interval` flag and the full poll-interval configurability (Phase 3 CLI polish); `execID` recorded-before-race in the main run path (already handled in Phase 0). The `resolveSessionTitles` `docker top` claude-detection code that becomes dead after Task 4 is deleted there; if any helper (`findSubagents`, `classifySubagent`, `parseAgentName`, `isChildOfClaude`) is left unreferenced, delete it in Task 4 Step 3.
