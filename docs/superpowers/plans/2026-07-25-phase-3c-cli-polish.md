# Phase 3c — CLI Polish Remainder Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Finish the §9 CLI-polish surface — docker-ping timeout, first-run hint, `--json` on prune, documented root flags with a `--` passthrough contract, a `--interval` flag for the sessions watcher, and `--dry-run` on mutating commands — as additive, byte-identical-preserving changes.

**Architecture:** All changes layer onto the existing Cobra CLI without disturbing the two load-bearing invariants: root keeps `DisableFlagParsing: true` (claude passthrough) with `extractBunkerFlags` as the sole root-flag parser, and stream discipline (payload→stdout, diagnostics→stderr) holds. New `--json`/`--dry-run` paths are separate early-return branches mirroring the established `status --json` gather/render split; documentation-only flag registration exploits the `--help` path already flipping `DisableFlagParsing=false`.

**Tech Stack:** Go 1.26, Cobra v1.10.2, moby client, lipgloss/termenv (already present). No new dependencies.

## Global Constraints

- Go 1.26; Cobra v1.10.2; no new heavy dependencies. Single static binary.
- Root command MUST keep `DisableFlagParsing: true` for normal runs (claude passthrough depends on it). `extractBunkerFlags` (cmd/run.go) is the SINGLE source of truth for root flags — never read root flags via `cmd.Flags()` on the run path (they are zero-valued because parsing is disabled).
- Stream discipline: machine payload (`--json`) → STDOUT; ALL diagnostics (info/verbose/warn/success/die/hints/dry-run lines) → STDERR via the `errW` seam in cmd/ui.go. `| jq` must see JSON only.
- Exit codes: 0 success, 1 generic error, 2 user-cancelled, 4 Docker-unavailable. Docker acquisition goes through cmd/docker.go `dockerClient()` → `Coded(ExitDockerUnavailable, ...)`.
- All new flags are ADDITIVE: existing text/interactive output and behavior stay byte-identical.
- `-v` is reserved for `--version`; verbose shorthand is `-V`.
- Platform-specific code uses `_unix.go` / `_windows.go` build tags.
- Tests: table-driven with `t.Run` subtests; isolate `HOME`/store dir (never touch the real `~/.claude` or `~/.cache`); TDD (failing test FIRST), exact run commands + expected output. NO placeholders.

## Task Order & Dependencies

1. **Task 1 — Docker ping timeout** (internal/container/client.go). Isolated.
2. **Task 2 — First-run onboarding hint** (cmd/run.go). Isolated.
3. **Task 3 — `--json` on prune** (cmd/prune.go). Isolated, additive.
4. **Task 4 — Root flag docs + `--` terminator** (cmd/root.go, cmd/run.go `extractBunkerFlags`). Touches `extractBunkerFlags`.
5. **Task 5 — `--interval` on the sessions watcher** (internal/sessions/watcher.go, cmd/sessions*). Isolated to sessions. **Deviation from spec §9:** `--interval` is scoped to the `sessions` command (where the watcher runs), not root, because it has no effect on the run path.
6. **Task 6 — `--dry-run` on mutating commands** (cmd/ui.go, cmd/run.go, cmd/prune.go, cmd/init.go, cmd/sessions_stop.go). Touches `extractBunkerFlags` again (after Task 4) and the run-path planning pass; reference symbols, not line numbers.

**Deferred to Phase 3d (its own plan):** the §9.1 build/create concurrency lock — heavier (platform PID-liveness, heartbeat, ownership nonce, signal-cleanup integration, dedicated concurrency tests), warrants a separate review cycle.

---

<!-- TASK SECTIONS SPLICED BELOW DURING ASSEMBLY -->

### Task 1: Docker ping timeout in NewClient

Bound the daemon reachability ping in `internal/container/client.go` so a Docker socket that is *present but unresponsive* (accepts the connection but never answers) fails fast instead of hanging forever. `NewClient()` is the single startup chokepoint every Docker-using command funnels through (`cmd/docker.go` `dockerClient()` → `Coded(ExitDockerUnavailable, err)` → exit 4), so bounding the ping here fixes the hang for every command at once. We wrap only the ping's context with a 5s deadline — we do **not** use a global `client.WithTimeout(...)` HTTP timeout, which would also cap long-running image build / pull / interactive exec. `NewClient`'s signature stays `() (*client.Client, error)` so no caller changes.

**Files:**
- Modify: `internal/container/client.go`
- Test (create): `internal/container/client_test.go`

**Interfaces:**
- Consumes (existing, unchanged):
  - `func NewClient() (*client.Client, error)` — `internal/container/client.go`
  - `func dockerErrorMessage(err error) string` — `internal/container/client.go`
  - `client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())` and `cli.Ping(ctx)` from `github.com/docker/docker/client` (moby v28.5.2+incompatible)
- Produces (relied on by callers / this plan):
  - `func NewClient() (*client.Client, error)` — **signature unchanged**; now internally bounds the daemon ping with `context.WithTimeout(context.Background(), 5*time.Second)`.
  - `func dockerErrorMessage(err error) string` — gains a leading `errors.Is(err, context.DeadlineExceeded)` branch returning a "did not respond within 5s" message; all existing branches unchanged.

Constraints in force: exit-code contract is preserved — a timed-out ping still returns a non-nil error out of `NewClient`, which `dockerClient()` tags `Coded(ExitDockerUnavailable, ...)` → exit 4 (no stream/diagnostic changes; this is an internal library function, no STDOUT/STDERR concern). No new dependencies. `time` and `errors` are stdlib.

---

- [ ] **Step 1: Write the failing test**

Create `internal/container/client_test.go` with exactly this content:

```go
package container

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestNewClientPingTimeout verifies NewClient bounds its daemon ping instead of
// hanging forever against a socket that is present but unresponsive. A TCP
// listener accepts the connection but never writes an HTTP response, so moby's
// Ping blocks reading the response until the ping context's deadline fires.
// Without the bounded context in NewClient this test hangs and is killed by
// `go test -timeout`; with the 5s bound NewClient returns an error promptly.
func TestNewClientPingTimeout(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	// Deferred LIFO ordering matters: ln.Close() must run BEFORE wg.Wait().
	// Registering wg.Wait() first (runs last) and ln.Close() last (runs first)
	// means closing the listener unblocks Accept, the goroutine returns, then
	// wg.Wait() completes. Registering them the other way deadlocks.
	var wg sync.WaitGroup
	defer wg.Wait()
	defer ln.Close()

	wg.Add(1)
	go func() {
		defer wg.Done()
		var held []net.Conn
		for {
			conn, err := ln.Accept()
			if err != nil {
				for _, c := range held {
					c.Close()
				}
				return
			}
			// Black hole: hold the connection open and never respond.
			held = append(held, conn)
		}
	}()

	// Point the Docker client at the black-hole listener; clear TLS so FromEnv
	// does not try to load certificates. t.Setenv restores the prior
	// environment (including any real DOCKER_HOST) when the test ends.
	t.Setenv("DOCKER_HOST", "tcp://"+ln.Addr().String())
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")

	start := time.Now()
	cli, err := NewClient()
	elapsed := time.Since(start)
	if cli != nil {
		cli.Close()
	}
	if err == nil {
		t.Fatalf("expected error from unresponsive daemon, got nil (elapsed %s)", elapsed)
	}
	if elapsed > 30*time.Second {
		t.Fatalf("NewClient did not fail fast: took %s (ping likely unbounded)", elapsed)
	}
}

// TestDockerErrorMessage covers branch selection in dockerErrorMessage,
// including the context.DeadlineExceeded special case added for the bounded
// ping. Instant, no network. dockerErrorMessage is tested directly (not via a
// live Ping) so the assertion does not depend on moby's error-wrapping internals.
func TestDockerErrorMessage(t *testing.T) {
	tests := []struct {
		name         string
		err          error
		wantContains string
	}{
		{
			name:         "deadline exceeded",
			err:          context.DeadlineExceeded,
			wantContains: "did not respond within 5s",
		},
		{
			name:         "wrapped deadline exceeded",
			err:          fmt.Errorf("Get \"http://docker/_ping\": %w", context.DeadlineExceeded),
			wantContains: "did not respond within 5s",
		},
		{
			name:         "permission denied",
			err:          fmt.Errorf("dial unix /var/run/docker.sock: connect: permission denied"),
			wantContains: "Docker permission denied",
		},
		{
			name:         "socket not found",
			err:          fmt.Errorf("dial unix /var/run/docker.sock: connect: no such file or directory"),
			wantContains: "Docker not found",
		},
		{
			name:         "generic",
			err:          fmt.Errorf("connection reset by peer"),
			wantContains: "Cannot connect to Docker",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dockerErrorMessage(tt.err)
			if !strings.Contains(got, tt.wantContains) {
				t.Fatalf("dockerErrorMessage(%v) = %q, want substring %q", tt.err, got, tt.wantContains)
			}
		})
	}
}
```

Why this is deterministic and hermetic: the TCP black-hole listener needs no Docker daemon; `t.Setenv` overrides `DOCKER_HOST` for this test only and restores it afterward. A TCP (not unix-socket) listener sidesteps the macOS `sun_path` ~104-char socket-path-length gotcha entirely. `TestNewClientPingTimeout` runs in ~5s (the bound); `TestDockerErrorMessage` is instant.

- [ ] **Step 2: Run the tests to verify they fail (before implementing)**

Run the two tests separately so the hang and the message failure are each legible. From the repo root `/Users/devon/projects/claude-bunker`:

```bash
go test ./internal/container/ -run TestDockerErrorMessage -v
```

Expected: FAIL. The `deadline exceeded` and `wrapped deadline exceeded` subtests fail because the special case does not exist yet — `dockerErrorMessage(context.DeadlineExceeded)` currently falls through to the `default` branch and returns `"Cannot connect to Docker: context deadline exceeded ..."`, which does not contain `"did not respond within 5s"`:

```
=== RUN   TestDockerErrorMessage
=== RUN   TestDockerErrorMessage/deadline_exceeded
    client_test.go:XXX: dockerErrorMessage(context deadline exceeded) = "Cannot connect to Docker: context deadline exceeded\n\n  Start Docker Desktop and try again.", want substring "did not respond within 5s"
--- FAIL: TestDockerErrorMessage (0.00s)
    --- FAIL: TestDockerErrorMessage/deadline_exceeded (0.00s)
    --- FAIL: TestDockerErrorMessage/wrapped_deadline_exceeded (0.00s)
FAIL
FAIL	github.com/Devon-White/claude-bunker/internal/container	0.2s
```

Then:

```bash
go test ./internal/container/ -run TestNewClientPingTimeout -v -timeout 30s
```

Expected: FAIL by timeout. Before the fix `NewClient` pings with `context.Background()`, so the ping against the black-hole listener never returns and `go test` kills the binary at 30s:

```
=== RUN   TestNewClientPingTimeout
panic: test timed out after 30s
	running tests:
		TestNewClientPingTimeout (30s)
...
FAIL	github.com/Devon-White/claude-bunker/internal/container	30.0XXs
```

(The `-timeout 30s` keeps this pre-fix demonstration from waiting out Go's default 10-minute test timeout. That 30s wait is the demonstration of the very hang this task removes.)

- [ ] **Step 3: Implement the fix**

Edit `internal/container/client.go`.

3a. Replace the import block (currently `context`, `fmt`, `runtime`, `strings`, and the docker client) to add `errors` and `time`:

```go
import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/docker/docker/client"
)
```

3b. In `NewClient`, replace the unbounded ping block:

```go
	_, err = cli.Ping(context.Background())
	if err != nil {
		cli.Close()
		return nil, fmt.Errorf("%s", dockerErrorMessage(err))
	}
```

with a ping bounded by a 5s context (5s matches the house convention already used at `cmd/run.go` `HasOtherActiveSessions` and `internal/container/presets.go`):

```go
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err = cli.Ping(ctx); err != nil {
		cli.Close()
		return nil, fmt.Errorf("%s", dockerErrorMessage(err))
	}
```

The full resulting `NewClient` reads:

```go
// NewClient creates a Docker client from environment settings and verifies
// the daemon is reachable.
func NewClient() (*client.Client, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("creating Docker client: %w\n%s", err, dockerInstallHint())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err = cli.Ping(ctx); err != nil {
		cli.Close()
		return nil, fmt.Errorf("%s", dockerErrorMessage(err))
	}

	return cli, nil
}
```

3c. Add the deadline special case as the first branch of `dockerErrorMessage`, before `msg := err.Error()`:

```go
// dockerErrorMessage returns a user-friendly error message with actionable
// guidance based on the type of Docker connection failure.
func dockerErrorMessage(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Sprintf("Docker daemon did not respond within 5s: %s\n\n"+
			"  The Docker socket is present but the daemon is not responding.\n"+
			"  %s", err, dockerStartHint())
	}

	msg := err.Error()
	lower := strings.ToLower(msg)

	switch {
	case strings.Contains(lower, "permission denied"):
		return fmt.Sprintf("Docker permission denied: %s\n\n"+
			"  Fix: Add your user to the docker group:\n"+
			"    sudo usermod -aG docker $USER\n"+
			"  Then log out and back in.", err)

	case strings.Contains(lower, "not found") || strings.Contains(lower, "no such file"):
		return fmt.Sprintf("Docker not found: %s\n\n"+
			"  %s", err, dockerInstallHint())

	default:
		return fmt.Sprintf("Cannot connect to Docker: %s\n\n"+
			"  %s", err, dockerStartHint())
	}
}
```

(`dockerStartHint`, `dockerInstallHint`, and the two remaining switch branches are unchanged from the existing file — repeated above only to show exact placement of the new leading `if`.)

- [ ] **Step 4: Run tests + build (expect PASS)**

From the repo root:

```bash
go test ./internal/container/ -run 'TestNewClientPingTimeout|TestDockerErrorMessage' -v -timeout 60s
```

Expected: PASS. `TestNewClientPingTimeout` now returns an error in ~5s (the bounded ping) and `TestDockerErrorMessage` passes all subtests including the two deadline cases:

```
=== RUN   TestNewClientPingTimeout
--- PASS: TestNewClientPingTimeout (5.0Xs)
=== RUN   TestDockerErrorMessage
=== RUN   TestDockerErrorMessage/deadline_exceeded
=== RUN   TestDockerErrorMessage/wrapped_deadline_exceeded
=== RUN   TestDockerErrorMessage/permission_denied
=== RUN   TestDockerErrorMessage/socket_not_found
=== RUN   TestDockerErrorMessage/generic
--- PASS: TestDockerErrorMessage (0.00s)
    --- PASS: TestDockerErrorMessage/deadline_exceeded (0.00s)
    --- PASS: TestDockerErrorMessage/wrapped_deadline_exceeded (0.00s)
    --- PASS: TestDockerErrorMessage/permission_denied (0.00s)
    --- PASS: TestDockerErrorMessage/socket_not_found (0.00s)
    --- PASS: TestDockerErrorMessage/generic (0.00s)
PASS
ok  	github.com/Devon-White/claude-bunker/internal/container	5.2s
```

Then confirm the whole module still builds and the full suite passes:

```bash
go build ./...
go test ./...
```

Expected: `go build ./...` produces no output (success). `go test ./...` reports `ok`/`PASS` for `internal/container` and the rest of the packages built by this change.

Note on pre-existing macOS failures: per project memory, two `internal/sessions` tests fail on macOS due to a unix-socket path-length limitation — that is a known pre-existing condition unrelated to this task, not a regression. Nothing in this task touches `internal/sessions`.

- [ ] **Step 5: Commit**

```bash
git add internal/container/client.go internal/container/client_test.go
git commit -m "fix(container): bound Docker daemon ping with 5s timeout in NewClient

An unresponsive-but-present Docker socket made NewClient's Ping hang
forever on context.Background(). Wrap the ping in a 5s WithTimeout so
every command that funnels through dockerClient() fails fast with exit 4
instead of blocking. Add a context.DeadlineExceeded case to
dockerErrorMessage for a clearer 'daemon not responding' message. No
signature change; no global HTTP timeout that would cap build/exec."
```

---

### Task 2: First-run onboarding hint

When a project has **no** `.devcontainer/devcontainer.json`, print one advisory line to
stderr suggesting `claude-bunker init`. The hint is purely informational (bunker runs fine
on defaults), non-blocking (never `die`/fail-closed), routed through a verbosity-gated
helper so `--quiet` / `CLAUDE_BUNKER_QUIET=1` suppress it, and printed **before** any
build/start output. Suppression is self-clearing: the file's presence *is* the signal — no
persisted "seen" flag, no keying off the transient `~/.cache` fingerprint/image/container
state. It fires only for the default `claude` launch, not `shell` (see design note below).

**Files:**
- Modify: `cmd/run.go` — add `noDevcontainer bool` field to the `runner` struct; capture the
  `present` bool from `devcontainer.LoadProjectConfig` in `loadConfig` and set the field; print
  the hint in `runInSandbox` right after `r.loadConfig(flags)`.
- Modify: `cmd/ui.go` — add a `hint(msg string)` helper (verbosity-gated, writes to `errW`).
- Test: `cmd/run_test.go` — add `TestLoadConfigSetsNoDevcontainer`.

**Interfaces:**
- Consumes:
  - `devcontainer.LoadProjectConfig(workspace string) (config.ProjectConfig, bool, error)`
    — the second return is a *file-present* flag: `false` (with `nil` error) when
    `<workspace>/.devcontainer/devcontainer.json` is absent, `true` when it parsed.
    (`internal/devcontainer/load.go:52`; absent path returns `(ProjectConfig{}, false, nil)` at line 57.)
  - `cmd/ui.go`: `errW io.Writer` (stderr seam, `ui.go:29`), package var `verbosity int`
    (`run.go:28`), `prefixStyle` and `dimStyle` (`ui.go:35`, `ui.go:41`).
- Produces:
  - `runner.noDevcontainer bool` — set to `!present` in `loadConfig`; read in `runInSandbox`.
  - `hint(msg string)` in `cmd/ui.go` — prints `[claude-bunker] <dim msg>` to `errW`, gated on
    `verbosity >= 0` (so `--quiet` suppresses it). Mirrors `info()` but dims the body so it reads
    as a soft tip, not a status line.

**Design note — scope (`claude` only, not `shell`):** `runInSandbox(passedArgs, execCmd)` is shared
by the default command (`execCmd == "claude"`) and `claude-bunker shell` (`execCmd == "shell"`).
Gate the hint on `execCmd == "claude"`. `shell` is a power-user/debug entry point where an
onboarding nudge is noise; the `init` suggestion is most relevant to the primary interactive
launch. `execCmd` is already a parameter of `runInSandbox`, so no plumbing is needed. (Do **not**
move the hint into `runDefault` — that would also be `claude`-only but would live away from the
`noDevcontainer` computation and duplicate the gating logic.)

---

- [ ] **Step 1: Write the failing test**

Add two imports and one test to `cmd/run_test.go`. The test drives the real `runner.loadConfig`
against an isolated `t.TempDir()` workspace (no Docker, no `~/.claude`, no `~/.cache` touched —
`loadConfig` only reads the workspace file plus env vars and never writes).

First, update the import block at the top of `cmd/run_test.go`:

```go
import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Devon-White/claude-bunker/internal/container"
)
```

Then append this test to the end of `cmd/run_test.go`:

```go
func TestLoadConfigSetsNoDevcontainer(t *testing.T) {
	tests := []struct {
		name             string
		writeDevcontainer bool
		wantNoDevcontainer bool
	}{
		{"absent devcontainer.json sets noDevcontainer true", false, true},
		{"present devcontainer.json sets noDevcontainer false", true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := t.TempDir()
			if tt.writeDevcontainer {
				dir := filepath.Join(ws, ".devcontainer")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatalf("MkdirAll: %v", err)
				}
				// "{}" is a valid, minimal devcontainer.json: Parse unmarshals it
				// into a zero DevContainer with no error, so LoadProjectConfig
				// returns present == true.
				if err := os.WriteFile(filepath.Join(dir, "devcontainer.json"), []byte("{}\n"), 0o644); err != nil {
					t.Fatalf("WriteFile: %v", err)
				}
			}

			r := &runner{workspace: ws}
			r.loadConfig(bunkerFlags{})

			if r.noDevcontainer != tt.wantNoDevcontainer {
				t.Errorf("noDevcontainer = %v, want %v", r.noDevcontainer, tt.wantNoDevcontainer)
			}
		})
	}
}
```

Why this is safe as a unit test: with an absent file `LoadProjectConfig` returns
`(ProjectConfig{}, false, nil)` and with `{}` it returns a zero config, `true`, `nil` — in
neither case does `loadConfig`'s `failClosed`/`NormalizeDomains` path fire `die()`, so the test
process is never terminated. `loadConfig` also calls `sandbox.DetectProxyEnv()` and reads
`ANTHROPIC_API_KEY`/`CLAUDE_CODE_OAUTH_TOKEN`, none of which affect `noDevcontainer`.

- [ ] **Step 2: Run test to verify it fails**

```
cd /Users/devon/projects/claude-bunker && go test ./cmd -run TestLoadConfigSetsNoDevcontainer
```

Expected: a **build failure** (RED) because the field does not exist yet:

```
# github.com/Devon-White/claude-bunker/cmd [github.com/Devon-White/claude-bunker/cmd.test]
./run_test.go:NN:NN: r.noDevcontainer undefined (type *runner has no field or method noDevcontainer)
FAIL	github.com/Devon-White/claude-bunker/cmd [build failed]
```

- [ ] **Step 3: Implement**

**3a — add the `noDevcontainer` field to the `runner` struct (`cmd/run.go`).** Insert it as an
isolated, blank-line-delimited field between the `auth` line and the `execID` comment block so
gofmt does not reflow the surrounding aligned columns.

Find:

```go
	auth          container.AuthTokens

	execID    string              // Docker exec ID from ExecInteractive, used for cleanup session detection
```

Replace with:

```go
	auth          container.AuthTokens

	// noDevcontainer is true when the project has no .devcontainer/devcontainer.json
	// (LoadProjectConfig reported the file absent). Drives the first-run onboarding hint.
	noDevcontainer bool

	execID    string              // Docker exec ID from ExecInteractive, used for cleanup session detection
```

**3b — capture `present` in `loadConfig` (`cmd/run.go`).** The current code discards it with `_`.

Find:

```go
	cfg, _, err := devcontainer.LoadProjectConfig(r.workspace)
```

Replace with:

```go
	cfg, present, err := devcontainer.LoadProjectConfig(r.workspace)
	r.noDevcontainer = !present
```

(The rest of `loadConfig` — the `failClosed` parse-error handling, `ExpandProjectConfig`,
`NormalizeDomains`, auth resolution, `DetectProxyEnv` — is unchanged. Note we set the field
before the `failClosed`/`die` check: on a parse error `present` is `true` (`load.go:64`), so a
malformed file correctly does **not** trigger the "no devcontainer" hint.)

**3c — add the `hint` helper (`cmd/ui.go`).** Insert it immediately after the `info` function.

Find:

```go
func info(msg string) {
	if verbosity >= 0 {
		fmt.Fprintln(errW, prefixStyle.Render("[claude-bunker]"), infoMsgStyle.Render(msg))
	}
}
```

Replace with:

```go
func info(msg string) {
	if verbosity >= 0 {
		fmt.Fprintln(errW, prefixStyle.Render("[claude-bunker]"), infoMsgStyle.Render(msg))
	}
}

// hint prints a soft, advisory tip to stderr. Like info() it honors the verbosity
// gate (verbosity >= 0), so --quiet / CLAUDE_BUNKER_QUIET=1 suppress it, and writes
// to errW so stdout stays clean for piping. The body is dimmed so a hint reads as
// optional guidance rather than a status line.
func hint(msg string) {
	if verbosity >= 0 {
		fmt.Fprintln(errW, prefixStyle.Render("[claude-bunker]"), dimStyle.Render(msg))
	}
}
```

**3d — print the hint in `runInSandbox` (`cmd/run.go`), right after `loadConfig`.** This is after
verbosity is set (from `--quiet`/`--verbose`/`CLAUDE_BUNKER_QUIET`) and before `resolveNaming`,
`resolveContainer`, and all build/start output.

Find:

```go
	r.loadConfig(flags)
	r.resolveNaming()
```

Replace with:

```go
	r.loadConfig(flags)

	// First-run onboarding hint: when the project has no .devcontainer/devcontainer.json,
	// nudge toward `claude-bunker init`. Purely advisory (bunker runs fine on defaults) and
	// non-blocking. Gated to the default `claude` launch (not `shell`) and printed before any
	// build/start output. hint() honors verbosity >= 0, so --quiet / CLAUDE_BUNKER_QUIET=1
	// suppress it. The file's presence is the self-clearing suppression signal — there is no
	// persisted "seen" flag and no dependence on the transient ~/.cache fingerprint/image state.
	if execCmd == "claude" && r.noDevcontainer {
		hint("No .devcontainer/devcontainer.json found — running with defaults. Run 'claude-bunker init' to set up a project config.")
	}

	r.resolveNaming()
```

- [ ] **Step 4: Run tests + build**

Normalize formatting (the isolated struct field and the new helper are gofmt-stable, but run it
to be certain), then run the new test verbosely and build the binary:

```
cd /Users/devon/projects/claude-bunker && gofmt -w cmd/run.go cmd/ui.go cmd/run_test.go && go test ./cmd -run TestLoadConfigSetsNoDevcontainer -v && go build -o /dev/null .
```

Expected (GREEN):

```
=== RUN   TestLoadConfigSetsNoDevcontainer
=== RUN   TestLoadConfigSetsNoDevcontainer/absent_devcontainer.json_sets_noDevcontainer_true
=== RUN   TestLoadConfigSetsNoDevcontainer/present_devcontainer.json_sets_noDevcontainer_false
--- PASS: TestLoadConfigSetsNoDevcontainer (0.00s)
    --- PASS: TestLoadConfigSetsNoDevcontainer/absent_devcontainer.json_sets_noDevcontainer_true (0.00s)
    --- PASS: TestLoadConfigSetsNoDevcontainer/present_devcontainer.json_sets_noDevcontainer_false (0.00s)
PASS
ok  	github.com/Devon-White/claude-bunker/cmd	0.0XXs
```

`go build` produces no output and exits 0.

Then confirm the whole `cmd` package still passes (existing `TestExtractBunkerFlags`,
`TestDiagnosticsGoToStderr`, etc. are untouched):

```
cd /Users/devon/projects/claude-bunker && go test ./cmd
```

Expected: `ok  github.com/Devon-White/claude-bunker/cmd`.

(Note: a full `go test ./...` on macOS may show 2 pre-existing failures in `internal/sessions`
caused by a Unix-socket path-length limit — those are unrelated to this change. Scope to `./cmd`
to verify this task.)

- [ ] **Step 5: Commit**

```
cd /Users/devon/projects/claude-bunker && git add cmd/run.go cmd/ui.go cmd/run_test.go && git commit -m "feat(run): hint at \`claude-bunker init\` on first run

Print one advisory stderr line suggesting \`claude-bunker init\` when the
project has no .devcontainer/devcontainer.json. Reuses the file-present flag
already returned by devcontainer.LoadProjectConfig (stored on the runner as
noDevcontainer) instead of a second stat. Gated to the default \`claude\`
launch, routed through a new verbosity-gated hint() helper so --quiet /
CLAUDE_BUNKER_QUIET=1 suppress it, and printed before any build output.
Self-clearing: writing the file (via init or by hand) silences it on the next
run — no persisted seen-flag, no dependence on ~/.cache state."
```

---

### Task 3: `--json` output for `prune`

Adds a `--json` flag to `claude-bunker prune`. The existing interactive / `--force` / `--all`
text path stays **byte-identical** — the JSON path is a separate early-return branch in
`runPrune`, mirroring `status.go` (`runStatus`, the `if j := ...GetBool("json")` branch) and
`sessions_list.go` (`runSessionsList`). JSON mode is non-interactive and, by DEFAULT, lists
candidates and removes NOTHING (dry-run); `--json --force` performs the removal. `--all` is
meaningless in JSON mode (JSON operates on ALL candidates) and is silently ignored, not an error.

Machine payload goes to STDOUT via `json.NewEncoder(os.Stdout)`; all diagnostics already go to
STDERR via `errW` (`cmd/ui.go`), and the JSON gather path prints nothing at all — per-item
removal failures are captured in the report's `Errors` slice instead of `warn()`.

**Files:**
- Modify: `cmd/prune.go`
- Test (create): `cmd/prune_test.go`

**Interfaces:**
- Consumes (existing, verified in `internal/container/volumes.go`):
  - `container.BunkerVolume{ Name, Kind, Project string }` — **no Size field**
  - `container.BunkerImage{ ID, Tag string; Size int64 }` — bytes; images DO carry size
  - `container.ListBunkerVolumesDetailed(ctx context.Context, cli *client.Client) ([]container.BunkerVolume, error)`
  - `container.ListBunkerImages(ctx context.Context, cli *client.Client) ([]container.BunkerImage, error)`
  - `container.RemoveVolume(ctx context.Context, cli *client.Client, name string) error`
  - `container.RemoveImageByTag(ctx context.Context, cli *client.Client, tag string) error`
  - `dockerclient` is the import alias for `github.com/docker/docker/client` already used in `cmd/prune.go`.
- Produces (relied on by tests / reviewers):
  - `type pruneReport struct{ DryRun bool; Volumes []pruneVolumeResult; Images []pruneImageResult; VolumesRemoved int; ImagesRemoved int; BytesReclaimed int64; Errors []string }` (snake_case json tags matching `statusInfo`)
  - `type pruneVolumeResult struct{ Name, Kind, Project string; Removed bool; Error string }`
  - `type pruneImageResult struct{ ID, Tag string; Size int64; Removed bool; Error string }`
  - `gatherPruneReport(ctx context.Context, cli *dockerclient.Client, remove bool) (pruneReport, error)` — lists via the container primitives (returns a REAL error on list failure), delegates tallying to `buildPruneReport`; prints nothing.
  - `buildPruneReport(vols []container.BunkerVolume, imgs []container.BunkerImage, remove bool, removeVol func(container.BunkerVolume) error, removeImg func(container.BunkerImage) error) pruneReport` — **pure test seam** (no Docker, no ctx); does the dry-run-vs-remove tallying so semantics are unit-testable without a daemon.

---

- [ ] **Step 1: Write the failing test**

  Create `cmd/prune_test.go`. It exercises the pure `buildPruneReport` seam (dry-run removes
  nothing / `--force` removes and tallies) and the report's JSON encoding. `buildPruneReport`
  is Docker-free, so no daemon, HOME, or store isolation is needed.

  ```go
  package cmd

  import (
  	"encoding/json"
  	"errors"
  	"testing"

  	"github.com/Devon-White/claude-bunker/internal/container"
  )

  // buildPruneReport is the pure core of gatherPruneReport: given already-listed
  // volumes/images plus remove callbacks, it assembles the report. These tests
  // inject fake remove funcs so no Docker daemon is required.
  func TestBuildPruneReport(t *testing.T) {
  	vols := []container.BunkerVolume{
  		{Name: "claude-bunker-history-projA", Kind: "bashhistory", Project: "projA"},
  		{Name: "claude-bunker-config-projA", Kind: "config", Project: "projA"},
  	}
  	imgs := []container.BunkerImage{
  		{ID: "aaaaaaaaaaaa", Tag: "claude-bunker:aaa", Size: 100},
  		{ID: "bbbbbbbbbbbb", Tag: "claude-bunker:bbb", Size: 200},
  	}

  	tests := []struct {
  		name           string
  		remove         bool
  		failImageTag   string // tag whose removal fails ("" = none)
  		wantDryRun     bool
  		wantVolRemoved int
  		wantImgRemoved int
  		wantBytes      int64
  		wantErrCount   int
  		wantVolCalls   int
  		wantImgCalls   int
  	}{
  		{
  			name:       "dry-run removes nothing",
  			remove:     false,
  			wantDryRun: true,
  			// all tallies zero, remove funcs never called
  		},
  		{
  			name:           "force removes all",
  			remove:         true,
  			wantDryRun:     false,
  			wantVolRemoved: 2,
  			wantImgRemoved: 2,
  			wantBytes:      300,
  			wantVolCalls:   2,
  			wantImgCalls:   2,
  		},
  		{
  			name:           "force with one image failure",
  			remove:         true,
  			failImageTag:   "claude-bunker:bbb",
  			wantDryRun:     false,
  			wantVolRemoved: 2,
  			wantImgRemoved: 1,
  			wantBytes:      100, // only the successfully-removed image counts
  			wantErrCount:   1,
  			wantVolCalls:   2,
  			wantImgCalls:   2,
  		},
  	}

  	for _, tc := range tests {
  		t.Run(tc.name, func(t *testing.T) {
  			volCalls, imgCalls := 0, 0
  			removeVol := func(v container.BunkerVolume) error {
  				volCalls++
  				return nil
  			}
  			removeImg := func(img container.BunkerImage) error {
  				imgCalls++
  				if img.Tag == tc.failImageTag {
  					return errors.New("in use")
  				}
  				return nil
  			}

  			rep := buildPruneReport(vols, imgs, tc.remove, removeVol, removeImg)

  			if rep.DryRun != tc.wantDryRun {
  				t.Errorf("DryRun = %v, want %v", rep.DryRun, tc.wantDryRun)
  			}
  			if rep.VolumesRemoved != tc.wantVolRemoved {
  				t.Errorf("VolumesRemoved = %d, want %d", rep.VolumesRemoved, tc.wantVolRemoved)
  			}
  			if rep.ImagesRemoved != tc.wantImgRemoved {
  				t.Errorf("ImagesRemoved = %d, want %d", rep.ImagesRemoved, tc.wantImgRemoved)
  			}
  			if rep.BytesReclaimed != tc.wantBytes {
  				t.Errorf("BytesReclaimed = %d, want %d", rep.BytesReclaimed, tc.wantBytes)
  			}
  			if len(rep.Errors) != tc.wantErrCount {
  				t.Errorf("len(Errors) = %d, want %d (%v)", len(rep.Errors), tc.wantErrCount, rep.Errors)
  			}
  			if volCalls != tc.wantVolCalls {
  				t.Errorf("removeVol called %d times, want %d", volCalls, tc.wantVolCalls)
  			}
  			if imgCalls != tc.wantImgCalls {
  				t.Errorf("removeImg called %d times, want %d", imgCalls, tc.wantImgCalls)
  			}

  			// The report always lists every candidate, in both modes.
  			if len(rep.Volumes) != len(vols) {
  				t.Errorf("len(Volumes) = %d, want %d", len(rep.Volumes), len(vols))
  			}
  			if len(rep.Images) != len(imgs) {
  				t.Errorf("len(Images) = %d, want %d", len(rep.Images), len(imgs))
  			}

  			// In dry-run mode every item must be Removed=false (zero mutations).
  			if !tc.remove {
  				for _, v := range rep.Volumes {
  					if v.Removed {
  						t.Errorf("dry-run volume %s marked Removed", v.Name)
  					}
  				}
  				for _, im := range rep.Images {
  					if im.Removed {
  						t.Errorf("dry-run image %s marked Removed", im.Tag)
  					}
  				}
  			}
  		})
  	}
  }

  // TestPruneReportJSON pins the snake_case wire format (mirrors TestStatusInfoJSON).
  func TestPruneReportJSON(t *testing.T) {
  	rep := pruneReport{
  		DryRun: true,
  		Volumes: []pruneVolumeResult{
  			{Name: "claude-bunker-config-projA", Kind: "config", Project: "projA"},
  		},
  		Images: []pruneImageResult{
  			{ID: "aaaaaaaaaaaa", Tag: "claude-bunker:aaa", Size: 100},
  		},
  	}
  	data, err := json.Marshal(rep)
  	if err != nil {
  		t.Fatal(err)
  	}
  	var back map[string]interface{}
  	if err := json.Unmarshal(data, &back); err != nil {
  		t.Fatal(err)
  	}
  	for _, k := range []string{"dry_run", "volumes", "images", "volumes_removed", "images_removed", "bytes_reclaimed"} {
  		if _, ok := back[k]; !ok {
  			t.Errorf("prune JSON missing key %q: %s", k, data)
  		}
  	}
  	// Errors is `omitempty`: an empty report must not emit an "errors" key.
  	if _, ok := back["errors"]; ok {
  		t.Errorf("empty Errors should be omitted, got: %s", data)
  	}
  }
  ```

- [ ] **Step 2: Run test to verify it fails**

  ```bash
  go test ./cmd/ -run 'TestBuildPruneReport|TestPruneReportJSON'
  ```

  Expected: a **build failure** (the symbols don't exist yet), e.g.:

  ```
  # github.com/Devon-White/claude-bunker/cmd [github.com/Devon-White/claude-bunker/cmd.test]
  cmd/prune_test.go:56:11: undefined: buildPruneReport
  cmd/prune_test.go:129:9: undefined: pruneReport
  cmd/prune_test.go:130:4: undefined: pruneVolumeResult
  cmd/prune_test.go:133:4: undefined: pruneImageResult
  FAIL	github.com/Devon-White/claude-bunker/cmd [build failed]
  ```

- [ ] **Step 3: Implement**

  Three edits in `cmd/prune.go`.

  **3a — add imports** (`encoding/json` and `os`). Replace the existing import block:

  ```go
  import (
  	"context"
  	"errors"
  	"fmt"

  	"github.com/charmbracelet/huh"
  	"github.com/spf13/cobra"

  	dockerclient "github.com/docker/docker/client"

  	"github.com/Devon-White/claude-bunker/internal/container"
  )
  ```

  with:

  ```go
  import (
  	"context"
  	"encoding/json"
  	"errors"
  	"fmt"
  	"os"

  	"github.com/charmbracelet/huh"
  	"github.com/spf13/cobra"

  	dockerclient "github.com/docker/docker/client"

  	"github.com/Devon-White/claude-bunker/internal/container"
  )
  ```

  **3b — register the flag** in `init()`. Replace:

  ```go
  func init() {
  	pruneCmd.Flags().Bool("force", false, "Skip confirmation prompt")
  	pruneCmd.Flags().Bool("all", false, "Remove all volumes/images without interactive selection")
  }
  ```

  with:

  ```go
  func init() {
  	pruneCmd.Flags().Bool("force", false, "Skip confirmation prompt")
  	pruneCmd.Flags().Bool("all", false, "Remove all volumes/images without interactive selection")
  	pruneCmd.Flags().Bool("json", false, "Output candidates as JSON (non-interactive; with --force, removes them)")
  }
  ```

  **3c — wire the JSON branch into `runPrune`** and add the report types + gather/build
  functions. Replace the current `runPrune` body region:

  ```go
  	force, _ := cmd.Flags().GetBool("force")
  	all, _ := cmd.Flags().GetBool("all")

  	if err := pruneVolumes(ctx, cli, force, all); err != nil {
  		return err
  	}
  ```

  with:

  ```go
  	force, _ := cmd.Flags().GetBool("force")
  	all, _ := cmd.Flags().GetBool("all")

  	// JSON mode is a separate early-return branch (mirrors status.go / sessions_list.go):
  	// non-interactive; DEFAULT is dry-run (list candidates, remove nothing); --force performs
  	// removal. --all is meaningless here (JSON operates on all candidates) and is ignored.
  	if jsonOut, _ := cmd.Flags().GetBool("json"); jsonOut {
  		rep, err := gatherPruneReport(ctx, cli, force)
  		if err != nil {
  			return err
  		}
  		enc := json.NewEncoder(os.Stdout)
  		enc.SetIndent("", "  ")
  		return enc.Encode(rep)
  	}

  	if err := pruneVolumes(ctx, cli, force, all); err != nil {
  		return err
  	}
  ```

  (`all` is still used by the unchanged text path below, so it does not become an unused
  variable.)

  Then **append** the report types and functions to the end of `cmd/prune.go` (after `pruneImages`):

  ```go
  // pruneReport is the machine-readable result of `prune --json`. When DryRun is
  // true nothing was removed (the default); with --json --force items are removed
  // and the *Removed counts / BytesReclaimed reflect it.
  type pruneReport struct {
  	DryRun         bool                `json:"dry_run"`
  	Volumes        []pruneVolumeResult `json:"volumes"`
  	Images         []pruneImageResult  `json:"images"`
  	VolumesRemoved int                 `json:"volumes_removed"`
  	ImagesRemoved  int                 `json:"images_removed"`
  	// BytesReclaimed is images-only: BunkerVolume has no Size field, so volume
  	// bytes cannot be reported without extra Docker calls (out of scope).
  	BytesReclaimed int64    `json:"bytes_reclaimed"`
  	Errors         []string `json:"errors,omitempty"`
  }

  type pruneVolumeResult struct {
  	Name    string `json:"name"`
  	Kind    string `json:"kind"`
  	Project string `json:"project"`
  	Removed bool   `json:"removed"`
  	Error   string `json:"error,omitempty"`
  }

  type pruneImageResult struct {
  	ID      string `json:"id"`
  	Tag     string `json:"tag"`
  	Size    int64  `json:"size"` // bytes; lets a dry-run consumer sum a "reclaimable" total
  	Removed bool   `json:"removed"`
  	Error   string `json:"error,omitempty"`
  }

  // gatherPruneReport lists claude-bunker volumes and images and, when remove is
  // true, removes them, assembling a pruneReport. Unlike the interactive path
  // (which warns and returns nil on a list failure), a list error here is returned
  // as a real error so runPrune exits non-zero. It prints nothing; per-item removal
  // failures land in the report's Errors slice.
  func gatherPruneReport(ctx context.Context, cli *dockerclient.Client, remove bool) (pruneReport, error) {
  	vols, err := container.ListBunkerVolumesDetailed(ctx, cli)
  	if err != nil {
  		return pruneReport{}, fmt.Errorf("failed to list volumes: %w", err)
  	}
  	imgs, err := container.ListBunkerImages(ctx, cli)
  	if err != nil {
  		return pruneReport{}, fmt.Errorf("failed to list images: %w", err)
  	}
  	removeVol := func(v container.BunkerVolume) error {
  		return container.RemoveVolume(ctx, cli, v.Name)
  	}
  	removeImg := func(img container.BunkerImage) error {
  		return container.RemoveImageByTag(ctx, cli, img.Tag)
  	}
  	return buildPruneReport(vols, imgs, remove, removeVol, removeImg), nil
  }

  // buildPruneReport is the pure core of gatherPruneReport. When remove is false it
  // lists candidates only (DryRun=true, nothing removed). When remove is true it
  // invokes removeVol/removeImg per item, recording each item's Removed/Error,
  // tallying VolumesRemoved/ImagesRemoved and (images only) BytesReclaimed; per-item
  // failures are appended to Errors. It is decoupled from Docker so callers/tests can
  // inject fake remove callbacks.
  func buildPruneReport(
  	vols []container.BunkerVolume,
  	imgs []container.BunkerImage,
  	remove bool,
  	removeVol func(container.BunkerVolume) error,
  	removeImg func(container.BunkerImage) error,
  ) pruneReport {
  	rep := pruneReport{DryRun: !remove}

  	for _, v := range vols {
  		res := pruneVolumeResult{Name: v.Name, Kind: v.Kind, Project: v.Project}
  		if remove {
  			if err := removeVol(v); err != nil {
  				res.Error = err.Error()
  				rep.Errors = append(rep.Errors, fmt.Sprintf("volume %s: %s", v.Name, err.Error()))
  			} else {
  				res.Removed = true
  				rep.VolumesRemoved++
  			}
  		}
  		rep.Volumes = append(rep.Volumes, res)
  	}

  	for _, img := range imgs {
  		res := pruneImageResult{ID: img.ID, Tag: img.Tag, Size: img.Size}
  		if remove {
  			if err := removeImg(img); err != nil {
  				res.Error = err.Error()
  				rep.Errors = append(rep.Errors, fmt.Sprintf("image %s: %s", img.Tag, err.Error()))
  			} else {
  				res.Removed = true
  				rep.ImagesRemoved++
  				rep.BytesReclaimed += img.Size
  			}
  		}
  		rep.Images = append(rep.Images, res)
  	}

  	return rep
  }
  ```

- [ ] **Step 4: Run tests + build**

  ```bash
  go test ./cmd/ -run 'TestBuildPruneReport|TestPruneReportJSON'
  go test ./cmd/
  go build -o claude-bunker .
  ```

  Expected: first two commands print `ok  github.com/Devon-White/claude-bunker/cmd`
  (the whole `cmd` package still passes — this change is additive), and the build
  produces the binary with no output. Optional manual smoke check (requires Docker;
  confirms stdout is pure JSON and diagnostics stay off it):

  ```bash
  ./claude-bunker prune --json | jq .dry_run   # -> true  (dry-run, nothing removed)
  ```

- [ ] **Step 5: Commit**

  ```bash
  git add cmd/prune.go cmd/prune_test.go
  git commit -m "feat(prune): add --json output (dry-run by default, --force removes)"
  ```

---

### Task 4: Root flag documentation + `--` passthrough terminator

**Goal:** Make `claude-bunker --help` document the ten bunker root flags and the
passthrough contract, and give a real `--` terminator in `extractBunkerFlags` so
everything after the first bare `--` is forwarded to `claude`/`bash` verbatim with
no further bunker interpretation. Additive only: normal-run behavior stays
byte-identical (root keeps `DisableFlagParsing: true`; `extractBunkerFlags` stays
the single source of truth).

**Files:**
- Modify: `cmd/root.go` — register 10 documentation-only flags on `rootCmd.Flags()` in `init()`; append a curated `Passthrough:` block to the usage template.
- Modify: `cmd/run.go` — add the `--` terminator at the top of `extractBunkerFlags`'s loop; harden the space-form credential value check.
- Test: `cmd/run_test.go` — extend the `TestExtractBunkerFlags` table; add two new test functions for the root-flag documentation.

**Interfaces:**
- Consumes (existing, verified against source):
  - `var rootCmd = &cobra.Command{... DisableFlagParsing: true ...}` (`cmd/root.go:16-30`).
  - `func init()` (`cmd/root.go:32-64`) — already registers subcommand flags and `AddCommand`s; extend it.
  - `func Execute() error` (`cmd/root.go:93-137`) — its `--help` intercept flips `rootCmd.DisableFlagParsing = false` then `SetArgs([]string{"--help"})` (`root.go:107-109`). This is the ONLY path where cobra parses root and renders `rootCmd.Flags()`. Do not change it.
  - `func extractBunkerFlags(args []string) bunkerFlags` (`cmd/run.go:144-204`) — the authoritative root-flag parser. `strings` is already imported in `run.go` (`run.go:10`).
  - `type bunkerFlags struct { auth container.AuthTokens; quiet, verbose, keep, rebuild, force, noSandbox, noColor bool; remaining []string; err error }` (`cmd/run.go:126-137`).
  - Cobra `(*Command).UsageTemplate() string` and `(*Command).SetUsageTemplate(string)` (cobra v1.10.2). `UsageTemplate()` returns cobra's default template when none has been set, so appending to it is non-destructive.
- Produces (relied on by reviewers / no later task depends on these symbols):
  - Ten documented flags on `rootCmd.Flags()`: `--keep`, `--rebuild`, `--gh-token`, `--api-key`, `--oauth-token`, `--verbose`/`-V`, `--quiet`/`-q`, `--force`, `--no-sandbox`, `--no-color`. (Explicitly NOT `--interval` — that is Task 5, scoped to the `sessions` command.)
  - `const passthroughUsage string` in `cmd/root.go`.
  - `--` terminator semantics in `extractBunkerFlags`: the first bare `--` is dropped, and `args[i+1:]` is appended to `f.remaining` verbatim; scanning stops.
  - Hardened space-form credential check: a credential value that looks like a flag (starts with `-`) is rejected with `f.err`.

---

- [ ] **Step 1: Write the failing tests (extend the `extractBunkerFlags` table)**

In `cmd/run_test.go`, inside `TestExtractBunkerFlags`, add these four cases to the
`tests` slice. Insert them immediately after the existing last case
`"only non-bunker flags all land in remaining"` (ends at `cmd/run_test.go:179`),
i.e. between that case's closing `},` and the slice's closing `}` on line 180:

```go
		{
			name: "double-dash terminator forwards the rest verbatim",
			args: []string{"--verbose", "--", "--keep"},
			want: bunkerFlags{
				verbose:   true,
				remaining: []string{"--keep"},
			},
		},
		{
			name: "double-dash at start forwards all following args",
			args: []string{"--", "--model", "opus"},
			want: bunkerFlags{
				remaining: []string{"--model", "opus"},
			},
		},
		{
			name: "double-dash stops bunker interpretation of later tokens",
			args: []string{"--keep", "--", "--gh-token", "ghp_claude_arg"},
			want: bunkerFlags{
				keep:      true,
				remaining: []string{"--gh-token", "ghp_claude_arg"},
			},
		},
		{
			// Hardened space form: a credential value that looks like a flag is
			// rejected. f.err is set; the trailing --verbose is still scanned
			// (harmless — runInSandbox dies on flags.err before reading verbose).
			name:    "credential flag followed by another flag is an error",
			args:    []string{"--gh-token", "--verbose"},
			want:    bunkerFlags{verbose: true},
			wantErr: true,
		},
```

No new imports are needed — the harness already asserts `auth.GhToken`, `verbose`,
`keep`, `remaining`, and `err` (`cmd/run_test.go:186-218`).

- [ ] **Step 2: Run the new table cases and watch them FAIL**

```bash
go test ./cmd -run TestExtractBunkerFlags -v 2>&1 | grep -E "FAIL|PASS|---" | head -40
```

Expected: the four new subtests FAIL (the pre-existing cases still PASS). Concretely,
against the current code:
- `double-dash terminator forwards the rest verbatim`: `--` falls through to `remaining` and `--keep` is still parsed → got `remaining=["--"]`, `keep=true`; want `remaining=["--keep"]`, `keep=false`. FAIL on `remaining` and `keep`.
- `double-dash at start forwards all following args`: got `remaining=["--","--model","opus"]`; want `["--model","opus"]`. FAIL on `remaining`.
- `double-dash stops bunker interpretation of later tokens`: `--gh-token ghp_claude_arg` is still extracted → got `auth.GhToken="ghp_claude_arg"`, `remaining=["--"]`; want `auth.GhToken=""`, `remaining=["--gh-token","ghp_claude_arg"]`. FAIL on `auth.GhToken` and `remaining`.
- `credential flag followed by another flag is an error`: current space form accepts `--verbose` as the value → got `auth.GhToken="--verbose"`, `err=nil`; want `err!=nil`, `verbose=true`. FAIL on `auth.GhToken`, `err`, and `verbose`.

Overall the command prints `FAIL github.com/Devon-White/claude-bunker/cmd`.

- [ ] **Step 3: Implement the `--` terminator + credential hardening in `cmd/run.go`**

Edit A — add the terminator at the very top of the scan loop. Replace this block
(`cmd/run.go:161-166`):

```go
	i := 0
	for i < len(args) {
		arg := args[i]

		// Check boolean flags
		if dest, ok := boolFlags[arg]; ok {
```

with:

```go
	i := 0
	for i < len(args) {
		arg := args[i]

		// `--` terminator: everything after the first bare `--` is forwarded to
		// claude/bash verbatim, with no further bunker-flag interpretation. The
		// `--` itself is dropped. args[i+1:] is a valid empty slice when `--` is
		// the last token, so `["--"]` alone yields no remaining args.
		if arg == "--" {
			f.remaining = append(f.remaining, args[i+1:]...)
			break
		}

		// Check boolean flags
		if dest, ok := boolFlags[arg]; ok {
```

Edit B — harden the space-form credential value so a flag-looking next token is
rejected. Replace this line (`cmd/run.go:176`):

```go
				if i+1 < len(args) && args[i+1] != "" {
```

with:

```go
				if i+1 < len(args) && args[i+1] != "" && !strings.HasPrefix(args[i+1], "-") {
```

This is safe: real credential values never begin with `-`, and the `--flag=value`
equals form (`cmd/run.go:186-196`) is untouched, so `--gh-token=-x` still works if a
value genuinely needs a leading dash. Both the terminator and the hardening apply to
`shell` too, since `shell.go:13` shares `runInSandbox` → `extractBunkerFlags`.

Edit C (doc comment, keeps the source honest) — update the `extractBunkerFlags`
comment. Replace (`cmd/run.go:139-143`):

```go
// extractBunkerFlags pulls claude-bunker-specific flags from the arg list.
// Value flags: --gh-token, --api-key, --oauth-token (each takes a non-empty
// value; a missing or empty value sets f.err).
// Boolean flags: --verbose, --quiet, --keep, --rebuild, --force, --no-sandbox.
// Returns the extracted values and the remaining args to pass through.
```

with:

```go
// extractBunkerFlags pulls claude-bunker-specific flags from the arg list.
// Value flags: --gh-token, --api-key, --oauth-token (each takes a non-empty
// value that does not look like a flag; a missing, empty, or flag-looking
// value sets f.err).
// Boolean flags: --verbose, --quiet, --keep, --rebuild, --force, --no-sandbox,
// --no-color.
// A bare "--" terminates bunker parsing: it is dropped and every token after it
// is forwarded verbatim in f.remaining.
// Returns the extracted values and the remaining args to pass through.
```

- [ ] **Step 4: Run the table tests to confirm they PASS**

```bash
go test ./cmd -run TestExtractBunkerFlags -v 2>&1 | tail -5
```

Expected: `ok  github.com/Devon-White/claude-bunker/cmd` with `--- PASS` for all
subtests including the four new ones. (Reasoning check on the tricky case
`["--verbose","--","--keep"]`: `--verbose` sets `verbose=true` at `i=0`; at `i=1`
`arg=="--"` so `remaining=append(nil, args[2:]...) = ["--keep"]` and the loop breaks —
`keep` stays false. Matches `want`.)

- [ ] **Step 5: Write the failing tests for root-flag documentation**

Append these two test functions to `cmd/run_test.go` (after `TestExtractBunkerFlags`,
same `package cmd`; `strings` is already imported at `cmd/run_test.go:6`):

```go
func TestRootFlagsRegisteredForHelp(t *testing.T) {
	// These render only on the --help path (Execute flips DisableFlagParsing=false
	// before SetArgs(["--help"])). On normal runs parsing stays disabled and
	// extractBunkerFlags is authoritative — registration here is documentation only.
	boolFlags := []string{"keep", "rebuild", "force", "no-sandbox", "no-color"}
	for _, name := range boolFlags {
		if rootCmd.Flags().Lookup(name) == nil {
			t.Errorf("root flag --%s not registered for --help documentation", name)
		}
	}
	stringFlags := []string{"gh-token", "api-key", "oauth-token"}
	for _, name := range stringFlags {
		f := rootCmd.Flags().Lookup(name)
		if f == nil {
			t.Errorf("root flag --%s not registered", name)
			continue
		}
		if f.Value.Type() != "string" {
			t.Errorf("root flag --%s type = %s, want string", name, f.Value.Type())
		}
	}
	if f := rootCmd.Flags().Lookup("verbose"); f == nil || f.Shorthand != "V" {
		t.Errorf("root --verbose must register with shorthand -V, got %+v", f)
	}
	if f := rootCmd.Flags().Lookup("quiet"); f == nil || f.Shorthand != "q" {
		t.Errorf("root --quiet must register with shorthand -q, got %+v", f)
	}
	// -v stays reserved for --version (handled in Execute); it must NOT be a
	// verbose shorthand on root.
	if f := rootCmd.Flags().ShorthandLookup("v"); f != nil {
		t.Errorf("-v must stay reserved for --version, but resolves to --%s", f.Name)
	}
	// --interval belongs to the sessions command (Task 5), not root.
	if rootCmd.Flags().Lookup("interval") != nil {
		t.Error("--interval must not be registered on root (sessions-scoped in Task 5)")
	}
}

func TestRootUsageDocumentsPassthrough(t *testing.T) {
	usage := rootCmd.UsageTemplate()
	if !strings.Contains(usage, "Passthrough:") {
		t.Error("root usage template must document the Passthrough contract")
	}
	if !strings.Contains(usage, "Use -- to force everything after it to claude verbatim") {
		t.Error("root usage template must explain the -- terminator")
	}
	if !strings.Contains(usage, "claude-bunker --keep -- --model opus") {
		t.Error("root usage template must show a -- passthrough example")
	}
}
```

Run them and watch them FAIL (root.go is still unchanged):

```bash
go test ./cmd -run 'TestRootFlagsRegisteredForHelp|TestRootUsageDocumentsPassthrough' -v 2>&1 | grep -E "FAIL|PASS|---"
```

Expected: both FAIL — `Lookup("keep")` returns nil (only `-h/--help` exists on root
today), and `UsageTemplate()` (cobra default) contains no `Passthrough:` block.

- [ ] **Step 6: Implement the root-flag registration + Passthrough block in `cmd/root.go`**

Edit A — add the `passthroughUsage` constant. Insert it immediately after the
`rootCmd` var block, before `func init()`. Replace (`cmd/root.go:30-32`):

```go
	RunE:               runDefault,
}

func init() {
```

with:

```go
	RunE:               runDefault,
}

// passthroughUsage documents the flag-passthrough contract and the `--`
// terminator, which cobra cannot infer. It is appended to the default usage
// template so it renders in `claude-bunker --help` alongside the Flags section.
const passthroughUsage = `
Passthrough:
  Unknown flags are forwarded to claude.
  Use -- to force everything after it to claude verbatim, e.g.:
    claude-bunker --keep -- --model opus -p "hi"
`

func init() {
```

Edit B — register the ten documentation-only flags and append the usage block.
Replace (`cmd/root.go:53-55`):

```go
	statusCmd.Flags().Bool("json", false, "Output as JSON")

	rootCmd.AddCommand(shellCmd)
```

with:

```go
	statusCmd.Flags().Bool("json", false, "Output as JSON")

	// Register root's own flags for --help documentation ONLY. Root keeps
	// DisableFlagParsing:true for normal runs (so unknown claude flags pass
	// through unmodified), and extractBunkerFlags (cmd/run.go) stays the single
	// source of truth for these on the run path. They render in
	// `claude-bunker --help` because Execute() flips DisableFlagParsing=false
	// before SetArgs(["--help"]). Do NOT read these via rootCmd.Flags() on the
	// run path — they are zero-valued there because parsing is disabled.
	// (--interval is intentionally absent; it is sessions-scoped in Task 5.)
	rootCmd.Flags().Bool("keep", false, "Keep the container running after exit")
	rootCmd.Flags().Bool("rebuild", false, "Force a clean image rebuild (clears cache)")
	rootCmd.Flags().String("gh-token", "", "GitHub token to inject (overrides config/env)")
	rootCmd.Flags().String("api-key", "", "Anthropic API key to inject")
	rootCmd.Flags().String("oauth-token", "", "Claude Code OAuth token to inject")
	rootCmd.Flags().BoolP("verbose", "V", false, "Show detailed output")
	rootCmd.Flags().BoolP("quiet", "q", false, "Suppress informational output")
	rootCmd.Flags().Bool("force", false, "Override fail-closed safety guards")
	rootCmd.Flags().Bool("no-sandbox", false, "Launch even if sandbox settings can't be seeded (NOT recommended)")
	rootCmd.Flags().Bool("no-color", false, "Disable ANSI color output")

	// Document the passthrough / `--` terminator contract, which cobra cannot
	// infer. Appended to the default usage template so it survives alongside the
	// auto-generated Flags section on the --help path.
	rootCmd.SetUsageTemplate(rootCmd.UsageTemplate() + passthroughUsage)

	rootCmd.AddCommand(shellCmd)
```

Note: `-v` is deliberately NOT used as the verbose shorthand — `Execute()` reserves
`-v` for `--version` (`root.go:110`), matching the subcommand convention of `-V`.

- [ ] **Step 7: Run all cmd tests + build**

```bash
go build -o /dev/null . && go test ./cmd 2>&1 | tail -5
```

Expected: build succeeds; `ok  github.com/Devon-White/claude-bunker/cmd`. All four
`extractBunkerFlags` additions and both new root tests pass, and no pre-existing test
regresses.

- [ ] **Step 8: Manually verify `--help` renders the flags and Passthrough block**

```bash
go run . --help 2>&1 | sed -n '/^Flags:/,$p'
```

Expected: the `Flags:` section lists all ten new flags plus `-h, --help`, and a
`Passthrough:` block appears at the end, e.g.:

```
Flags:
      --api-key string       Anthropic API key to inject
      --force                Override fail-closed safety guards
      --gh-token string      GitHub token to inject (overrides config/env)
  -h, --help                 help for claude-bunker
      --keep                 Keep the container running after exit
      --no-color             Disable ANSI color output
      --no-sandbox           Launch even if sandbox settings can't be seeded (NOT recommended)
      --oauth-token string   Claude Code OAuth token to inject
  -q, --quiet                Suppress informational output
      --rebuild              Force a clean image rebuild (clears cache)
  -V, --verbose              Show detailed output

Use "claude-bunker [command] --help" for more information about a command.

Passthrough:
  Unknown flags are forwarded to claude.
  Use -- to force everything after it to claude verbatim, e.g.:
    claude-bunker --keep -- --model opus -p "hi"
```

(Cobra's exact column alignment may differ; what matters is that all ten flags and the
Passthrough block are present.) Then confirm passthrough is NOT broken by an unknown
flag — this must reach Docker (not a cobra parse error):

```bash
go run . --model opus --some-unknown-claude-flag 2>&1 | head -2
```

Expected: a `[claude-bunker] ERROR: Cannot connect to Docker...` message and
`exit status 4` (Docker-unavailable) if no daemon is running — proving cobra did NOT
reject the unknown flag, i.e. `DisableFlagParsing` is still in force on the run path.
(If a Docker daemon IS running, the sandbox proceeds instead; either way there is no
"unknown flag" parse error.)

- [ ] **Step 9: Commit**

```bash
git add cmd/root.go cmd/run.go cmd/run_test.go
git commit -m "feat(cli): document root flags in --help and add -- passthrough terminator

Register the ten bunker root flags on rootCmd.Flags() for --help documentation
only (root keeps DisableFlagParsing:true; extractBunkerFlags stays authoritative),
and append a curated Passthrough block to the usage template. Implement a real --
terminator in extractBunkerFlags so tokens after the first bare -- are forwarded
to claude/bash verbatim, and reject flag-looking credential values in the space
form. Additive: normal-run behavior is unchanged."
```

---

### Task 5: `--interval` flag for the sessions watcher

Make the sessions-TUI poll cadence configurable. Today `internal/sessions/watcher.go`
hardcodes a 3-second poll (`const pollInterval = 3 * time.Second`, used at the ticker).
The ONLY production caller of `sessions.NewWatcher` is `cmd/sessions_tui.go`
(`runSessionsTUI`, which is `RunE` on the bare `sessions` command). So this flag is
scoped to the `sessions` command, NOT root (registering it on root would make it a
no-op — root has `DisableFlagParsing: true` and never constructs a `Watcher`).

**Files:**
- Modify: `internal/sessions/watcher.go` (add `pollInterval` field to `Watcher`; change `NewWatcher` signature; rename the const to `defaultPollInterval`; use the field at the ticker)
- Modify: `cmd/sessions_tui.go` (read `--interval` from the cobra flag and pass it to `NewWatcher`)
- Modify: `cmd/sessions.go` (register the `--interval` duration flag on `sessionsCmd`)
- Modify (Test): `internal/sessions/watcher_test.go` (add the new interval test; update the 3 existing `NewWatcher(mgr)` call sites to the 2-arg form)
- Create (Test): `cmd/sessions_test.go` (assert the flag is registered with a 3s default)

**Interfaces:**
- Consumes (existing, verified in live source):
  - `sessions.NewManager(cli DockerClient) *Manager` — used in tests as `NewManager(&mockClient{})`; `mockClient` is defined in `internal/sessions/watcher_test.go`.
  - `func (w *Watcher) Subscribe(ctx context.Context) <-chan UpdateMsg` — unchanged.
  - `runSessionsTUI(cmd *cobra.Command, args []string) error` — `RunE` of `sessionsCmd`; flag parsing is ENABLED on subcommands (`cmd/root.go:40` sets `sessionsCmd.DisableFlagParsing = false`), so `cmd.Flags().GetDuration(...)` is valid here.
- Produces (relied on by this task's tests + the TUI):
  - `func NewWatcher(mgr *Manager, interval time.Duration) *Watcher` — **signature CHANGE** from `NewWatcher(mgr *Manager) *Watcher`. When `interval <= 0`, falls back to `defaultPollInterval` (3s).
  - `const defaultPollInterval = 3 * time.Second` (renamed from `pollInterval`).
  - `Watcher.pollInterval time.Duration` — new unexported field holding the resolved interval.
  - `--interval` duration flag (default `3s`) on `sessionsCmd` (local flag, not persistent — it applies only to the bare `sessions` TUI path, not to `sessions list/stop/attach/logs`).

---

- [ ] **Step 1: Write the failing tests**

  **1a.** Append a new test to `internal/sessions/watcher_test.go` (it already imports
  `"testing"`, `"time"`, and defines `mockClient`). This test uses the NEW 2-arg
  signature and the new field/const, so the package will fail to compile until Step 3:

  ```go
  func TestNewWatcher_Interval(t *testing.T) {
  	mgr := NewManager(&mockClient{})

  	t.Run("stores explicit interval", func(t *testing.T) {
  		w := NewWatcher(mgr, 10*time.Second)
  		if w.pollInterval != 10*time.Second {
  			t.Errorf("pollInterval = %v, want %v", w.pollInterval, 10*time.Second)
  		}
  	})

  	t.Run("non-positive interval falls back to default", func(t *testing.T) {
  		w := NewWatcher(mgr, 0)
  		if w.pollInterval != defaultPollInterval {
  			t.Errorf("pollInterval = %v, want %v", w.pollInterval, defaultPollInterval)
  		}
  		if defaultPollInterval != 3*time.Second {
  			t.Errorf("defaultPollInterval = %v, want 3s", defaultPollInterval)
  		}
  	})
  }
  ```

  **1b.** Create `cmd/sessions_test.go` with a lightweight registration check
  (`sessionsCmd` is a package-level var in `cmd/sessions.go`; pflag renders a 3s
  `Duration` default as the string `"3s"`):

  ```go
  package cmd

  import "testing"

  func TestSessionsIntervalFlagDefault(t *testing.T) {
  	f := sessionsCmd.Flags().Lookup("interval")
  	if f == nil {
  		t.Fatal("sessions command missing --interval flag")
  	}
  	if f.DefValue != "3s" {
  		t.Errorf("--interval default = %q, want %q", f.DefValue, "3s")
  	}
  }
  ```

  Do NOT touch the three existing `NewWatcher(mgr)` call sites yet — leaving them
  keeps the failure localized to the new test line.

- [ ] **Step 2: Run tests to verify they fail**

  ```bash
  go test ./internal/sessions/ -run TestNewWatcher_Interval
  go test ./cmd/ -run TestSessionsIntervalFlagDefault
  ```

  Expected: the `internal/sessions` command fails to **build** with messages like:
  ```
  ./watcher_test.go:NN: too many arguments in call to NewWatcher
  	have (*Manager, time.Duration)
  	want (*Manager)
  ./watcher_test.go:NN: w.pollInterval undefined (type *Watcher has no field or method pollInterval)
  ./watcher_test.go:NN: undefined: defaultPollInterval
  FAIL	github.com/Devon-White/claude-bunker/internal/sessions [build failed]
  ```
  Expected: the `cmd` test compiles but FAILS at runtime:
  ```
  --- FAIL: TestSessionsIntervalFlagDefault
      sessions_test.go:6: sessions command missing --interval flag
  FAIL	github.com/Devon-White/claude-bunker/cmd
  ```

- [ ] **Step 3: Implement**

  **3a. `internal/sessions/watcher.go`** — add the field to the struct:

  Replace:
  ```go
  type Watcher struct {
  	mgr *Manager
  }
  ```
  with:
  ```go
  type Watcher struct {
  	mgr          *Manager
  	pollInterval time.Duration
  }
  ```

  Replace the constructor:
  ```go
  // NewWatcher creates a watcher.
  func NewWatcher(mgr *Manager) *Watcher {
  	return &Watcher{mgr: mgr}
  }
  ```
  with:
  ```go
  // NewWatcher creates a watcher. interval controls how often the poller
  // refreshes session state; a value <= 0 falls back to defaultPollInterval.
  func NewWatcher(mgr *Manager, interval time.Duration) *Watcher {
  	if interval <= 0 {
  		interval = defaultPollInterval
  	}
  	return &Watcher{mgr: mgr, pollInterval: interval}
  }
  ```

  Rename the const:
  ```go
  // pollInterval is how often the watcher refreshes session state from
  // `claude agents --json` while subscribed.
  const pollInterval = 3 * time.Second
  ```
  becomes:
  ```go
  // defaultPollInterval is the fallback cadence for refreshing session state
  // from `claude agents --json` while subscribed, used when NewWatcher is
  // given a non-positive interval.
  const defaultPollInterval = 3 * time.Second
  ```

  Use the field at the ticker (inside `pollRefresh`):
  ```go
  	ticker := time.NewTicker(pollInterval)
  ```
  becomes:
  ```go
  	ticker := time.NewTicker(w.pollInterval)
  ```

  **3b. `internal/sessions/watcher_test.go`** — update the three EXISTING callers
  (`TestWatcher_InitialSnapshot`, `TestWatcher_ContextCancellation`,
  `TestWatcher_EventTriggersRefresh`) so the package compiles against the new
  signature. All three lines are identical (`watcher := NewWatcher(mgr)`), so a
  single replace-all changes them to:
  ```go
  	watcher := NewWatcher(mgr, defaultPollInterval)
  ```
  (Behavior stays identical — those tests rely on the initial snapshot / docker
  events / context cancel, never on the 3s poll firing.)

  **3c. `cmd/sessions_tui.go`** — thread the flag into the constructor.

  Replace:
  ```go
  	watcher := sessions.NewWatcher(mgr)
  ```
  with:
  ```go
  	interval, _ := cmd.Flags().GetDuration("interval")
  	watcher := sessions.NewWatcher(mgr, interval)
  ```
  (When the flag is present its default is 3s, so `interval` is 3s if unset. The
  `<= 0` guard in `NewWatcher` covers the direct-call/test path; `GetDuration`'s
  error is intentionally ignored — an unregistered lookup would yield 0 and still
  fall back to 3s.)

  **3d. `cmd/sessions.go`** — register the flag and import `time`.

  Replace the import block:
  ```go
  import (
  	"github.com/spf13/cobra"
  )
  ```
  with:
  ```go
  import (
  	"time"

  	"github.com/spf13/cobra"
  )
  ```

  Replace `init()`:
  ```go
  func init() {
  	sessionsCmd.AddCommand(sessionsListCmd)
  	sessionsCmd.AddCommand(sessionsStopCmd)
  	sessionsCmd.AddCommand(sessionsAttachCmd)
  	sessionsCmd.AddCommand(sessionsLogsCmd)
  }
  ```
  with:
  ```go
  func init() {
  	// --interval is scoped to the `sessions` TUI (the only NewWatcher caller);
  	// registering it on root would make it a no-op there. The 3s literal mirrors
  	// sessions.defaultPollInterval, which stays unexported.
  	sessionsCmd.Flags().Duration("interval", 3*time.Second, "Session-watcher poll interval")

  	sessionsCmd.AddCommand(sessionsListCmd)
  	sessionsCmd.AddCommand(sessionsStopCmd)
  	sessionsCmd.AddCommand(sessionsAttachCmd)
  	sessionsCmd.AddCommand(sessionsLogsCmd)
  }
  ```

- [ ] **Step 4: Run tests + build**

  ```bash
  go test ./internal/sessions/ -run 'TestNewWatcher_Interval|TestWatcher'
  go test ./cmd/ -run TestSessionsIntervalFlagDefault
  go vet ./internal/sessions/ ./cmd/
  go build -o /dev/null .
  ```

  Expected: all four succeed —
  ```
  ok  	github.com/Devon-White/claude-bunker/internal/sessions
  ok  	github.com/Devon-White/claude-bunker/cmd
  ```
  and `go build` produces no output (exit 0).

  Note (known, pre-existing — do NOT try to fix here): running the FULL
  `go test ./internal/sessions/` on macOS shows 2 unrelated failures caused by a
  Unix-socket path-length limit, not by this change. The `-run` filters above
  scope the run to the watcher/interval tests to keep the signal clean. On Linux
  CI the full package passes.

- [ ] **Step 5: Commit**

  ```bash
  git add internal/sessions/watcher.go internal/sessions/watcher_test.go \
          cmd/sessions_tui.go cmd/sessions.go cmd/sessions_test.go
  git commit -m "feat(sessions): make watcher poll cadence configurable via --interval

  Add a --interval duration flag (default 3s) on the sessions command and
  thread it into sessions.NewWatcher, which now takes an interval and falls
  back to defaultPollInterval when given a non-positive value. Flag is scoped
  to the sessions TUI (its only caller), not root."
  ```

---

### Task 6: `--dry-run` on mutating commands

Add a single `--dry-run` flag that plans (but never performs) every host/Docker mutation on
the mutating commands: default `run`, `shell`, `prune`, `init`, and `sessions stop`. It prints
an ordered plan to **stderr** with a distinct `DRY-RUN` label (always, even under `--quiet`),
never fires `success()`, never prompts for confirmation, and exits `0` before any mutation.

Because the root command has `DisableFlagParsing: true` (claude passthrough), the flag is wired
**two ways**:
- default `run` → parsed by `extractBunkerFlags` (added to `boolFlags`).
- `shell` / `prune` / `init` / `sessions stop` → cobra `Bool("dry-run", …)` on each subcommand
  (these have `DisableFlagParsing = false` and reject unknown flags — see Corrections below),
  each setting the package-level `dryRun` var in its `RunE`.

**Scope IN:** `run`, `shell`, `prune`, `init`, `sessions stop`.
**Scope OUT (intentionally excluded):** `status`, `doctor`, `logs`, `version`, `completion`,
`sessions list`, `sessions logs` (all read-only — flag would be meaningless); `sessions` TUI
(already gated by in-app y/N confirms) and `sessions attach` (a session launcher whose only side
effect is teardown-on-exit — low value, awkward on an interactive attach).

**Files:**
- Modify: `cmd/ui.go` — package `dryRun` bool + `plan(string)` / `planf(string, …any)`; `success()` no-op under `dryRun`.
- Modify: `cmd/run.go` — `bunkerFlags.dryRun`; `"--dry-run"` in `extractBunkerFlags` `boolFlags`; `runner.dryRun`; `resolveContainer` mutation guards; `runner.planRun(...)`; planning pass in `runInSandbox`.
- Modify: `cmd/shell.go` — read the `dry-run` cobra flag into the package `dryRun` var.
- Modify: `cmd/root.go` — register `Bool("dry-run", …)` on `shellCmd` and `initCmd`.
- Modify: `cmd/prune.go` — register `Bool("dry-run", …)`; set `dryRun` in `runPrune`; plan branch in `pruneResources`.
- Modify: `cmd/init.go` — set `dryRun` in `runInit`; guard the top-level `MkdirAll`; plan branch in `writeDevContainer`.
- Modify: `cmd/sessions_stop.go` — register `Bool("dry-run", …)`; extract `stopOrPlan(...)`; call it from `runSessionsStop`.
- Test (modify): `cmd/ui_test.go`, `cmd/run_test.go`, `cmd/init_test.go`.
- Test (create): `cmd/prune_test.go`, `cmd/sessions_stop_test.go`.

**Interfaces:**
- Consumes (existing, verified):
  - `var errW io.Writer` and `var verbosity int` (cmd/ui.go).
  - `prefixStyle`, `warnLabelStyle` lipgloss styles (cmd/ui.go).
  - `func success(msg string)` (cmd/ui.go).
  - `extractBunkerFlags(args []string) bunkerFlags` and `type bunkerFlags struct{…}` (cmd/run.go).
  - `type runner struct{…}`; `(*runner).resolveContainer()`; `(*runner).buildAndCreate()` (cmd/run.go).
  - `container.ImageExists(ctx, cli *client.Client, imageTag string) bool` (internal/container/build.go).
  - `container.AuthTokens.HasSecrets() bool` (internal/container/constants.go).
  - `func writeDevContainer(workspace string, cfg config.ProjectConfig) error` (cmd/init.go).
  - `func confirmAction(title string) (bool, error)` (cmd/ui.go).
  - `pruneResources[T any](ctx, cli *dockerclient.Client, force, all bool, spec pruneSpec[T]) error`; `type pruneSpec[T any]` (cmd/prune.go).
  - `sessions.NewManager(cli DockerClient) *sessions.Manager`; `type sessions.DockerClient interface`; `type sessions.ContainerState struct{ ID, DisplayName, Status string; Sessions []SessionInfo }`; `(*Manager).StopContainer/RemoveContainer/ResolveContainer` (internal/sessions/manager.go, state.go).
  - `devcontainer.DevContainerPath(workspace string) string` (internal/devcontainer/load.go).
- Produces (relied on by reviewers / later work):
  - `var dryRun bool` (cmd/ui.go) — package planning switch.
  - `func plan(msg string)` and `func planf(format string, a ...any)` (cmd/ui.go).
  - `bunkerFlags.dryRun bool` and `runner.dryRun bool` (cmd/run.go).
  - `func (r *runner) planRun(execCmd string, args []string)` (cmd/run.go).
  - `func stopOrPlan(ctx context.Context, mgr *sessions.Manager, c sessions.ContainerState, force, remove bool) error` (cmd/sessions_stop.go).
  - `"dry-run"` cobra bool flag on `shellCmd`, `initCmd`, `pruneCmd`, `sessionsStopCmd`.

> **Cross-task note (reference symbols, not line numbers):** Task 2 adds `runner.noDevcontainer`
> and Task 4 adds a `--` terminator to `extractBunkerFlags`. This task edits the same functions
> (`runInSandbox`, `extractBunkerFlags`). Anchor edits on the surrounding code shown below, not on
> line numbers, so the three tasks merge cleanly.

---

- [ ] **Step 1: Write the failing tests for the shared helper + run-path flag**

Append to `cmd/ui_test.go`. Add `"strings"` to its import block (currently `bytes`, `errors`, `testing`):

```go
func TestPlanAlwaysPrintsIgnoringQuiet(t *testing.T) {
	var buf bytes.Buffer
	origErr := errW
	errW = &buf
	t.Cleanup(func() { errW = origErr })

	origV := verbosity
	verbosity = -1 // --quiet: info/warn/success are suppressed, plan must NOT be
	t.Cleanup(func() { verbosity = origV })

	plan("would do a thing")
	planf("would remove %s", "widget")

	out := buf.String()
	if !strings.Contains(out, "would do a thing") {
		t.Errorf("plan() must print even under --quiet; got %q", out)
	}
	if !strings.Contains(out, "would remove widget") {
		t.Errorf("planf() must print even under --quiet; got %q", out)
	}
}

func TestSuccessSuppressedUnderDryRun(t *testing.T) {
	var buf bytes.Buffer
	origErr := errW
	errW = &buf
	t.Cleanup(func() { errW = origErr })

	origV := verbosity
	verbosity = 0
	t.Cleanup(func() { verbosity = origV })

	origDry := dryRun
	dryRun = true
	t.Cleanup(func() { dryRun = origDry })

	success("Saved something")
	if buf.Len() != 0 {
		t.Errorf("success() must not fire under dryRun; got %q", buf.String())
	}
}
```

Append to `cmd/run_test.go` (imports already include `bytes`? no — it has `errors`, `slices`,
`strings`, `testing`, `container`; add `"bytes"`):

```go
func TestExtractBunkerFlags_DryRun(t *testing.T) {
	f := extractBunkerFlags([]string{"--dry-run", "--model", "opus"})
	if !f.dryRun {
		t.Error("--dry-run must set bunkerFlags.dryRun")
	}
	if slices.Contains(f.remaining, "--dry-run") {
		t.Error("--dry-run must be consumed, not passed through to claude")
	}
	if !slices.Equal(f.remaining, []string{"--model", "opus"}) {
		t.Errorf("remaining = %v, want [--model opus]", f.remaining)
	}
}

// planRun's reuse branch performs no Docker calls (r.ctx/r.cli/ImageExists are
// only touched on the fresh-build branch), so it is unit-testable without a daemon.
func TestPlanRun_ReusePathNoDockerCalls(t *testing.T) {
	var buf bytes.Buffer
	origErr := errW
	errW = &buf
	t.Cleanup(func() { errW = origErr })

	r := &runner{
		reused:        true,
		containerName: "proj-abc123",
		imageTag:      "claude-bunker:proj-abc123",
		auth:          container.AuthTokens{OAuthToken: "tok"},
	}
	r.planRun("claude", []string{"--model", "opus"})

	out := buf.String()
	for _, want := range []string{
		"would reuse running container proj-abc123",
		"would re-inject auth secrets",
		"would launch: claude --model opus",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("plan output missing %q; got %q", want, out)
		}
	}
}
```

- [ ] **Step 2: Run the tests — expect compile failure**

```bash
go test ./cmd -run 'TestPlan|TestSuccessSuppressed|TestExtractBunkerFlags_DryRun|TestPlanRun' 2>&1 | head -20
```

Expected: the `cmd` test binary fails to build with `undefined` errors, e.g.
`undefined: plan`, `undefined: planf`, `undefined: dryRun`, `f.dryRun undefined (type bunkerFlags has no field or method dryRun)`,
`r.planRun undefined (type *runner has no field or method planRun)`. (`FAIL github.com/Devon-White/claude-bunker/cmd [build failed]`.)

- [ ] **Step 3: Implement the shared helper in `cmd/ui.go`**

Add the package var + helpers immediately after the `success` function:

```go
// dryRun is set by --dry-run. When true, mutating commands describe their plan
// via plan()/planf() and perform no host or Docker mutations, and success() is
// suppressed so "planned" is never mistaken for "done".
var dryRun bool

// plan prints a planned (not performed) action to stderr with a distinct
// DRY-RUN label. Unlike info/warn/success it ALWAYS prints — ignoring --quiet —
// because the plan is the entire point of the invocation.
func plan(msg string) {
	fmt.Fprintln(errW, prefixStyle.Render("[claude-bunker]"), warnLabelStyle.Render("DRY-RUN"), msg)
}

// planf is plan with printf-style formatting.
func planf(format string, a ...any) {
	plan(fmt.Sprintf(format, a...))
}
```

Change `success` so it never fires under `dryRun` (add the guard as the first line):

```go
func success(msg string) {
	if dryRun {
		return
	}
	if verbosity >= 0 {
		fmt.Fprintln(errW, prefixStyle.Render("[claude-bunker]"), successMsgStyle.Render(msg))
	}
}
```

- [ ] **Step 4: Implement the run-path flag + planning pass in `cmd/run.go` (+ shell/root wiring)**

**4a — `bunkerFlags` gets a `dryRun` field.** In the `bunkerFlags` struct, add the field next to `rebuild`:

```go
	keep      bool
	rebuild   bool
	dryRun    bool
	force     bool
```

**4b — `extractBunkerFlags` learns `--dry-run`.** Add the entry to the `boolFlags` map:

```go
	boolFlags := map[string]*bool{
		"--verbose":    &f.verbose,
		"--quiet":      &f.quiet,
		"--keep":       &f.keep,
		"--rebuild":    &f.rebuild,
		"--dry-run":    &f.dryRun,
		"--force":      &f.force,
		"--no-sandbox": &f.noSandbox,
		"--no-color":   &f.noColor,
	}
```

**4c — `runner` gets a `dryRun` field.** Add it to the struct near `force`:

```go
	reused    bool
	noCache   bool
	dryRun    bool                // --dry-run: plan mutations, perform none, exit 0
	force     bool
```

**4d — `runInSandbox`: set `dryRun`, plan the `--rebuild` teardown, and short-circuit after
`resolveContainer`.** Replace the block that spans from the `r := &runner{…}` literal through
`r.resolveContainer()` and the `if r.containerID == "" {` check with:

```go
	// dry-run may arrive via extractBunkerFlags (default run) or be pre-set on the
	// package var by the shell subcommand's cobra flag. Funnel both into dryRun.
	if flags.dryRun {
		dryRun = true
	}

	r := &runner{
		ctx:       ctx,
		cancel:    cancel,
		cli:       cli,
		workspace: resolveWorkspace(),
		dryRun:    dryRun,
		force:     flags.force,
		noSandbox: flags.noSandbox,
	}
	activeRunner = r

	r.loadConfig(flags)
	r.resolveNaming()

	// Handle --rebuild: force a clean slate
	if flags.rebuild {
		r.noCache = true
		if r.dryRun {
			plan("would clear fingerprint, remove image " + r.imageTag +
				", and stop+remove any existing container (--rebuild)")
		} else {
			info("Rebuild requested — clearing cache and removing existing image...")
			_ = config.ClearFingerprint(r.containerName)
			_ = container.RemoveImageByTag(r.ctx, r.cli, r.imageTag)
			if id, err := container.FindByLabel(r.ctx, r.cli, r.containerName); err == nil && id != "" {
				_ = container.Stop(r.ctx, r.cli, id)
				_ = container.Remove(r.ctx, r.cli, id)
			}
		}
	}

	r.resolveContainer()

	// Planning pass: resolveContainer has computed the reuse/recreate/create
	// decision (and, under dryRun, suppressed its own Stop/Remove side effects).
	// Report the ordered plan and exit before any build/create/seed/exec.
	if r.dryRun {
		r.planRun(execCmd, flags.remaining)
		cli.Close()
		os.Exit(0)
	}

	if r.containerID == "" {
		r.buildAndCreate()
	}
```

(Everything from `r.registerCleanup(!flags.keep)` onward is unchanged.)

**4e — `resolveContainer`: make its mutations conditional on `!r.dryRun`.** Two guards.

In the running-container *recreate* branch, insert an early return **before** the
`if r.fpResult.ImageMatch { info("Container configuration changed …") }` / `Stop` / `Remove`
sequence:

```go
		// Under dry-run, don't stop/remove; the planning pass reports "would create+start".
		if r.dryRun {
			return
		}
		if r.fpResult.ImageMatch {
			info("Container configuration changed — recreating sandbox...")
		} else {
			info("Image configuration changed — rebuilding sandbox...")
		}
		if err := container.Stop(r.ctx, r.cli, id); err != nil {
			verbose("Stop existing container: " + err.Error())
		}
		if err := container.Remove(r.ctx, r.cli, id); err != nil {
			verbose("Remove existing container: " + err.Error())
		}
```

In the `else` branch (stale stopped container), guard the `Remove`:

```go
	} else {
		if id, err := container.FindByLabel(r.ctx, r.cli, r.containerName); err == nil && id != "" {
			if r.dryRun {
				return
			}
			if err := container.Remove(r.ctx, r.cli, id); err != nil {
				verbose("Remove stopped container: " + err.Error())
			}
		}
	}
```

(The two *reuse* branches only read state — `ContainerRunning`, `HasAnyActiveSessions` — and set
`r.reused = true`, so they are already safe under dry-run.)

**4f — add `planRun`.** Add this method after `resolveContainer` (before `buildAndCreate`):

```go
// planRun prints the ordered plan for a dry-run of the run/shell path. It mirrors
// the real control flow: a reused container skips build/seed and only re-injects
// auth; a fresh/recreated container is built, created, firewalled, and seeded.
func (r *runner) planRun(execCmd string, args []string) {
	if r.reused {
		planf("would reuse running container %s (image %s)", r.containerName, r.imageTag)
		if r.auth.HasSecrets() {
			plan("would re-inject auth secrets into the reused container")
		}
	} else {
		if !r.fpResult.ImageMatch || !container.ImageExists(r.ctx, r.cli, r.imageTag) {
			planf("would build image %s", r.imageTag)
		} else {
			planf("would reuse image %s", r.imageTag)
		}
		planf("would create and start container %s", r.containerName)
		planf("would configure firewall (%d extra allowed domain(s))", len(r.extraDomains))
		plan("would seed managed-settings.json and copy .claude/ into the container")
		if r.auth.HasSecrets() {
			plan("would inject auth secrets")
		}
	}
	if execCmd == "claude" {
		if len(args) > 0 {
			planf("would launch: claude %s", strings.Join(args, " "))
		} else {
			plan("would launch: claude")
		}
	} else {
		planf("would launch: %s", execCmd)
	}
}
```

**4g — `cmd/shell.go`: read the cobra flag into the package var.** Replace the `RunE`:

```go
	RunE: func(cmd *cobra.Command, args []string) error {
		initVerbosity(cmd)
		if v, _ := cmd.Flags().GetBool("dry-run"); v {
			dryRun = true
		}
		return runInSandbox(args, "bash")
	},
```

**4h — `cmd/root.go`: register the shell + init cobra flags.** In `init()`, next to the existing
`initCmd.Flags().Bool("defaults", …)` line, add:

```go
	shellCmd.Flags().Bool("dry-run", false, "Show what would be built/created without launching")
	initCmd.Flags().Bool("dry-run", false, "Show what would be written without creating any files")
```

- [ ] **Step 5: Run the foundation tests + build**

```bash
go test ./cmd -run 'TestPlan|TestSuccessSuppressed|TestExtractBunkerFlags|TestPlanRun|TestDiagnosticsGoToStderr' 2>&1 | tail -5
go build -o /tmp/cb-task6 . 2>&1 | tail -5
```

Expected: `ok  github.com/Devon-White/claude-bunker/cmd` and a clean build (no output from `go build`).
Sanity-check the wiring reaches the subcommands without a daemon:

```bash
/tmp/cb-task6 shell --help 2>&1 | grep -- --dry-run
```

Expected: a line containing `--dry-run   Show what would be built/created without launching`.

- [ ] **Step 6: Write the failing `prune` dry-run test**

Create `cmd/prune_test.go`:

```go
package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"

	dockerclient "github.com/docker/docker/client"
)

// pruneResources under dryRun must plan every candidate and remove nothing,
// without prompting. A fake spec records remove() calls; nil cli is never used
// because list/remove ignore it and the dry-run branch returns before removal.
func TestPruneResources_DryRunPlansWithoutRemoving(t *testing.T) {
	var buf bytes.Buffer
	origErr := errW
	errW = &buf
	t.Cleanup(func() { errW = origErr })

	origV := verbosity
	verbosity = 0
	t.Cleanup(func() { verbosity = origV })

	origDry := dryRun
	dryRun = true
	t.Cleanup(func() { dryRun = origDry })

	var removeCalls []string
	spec := pruneSpec[string]{
		resourceName: "image",
		list: func(_ context.Context, _ *dockerclient.Client) ([]string, error) {
			return []string{"img-a", "img-b"}, nil
		},
		groups: func(items []string) ([]string, [][]string) {
			labels := make([]string, len(items))
			grouped := make([][]string, len(items))
			for i, it := range items {
				labels[i] = it
				grouped[i] = []string{it}
			}
			return labels, grouped
		},
		label: func(s string) string { return s },
		remove: func(_ context.Context, _ *dockerclient.Client, s string) error {
			removeCalls = append(removeCalls, s)
			return nil
		},
	}

	if err := pruneResources(context.Background(), nil, false, false, spec); err != nil {
		t.Fatalf("pruneResources: %v", err)
	}
	if len(removeCalls) != 0 {
		t.Fatalf("dry-run must remove nothing; removed %v", removeCalls)
	}
	out := buf.String()
	for _, want := range []string{"would remove image img-a", "would remove image img-b"} {
		if !strings.Contains(out, want) {
			t.Errorf("plan output missing %q; got %q", want, out)
		}
	}
}
```

Run it:

```bash
go test ./cmd -run TestPruneResources_DryRun 2>&1 | tail -8
```

Expected FAIL: with 2 groups, `all=false`, and a non-TTY test process, the current
`pruneResources` takes the `else if !isTTY()` path — it `warn`s and returns before planning, so
the assertions fail with `plan output missing "would remove image img-a"`.

- [ ] **Step 7: Implement `prune` dry-run in `cmd/prune.go`**

**7a — register the flag.** In `init()`:

```go
func init() {
	pruneCmd.Flags().Bool("force", false, "Skip confirmation prompt")
	pruneCmd.Flags().Bool("all", false, "Remove all volumes/images without interactive selection")
	pruneCmd.Flags().Bool("dry-run", false, "Show what would be removed without removing anything")
}
```

**7b — set the package var in `runPrune`.** Add after `initVerbosity(cmd)`:

```go
func runPrune(cmd *cobra.Command, args []string) error {
	initVerbosity(cmd)
	dryRun, _ = cmd.Flags().GetBool("dry-run")
	ctx := context.Background()
	// … unchanged …
```

**7c — plan branch in `pruneResources`.** Two edits. First, make dry-run imply "select all"
so the plan covers every candidate non-interactively — change the selection condition:

```go
	if all || dryRun || len(groupLabels) == 1 {
		for i := range groupLabels {
			selectedIndices = append(selectedIndices, i)
		}
	} else if !isTTY() {
```

Second, insert the dry-run plan branch **after** the `if len(toRemove) == 0 { … }` guard and
**before** the `if !force {` confirmation block:

```go
	if len(toRemove) == 0 {
		info("Nothing to remove.")
		return nil
	}

	if dryRun {
		for _, item := range toRemove {
			planf("would remove %s %s", spec.resourceName, spec.label(item))
		}
		return nil
	}

	if !force {
		ok, err := confirmAction(fmt.Sprintf("Remove %d %s(s)?", len(toRemove), spec.resourceName))
```

- [ ] **Step 8: Verify `prune` dry-run passes**

```bash
go test ./cmd -run TestPruneResources_DryRun 2>&1 | tail -3
```

Expected: `ok  github.com/Devon-White/claude-bunker/cmd`.

- [ ] **Step 9: Write the failing `init` dry-run test**

Append to `cmd/init_test.go` (add `"strings"` to its import block — currently `bytes`, `errors`,
`os`, `path/filepath`, `testing`, `huh`, `config`, `devcontainer`):

```go
func TestWriteDevContainer_DryRunPlansWithoutWriting(t *testing.T) {
	ws := t.TempDir()

	var buf bytes.Buffer
	origErr := errW
	errW = &buf
	t.Cleanup(func() { errW = origErr })

	origDry := dryRun
	dryRun = true
	t.Cleanup(func() { dryRun = origDry })

	cfg := config.ProjectConfig{
		Features: map[string]map[string]interface{}{
			"ghcr.io/devcontainers/features/node:1": {"version": "20"},
		},
	}
	if err := writeDevContainer(ws, cfg); err != nil {
		t.Fatalf("writeDevContainer: %v", err)
	}

	p := devcontainer.DevContainerPath(ws)
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("dry-run must not create %s (stat err = %v)", p, err)
	}
	if _, err := os.Stat(filepath.Dir(p)); !os.IsNotExist(err) {
		t.Errorf("dry-run must not create the .devcontainer directory")
	}
	if !strings.Contains(buf.String(), "would write "+p) {
		t.Errorf("plan output missing %q; got %q", "would write "+p, buf.String())
	}
}
```

Run it:

```bash
go test ./cmd -run TestWriteDevContainer_DryRun 2>&1 | tail -8
```

Expected FAIL: current `writeDevContainer` calls `os.MkdirAll`+`os.WriteFile` unconditionally, so
`os.Stat(p)` succeeds and the test fails with `dry-run must not create …`.

- [ ] **Step 10: Implement `init` dry-run in `cmd/init.go`**

**10a — set the package var + guard the top-level dir creation in `runInit`.** After
`initVerbosity(cmd)`:

```go
func runInit(cmd *cobra.Command, args []string) error {
	initVerbosity(cmd)
	dryRun, _ = cmd.Flags().GetBool("dry-run")

	workspace := resolveWorkspace()
	// … unchanged through cfgPath / existing-config load …

	// Create directory structure (skipped under dry-run — no host mutation).
	dir := filepath.Dir(cfgPath)
	if !dryRun {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			die("Failed to create config directory: " + err.Error())
		}
	}
	// … unchanged …
```

**10b — plan branch in `writeDevContainer`.** Insert immediately after `p := devcontainer.DevContainerPath(workspace)`
and before the `os.MkdirAll`:

```go
	p := devcontainer.DevContainerPath(workspace)
	if dryRun {
		planf("would write %s", p)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		return err
	}
	success("Saved " + p)
	return nil
```

(The `initCmd` cobra flag was registered in Step 4h.)

Run it:

```bash
go test ./cmd -run 'TestWriteDevContainer' 2>&1 | tail -3
```

Expected: `ok` — both `TestWriteDevContainer` (non-dry, still writes) and the new dry-run test pass.

- [ ] **Step 11: Write the failing `sessions stop` dry-run test**

Create `cmd/sessions_stop_test.go`:

```go
package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"

	"github.com/Devon-White/claude-bunker/internal/sessions"
)

// noMutateDocker satisfies sessions.DockerClient via the embedded (nil) interface,
// overriding only the mutating calls to fail the test if they are ever reached.
type noMutateDocker struct {
	sessions.DockerClient
	t *testing.T
}

func (d noMutateDocker) ContainerStop(context.Context, string, container.StopOptions) error {
	d.t.Fatal("dry-run must not call ContainerStop")
	return nil
}

func (d noMutateDocker) ContainerRemove(context.Context, string, container.RemoveOptions) error {
	d.t.Fatal("dry-run must not call ContainerRemove")
	return nil
}

func TestStopOrPlan_DryRunPlansWithoutDockerCalls(t *testing.T) {
	var buf bytes.Buffer
	origErr := errW
	errW = &buf
	t.Cleanup(func() { errW = origErr })

	origDry := dryRun
	dryRun = true
	t.Cleanup(func() { dryRun = origDry })

	mgr := sessions.NewManager(noMutateDocker{t: t})
	c := sessions.ContainerState{ID: "abc123", DisplayName: "proj-a", Status: "running"}

	if err := stopOrPlan(context.Background(), mgr, c, false /*force*/, true /*remove*/); err != nil {
		t.Fatalf("stopOrPlan: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"would stop container proj-a", "would remove container proj-a"} {
		if !strings.Contains(out, want) {
			t.Errorf("plan output missing %q; got %q", want, out)
		}
	}
}
```

Run it:

```bash
go test ./cmd -run TestStopOrPlan_DryRun 2>&1 | tail -8
```

Expected FAIL: build error `undefined: stopOrPlan` (the helper does not exist yet).

- [ ] **Step 12: Implement `sessions stop` dry-run in `cmd/sessions_stop.go`**

**12a — register the flag.** In `init()`:

```go
func init() {
	sessionsStopCmd.Flags().BoolP("force", "f", false, "Skip confirmation prompt")
	sessionsStopCmd.Flags().Bool("remove", false, "Also remove the container after stopping")
	sessionsStopCmd.Flags().Bool("dry-run", false, "Show what would be stopped/removed without doing it")
}
```

**12b — read the flag + delegate in `runSessionsStop`.** Replace the body from
`initVerbosity(cmd)` through the end of the function with:

```go
func runSessionsStop(cmd *cobra.Command, args []string) error {
	initVerbosity(cmd)
	dryRun, _ = cmd.Flags().GetBool("dry-run")
	ctx := context.Background()

	cli, err := dockerClient()
	if err != nil {
		return err
	}
	defer cli.Close()

	mgr := sessions.NewManager(cli)
	c, err := mgr.ResolveContainer(ctx, args[0])
	if err != nil {
		return err
	}

	if c.Status != "running" {
		info(fmt.Sprintf("Container %s is already %s.", c.DisplayName, c.Status))
		return nil
	}

	// Warn about active sessions.
	if len(c.Sessions) > 0 {
		warn(fmt.Sprintf("Container %s has %d active session(s).", c.DisplayName, len(c.Sessions)))
	}

	force, _ := cmd.Flags().GetBool("force")
	remove, _ := cmd.Flags().GetBool("remove")
	return stopOrPlan(ctx, mgr, c, force, remove)
}

// stopOrPlan stops (and optionally removes) a resolved container. Under dryRun it
// prints the plan and performs no Docker calls or confirmation prompt. The non-dry
// path is byte-identical to the previous inline implementation.
func stopOrPlan(ctx context.Context, mgr *sessions.Manager, c sessions.ContainerState, force, remove bool) error {
	if dryRun {
		planf("would stop container %s", c.DisplayName)
		if remove {
			planf("would remove container %s", c.DisplayName)
		}
		return nil
	}

	if !force {
		ok, err := confirmAction(fmt.Sprintf("Stop container %s?", c.DisplayName))
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
	}

	if err := mgr.StopContainer(ctx, c.ID); err != nil {
		return fmt.Errorf("stopping container: %w", err)
	}
	success(fmt.Sprintf("Stopped %s", c.DisplayName))

	if remove {
		if err := mgr.RemoveContainer(ctx, c.ID); err != nil {
			return fmt.Errorf("removing container: %w", err)
		}
		success(fmt.Sprintf("Removed %s", c.DisplayName))
	}
	return nil
}
```

Run it:

```bash
go test ./cmd -run TestStopOrPlan_DryRun 2>&1 | tail -3
```

Expected: `ok` — the mock's `ContainerStop`/`ContainerRemove` are never reached (no `t.Fatal`),
proving zero mutating Docker calls, and both plan lines are present.

- [ ] **Step 13: Full build + test suite**

```bash
go build -o /tmp/cb-task6 . 2>&1 | tail -5
go test ./... 2>&1 | tail -25
go vet ./cmd 2>&1 | tail -5
```

Expected: clean build; all `cmd` tests `ok`; `go vet` silent. (Per MEMORY note, two
`internal/sessions` socket-path tests may FAIL pre-existing on macOS — unrelated to this task.)

Additional smoke check that no diagnostics leak to stdout (stream discipline):

```bash
/tmp/cb-task6 prune --dry-run --all >/tmp/cb-stdout 2>/tmp/cb-stderr; echo "exit=$?"; echo "stdout-bytes=$(wc -c </tmp/cb-stdout)"; grep -c 'DRY-RUN' /tmp/cb-stderr
```

Expected: exit `0` (or `4` if no Docker daemon — acceptable, Docker-unavailable path);
`stdout-bytes=0` (all plan output is on stderr); `DRY-RUN` lines present on stderr when volumes/images exist.

- [ ] **Step 14: Commit**

```bash
git add cmd/ui.go cmd/run.go cmd/shell.go cmd/root.go cmd/prune.go cmd/init.go cmd/sessions_stop.go \
        cmd/ui_test.go cmd/run_test.go cmd/prune_test.go cmd/init_test.go cmd/sessions_stop_test.go
git commit -m "feat(cli): add --dry-run to run/shell/prune/init/sessions-stop

Plan mutating operations without performing them. A shared plan()/planf()
helper prints a DRY-RUN label to stderr regardless of --quiet; success() is
suppressed. The run path implements a planning pass after resolveContainer
(whose Stop/Remove side effects are gated on !dryRun) and exits 0 before any
build/create/seed/exec. Prune skips confirm and plans each candidate; init
plans the devcontainer write without touching the filesystem; sessions stop
plans stop/remove without Docker calls. Read-only and interactive commands
are intentionally excluded."
```

---
