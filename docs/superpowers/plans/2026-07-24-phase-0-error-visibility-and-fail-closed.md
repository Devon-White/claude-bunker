# Phase 0 — Error Visibility & Fail-Closed Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make every claude-bunker failure visible with a correct exit code, make the security-critical config/seed/teardown paths fail closed with explicit override flags, stop `init` from destroying an existing config, and stop the test suite from wiping the developer's real `~/.claude`.

**Architecture:** Small, surgical changes to the existing Cobra CLI and Docker wrapper. A new `cmd/errors.go` introduces a coded-error type + a top-level error printer wired into `main.go`. A single `failClosed` helper unifies the config and seed fail-closed decisions. `HasOtherActiveSessions` gains an `error` return so teardown can refuse when it can't tell. `init` no longer writes on abort/non-TTY. The session stores gain a `CLAUDE_BUNKER_STORE_DIR` seam so tests never touch the real home.

**Tech Stack:** Go 1.26, Cobra, charmbracelet/huh + lipgloss, docker/docker (moby) client. Table-driven tests with `t.Run` subtests.

## Global Constraints

- Go 1.26+; single static binary — do NOT add new heavy runtime dependencies.
- Root command has `DisableFlagParsing: true`; claude-bunker flags are parsed manually by `extractBunkerFlags`. Unknown flags pass through to `claude`.
- Fatal errors use `die(msg)` (prints styled `ERROR:` to stderr, cleans up the active runner, exits). `warn()`/`info()`/`verbose()` respect `verbosity` (-1 quiet / 0 normal / 1 verbose).
- Informational output goes through `info()/verbose()/success()`; errors/warnings go to stderr.
- Container constants: user `claude-bunker`, home `/home/claude-bunker`, workspace `/workspace`.
- This is Phase 0 of a larger redesign (see `docs/superpowers/specs/2026-07-24-claude-bunker-polish-design.md`). Do NOT pull in Phase 1+ work (session rewrite, devcontainer spec, full flag architecture, full exit-code catalog). Keep changes minimal and self-contained.
- Run `go build ./...` and `go test ./...` from the repo root. Commit after each task.

---

## File Structure

- `cmd/errors.go` — **new.** `CodedError`, exit-code constants, `ExitCodeFor`, `PrintError`. One responsibility: mapping errors to exit codes and rendering them.
- `cmd/errors_test.go` — **new.** Unit tests for the above.
- `main.go` — **modify.** Print `Execute()`'s error to stderr and exit with its code.
- `cmd/run.go` — **modify.** Add `force`/`noSandbox` to `bunkerFlags` + `runner`; error on missing credential values; fail-closed config load, seed, and teardown; introduce `failClosed`.
- `cmd/run_test.go` — **modify.** Update flag tests for `--force`/`--no-sandbox` and the missing-value-is-an-error behavior; add a `failClosed` test.
- `cmd/init.go` — **modify.** `--defaults` flag; do not write on abort or non-TTY-without-defaults; TTY seam.
- `cmd/init_test.go` — **new.** Tests for the non-interactive decision + abort mapping + file-untouched-on-abort.
- `cmd/root.go` — **modify.** Register the `--defaults` flag on `initCmd`.
- `internal/container/lifecycle.go` — **modify.** `HasOtherActiveSessions`/`HasAnyActiveSessions` return `(bool, error)`; add a tiny `execInspector` interface.
- `internal/container/lifecycle_test.go` — **new.** Table-driven tests for `HasOtherActiveSessions` with a fake inspector.
- `internal/sessions/store.go` — **modify.** Lazy path resolution honoring `CLAUDE_BUNKER_STORE_DIR`.
- `internal/sessions/store_test.go` — **new.** Test the env seam.
- `internal/sessions/main_test.go` — **new.** `TestMain` that isolates all sessions tests to a temp store dir.

---

## Task 1: Coded errors + visible top-level error printing

**Files:**
- Create: `cmd/errors.go`
- Create: `cmd/errors_test.go`
- Modify: `main.go:9-13`

**Interfaces:**
- Produces: `type CodedError struct { Code int; Err error }`; `func Coded(code int, err error) error`; `func ExitCodeFor(err error) int`; `func PrintError(w io.Writer, err error)`; constants `ExitOK=0, ExitError=1, ExitCancelled=2, ExitDockerUnavailable=4`.
- Consumes (in `PrintError`): the existing `prefixStyle` and `errorLabelStyle` from `cmd/ui.go`.

- [ ] **Step 1: Write the failing test**

Create `cmd/errors_test.go`:

```go
package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
)

func TestExitCodeFor(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"nil is zero", nil, 0},
		{"plain error is one", errors.New("boom"), 1},
		{"coded error keeps code", Coded(ExitDockerUnavailable, errors.New("no docker")), 4},
		{"wrapped coded error keeps code", fmt.Errorf("startup: %w", Coded(4, errors.New("x"))), 4},
		{"coded nil is passthrough zero", Coded(4, nil), 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExitCodeFor(tt.err); got != tt.want {
				t.Errorf("ExitCodeFor(%v) = %d, want %d", tt.err, got, tt.want)
			}
		})
	}
}

func TestPrintError(t *testing.T) {
	var b bytes.Buffer
	PrintError(&b, errors.New("something broke"))
	out := b.String()
	if b.Len() == 0 {
		t.Fatal("PrintError wrote nothing")
	}
	if !bytes.Contains([]byte(out), []byte("something broke")) {
		t.Errorf("PrintError output %q missing the message", out)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/ -run 'TestExitCodeFor|TestPrintError' -v`
Expected: FAIL — `undefined: Coded`, `undefined: ExitCodeFor`, `undefined: PrintError`, `undefined: ExitDockerUnavailable`.

- [ ] **Step 3: Write minimal implementation**

Create `cmd/errors.go`:

```go
package cmd

import (
	"errors"
	"fmt"
	"io"
)

// Process exit codes. Phase 0 defines the ones it needs; later phases extend
// this into the full exit-code catalog.
const (
	ExitOK                = 0
	ExitError             = 1
	ExitCancelled         = 2
	ExitDockerUnavailable = 4
)

// CodedError carries a process exit code alongside an error.
type CodedError struct {
	Code int
	Err  error
}

func (e *CodedError) Error() string { return e.Err.Error() }
func (e *CodedError) Unwrap() error { return e.Err }

// Coded wraps err with a specific process exit code. Returns nil if err is nil.
func Coded(code int, err error) error {
	if err == nil {
		return nil
	}
	return &CodedError{Code: code, Err: err}
}

// ExitCodeFor returns the process exit code for an error: a CodedError's Code
// (found anywhere in the wrap chain), 1 for any other non-nil error, 0 for nil.
func ExitCodeFor(err error) int {
	if err == nil {
		return ExitOK
	}
	var ce *CodedError
	if errors.As(err, &ce) {
		return ce.Code
	}
	return ExitError
}

// PrintError renders a styled error line to w. Used by main() for errors that
// bubble up from Execute() (which are otherwise silenced by SilenceErrors).
func PrintError(w io.Writer, err error) {
	fmt.Fprintln(w,
		prefixStyle.Render("[claude-bunker]"),
		errorLabelStyle.Render("ERROR:"),
		err.Error(),
	)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/ -run 'TestExitCodeFor|TestPrintError' -v`
Expected: PASS.

- [ ] **Step 5: Wire main.go to print the error**

Replace `main.go` lines 9-13 (the `main` function body):

```go
func main() {
	if err := cmd.Execute(); err != nil {
		cmd.PrintError(os.Stderr, err)
		os.Exit(cmd.ExitCodeFor(err))
	}
}
```

(The `os` and `cmd` imports are already present.)

- [ ] **Step 6: Verify the build and the end-to-end behavior manually**

Run: `go build -o /tmp/cb . && /tmp/cb sessions bogus; echo "exit=$?"`
Expected: a styled `[claude-bunker] ERROR: unknown command "bogus" ...` line on stderr and `exit=1` (previously this printed nothing and exited 1). `SilenceErrors` on root stays `true`, so the message is printed exactly once (by `main`, not cobra).

- [ ] **Step 7: Commit**

```bash
git add cmd/errors.go cmd/errors_test.go main.go
git commit -m "fix(cli): surface Execute() errors on stderr with exit codes

Adds CodedError/ExitCodeFor/PrintError. main() now prints the error that
cobra's SilenceErrors suppressed, instead of discarding it and exiting 1
with no output."
```

---

## Task 2: Register `--force`/`--no-sandbox`; error on missing credential values

**Files:**
- Modify: `cmd/run.go:112-179` (`bunkerFlags`, `extractBunkerFlags`) and `cmd/run.go:188-217` (`runInSandbox` wiring)
- Modify: `cmd/run_test.go`

**Interfaces:**
- Produces: `bunkerFlags` gains `force bool`, `noSandbox bool`, and `err error`. `--force` and `--no-sandbox` parse as boolean flags. A value flag (`--gh-token`/`--api-key`/`--oauth-token`) with no value or an empty value sets `f.err`.
- Consumes: nothing new.

- [ ] **Step 1: Write the failing test**

Three edits to `TestExtractBunkerFlags` in `cmd/run_test.go`.

**(a)** Add a `wantErr bool` field to the test-table struct header (currently lines 11-15):

```go
	tests := []struct {
		name    string
		args    []string
		want    bunkerFlags
		wantErr bool
	}{
```

**(b)** Replace the four existing cases that asserted a silent `bunkerFlags{}` — `"flag at end with no value does not panic"` (lines 102-106), `"api-key at end with no value does not panic"` (137-140), `"oauth-token at end with no value does not panic"` (141-145), and `"equals form with empty value"` (156-159) — and add two new boolean-flag cases. The full set to insert:

```go
		{
			name: "force flag",
			args: []string{"--force"},
			want: bunkerFlags{force: true},
		},
		{
			name: "no-sandbox flag",
			args: []string{"--no-sandbox"},
			want: bunkerFlags{noSandbox: true},
		},
		{
			name:    "gh-token at end with no value is an error",
			args:    []string{"--gh-token"},
			wantErr: true,
		},
		{
			name:    "api-key at end with no value is an error",
			args:    []string{"--api-key"},
			wantErr: true,
		},
		{
			name:    "oauth-token at end with no value is an error",
			args:    []string{"--oauth-token"},
			wantErr: true,
		},
		{
			name:    "equals form with empty value is an error",
			args:    []string{"--gh-token="},
			wantErr: true,
		},
```

**(c)** Add these assertions to the subtest body in the `for` loop, right after the existing `remaining` check (after line 196):

```go
			if (got.err != nil) != tt.wantErr {
				t.Errorf("err = %v, wantErr = %v", got.err, tt.wantErr)
			}
			if got.force != tt.want.force {
				t.Errorf("force = %v, want %v", got.force, tt.want.force)
			}
			if got.noSandbox != tt.want.noSandbox {
				t.Errorf("noSandbox = %v, want %v", got.noSandbox, tt.want.noSandbox)
			}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/ -run TestExtractBunkerFlags -v`
Expected: FAIL — `got.err`, `got.force`, `got.noSandbox` undefined (fields don't exist yet), and the empty-value cases still return no error.

- [ ] **Step 3: Write minimal implementation**

In `cmd/run.go`, extend the `bunkerFlags` struct (currently lines 114-121):

```go
type bunkerFlags struct {
	auth       container.AuthTokens
	quiet      bool
	verbose    bool
	keep       bool
	rebuild    bool
	force      bool
	noSandbox  bool
	remaining  []string
	err        error
}
```

In `extractBunkerFlags`, add the two boolean flags to `boolFlags` (currently lines 134-139):

```go
	boolFlags := map[string]*bool{
		"--verbose":    &f.verbose,
		"--quiet":      &f.quiet,
		"--keep":       &f.keep,
		"--rebuild":    &f.rebuild,
		"--force":      &f.force,
		"--no-sandbox": &f.noSandbox,
	}
```

Replace the value-flag handling block (currently lines 152-172) so a missing or empty value sets `f.err`:

```go
		// Check for --flag value and --flag=value forms
		handled := false
		for flag, dest := range flagMap {
			if arg == flag {
				if i+1 < len(args) && args[i+1] != "" {
					*dest = args[i+1]
					i += 2
				} else {
					f.err = fmt.Errorf("flag %s needs a non-empty value", flag)
					i++
				}
				handled = true
				break
			}
			if strings.HasPrefix(arg, flag+"=") {
				val := arg[len(flag)+1:]
				if val == "" {
					f.err = fmt.Errorf("flag %s needs a non-empty value", flag)
				} else {
					*dest = val
				}
				i++
				handled = true
				break
			}
		}
```

Wire it into `runInSandbox`: right after `flags := extractBunkerFlags(passedArgs)` (line 189) add:

```go
	if flags.err != nil {
		die(flags.err.Error())
	}
```

And when constructing the `runner` (lines 211-217), add the two fields:

```go
	r := &runner{
		ctx:       ctx,
		cancel:    cancel,
		cli:       cli,
		workspace: resolveWorkspace(),
		force:     flags.force,
		noSandbox: flags.noSandbox,
	}
```

Add the fields to the `runner` struct (after `noCache bool`, around line 56):

```go
	force     bool // --force: override fail-closed guards
	noSandbox bool // --no-sandbox: launch even if sandbox settings can't be seeded
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/ -run TestExtractBunkerFlags -v`
Expected: PASS (all subtests, including the new error and boolean cases).

- [ ] **Step 5: Verify the build**

Run: `go build ./...`
Expected: no errors. (`fmt` is already imported in `run.go`.)

- [ ] **Step 6: Commit**

```bash
git add cmd/run.go cmd/run_test.go
git commit -m "feat(cli): add --force/--no-sandbox and error on missing credential values

extractBunkerFlags now records an error when a credential flag has no value
(previously silently launched without auth), and parses --force/--no-sandbox
for the fail-closed overrides."
```

---

## Task 3: `failClosed` helper + fail-closed config load

**Files:**
- Modify: `cmd/run.go` (add `failClosed`; change `loadConfig`, lines 266-287)
- Modify: `cmd/run_test.go` (add `TestFailClosed`)

**Interfaces:**
- Produces: `func failClosed(err error, overridden bool, remediation string) error` — returns a wrapped fatal error when `err != nil && !overridden`; returns nil otherwise (caller warns if `err != nil`).
- Consumes: `config.LoadProjectConfig` (unchanged), `bunkerFlags.force`.

- [ ] **Step 1: Write the failing test**

Add to `cmd/run_test.go`:

```go
func TestFailClosed(t *testing.T) {
	realErr := errors.New("bad config")

	if got := failClosed(nil, false, "fix it"); got != nil {
		t.Errorf("nil error should stay nil, got %v", got)
	}
	if got := failClosed(realErr, true, "fix it"); got != nil {
		t.Errorf("overridden error should be nil, got %v", got)
	}
	got := failClosed(realErr, false, "fix it or use --force")
	if got == nil {
		t.Fatal("non-overridden error should be fatal, got nil")
	}
	if !errors.Is(got, realErr) {
		t.Errorf("fatal error should wrap the cause; got %v", got)
	}
	if !strings.Contains(got.Error(), "fix it or use --force") {
		t.Errorf("fatal error should include remediation; got %q", got.Error())
	}
}
```

Add `"errors"` and `"strings"` to `cmd/run_test.go` imports (the file currently imports `"slices"` and `"testing"` and the container package).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/ -run TestFailClosed -v`
Expected: FAIL — `undefined: failClosed`.

- [ ] **Step 3: Write minimal implementation**

Add to `cmd/run.go` (near the other helpers, e.g. just above `loadConfig`):

```go
// failClosed returns a fatal error when err is non-nil and not overridden by a
// flag. When overridden, it returns nil and the caller should warn and continue.
// This centralizes the security-critical "refuse to run without the protection
// unless the user explicitly opted out" decision.
func failClosed(err error, overridden bool, remediation string) error {
	if err == nil || overridden {
		return nil
	}
	return fmt.Errorf("%w\n%s", err, remediation)
}
```

Change `loadConfig` (lines 267-272) from warn-and-continue to fail-closed:

```go
func (r *runner) loadConfig(flags bunkerFlags) {
	cfg, err := config.LoadProjectConfig(r.workspace)
	if fatal := failClosed(err, flags.force, "Fix the config, or re-run with --force to ignore it."); fatal != nil {
		die("Failed to parse config: " + fatal.Error())
	}
	if err != nil {
		warn("Continuing despite config error (--force): " + err.Error())
	}
	r.projectCfg = cfg
```

(The rest of `loadConfig` — auth resolution and proxy detection — is unchanged. On the `--force` path, `cfg` is the zero-value config returned by `LoadProjectConfig` on error.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/ -run TestFailClosed -v`
Expected: PASS.

- [ ] **Step 5: Verify the build and the whole cmd package**

Run: `go build ./... && go test ./cmd/ -v`
Expected: build clean; all `cmd` tests pass.

- [ ] **Step 6: Commit**

```bash
git add cmd/run.go cmd/run_test.go
git commit -m "fix(security): fail closed on malformed config

A parse error in config.json now aborts with a remediation hint instead of
silently running with a zero-value config (which dropped exclude overlays and
allowDomains). --force restores the old warn-and-continue behavior."
```

---

## Task 4: Fail-closed sandbox seed

**Files:**
- Modify: `cmd/run.go` (`seedSettings`, lines 446-463)

**Interfaces:**
- Consumes: `failClosed` (Task 3), `runner.noSandbox` (Task 2), `sandbox.SeedSettings` (unchanged signature).

- [ ] **Step 1: Write the failing test**

Add a seed-shaped case to `TestFailClosed` in `cmd/run_test.go` to lock in the semantics used here (the helper is shared, so this documents the seed decision):

```go
func TestFailClosed_Seed(t *testing.T) {
	seedErr := errors.New("cannot write managed-settings.json")

	// Without --no-sandbox, a seed failure is fatal.
	if failClosed(seedErr, false, "re-run with --no-sandbox") == nil {
		t.Error("seed failure without --no-sandbox should be fatal")
	}
	// With --no-sandbox, it is tolerated.
	if failClosed(seedErr, true, "re-run with --no-sandbox") != nil {
		t.Error("seed failure with --no-sandbox should be tolerated")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/ -run TestFailClosed_Seed -v`
Expected: PASS — this task depends on Task 3, which already added `failClosed`, so the helper compiles and the test passes. (This test documents the seed-failure decision; the behavior change lands in Step 3 where `seedSettings` starts calling the helper.)

- [ ] **Step 3: Write the implementation**

Change `seedSettings` (lines 446-463) so a seed failure is fatal unless `--no-sandbox`:

```go
func (r *runner) seedSettings() {
	log := r.logWriter()
	opts := sandbox.SeedOpts{
		ContainerID:  r.containerID,
		Workspace:    r.workspace,
		ExtraDomains: r.extraDomains,
		PluginLevel:  r.projectCfg.PluginLevel(),
		LogW:         log,
	}
	err := sandbox.SeedSettings(r.ctx, r.cli, opts)
	if fatal := failClosed(err, r.noSandbox, "The sandbox cannot be enforced. Re-run with --no-sandbox to launch without it (NOT recommended)."); fatal != nil {
		die("Failed to seed sandbox settings: " + fatal.Error())
	}
	if err != nil {
		warn("Launching without enforced sandbox settings (--no-sandbox): " + err.Error())
	}
	if r.projectCfg.ShouldSeedHistory() {
		if err := sandbox.SeedSessionHistory(r.ctx, r.cli, r.containerID, r.workspace, log); err != nil {
			warn("Failed to seed session history: " + err.Error())
		}
	}
}
```

(Session-history seeding stays a warning — it is not a security layer. Only the managed-settings seed is fail-closed.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/ -run TestFailClosed -v && go build ./...`
Expected: PASS; build clean.

- [ ] **Step 5: Commit**

```bash
git add cmd/run.go cmd/run_test.go
git commit -m "fix(security): fail closed when sandbox settings can't be seeded

A SeedSettings failure previously warned and ran the container with no
managed-settings.json (no bubblewrap enforcement, no domain allowlist). It now
aborts unless --no-sandbox is given."
```

---

## Task 5: Fail-closed `HasOtherActiveSessions`

**Files:**
- Modify: `internal/container/lifecycle.go:576-604` (both functions + a new interface)
- Create: `internal/container/lifecycle_test.go`
- Modify: `cmd/run.go:87` (cleanup caller) and `cmd/run.go:330` (resolveContainer caller)

**Interfaces:**
- Produces: `type execInspector interface { ContainerInspect(context.Context, string) (container.InspectResponse, error); ContainerExecInspect(context.Context, string) (container.ExecInspect, error) }`. `func HasOtherActiveSessions(ctx, cli execInspector, containerID, myExecID string) (bool, error)`. `func HasAnyActiveSessions(ctx, cli execInspector, containerID string) (bool, error)`.
- Consumes: `runner.force` (Task 2) at the cleanup call site.

- [ ] **Step 1: Write the failing test**

Create `internal/container/lifecycle_test.go`:

```go
package container

import (
	"context"
	"errors"
	"testing"

	"github.com/docker/docker/api/types/container"
)

type fakeInspector struct {
	inspect     container.InspectResponse
	inspectErr  error
	execRunning map[string]bool
	execErr     error
}

func (f *fakeInspector) ContainerInspect(_ context.Context, _ string) (container.InspectResponse, error) {
	return f.inspect, f.inspectErr
}

func (f *fakeInspector) ContainerExecInspect(_ context.Context, execID string) (container.ExecInspect, error) {
	if f.execErr != nil {
		return container.ExecInspect{}, f.execErr
	}
	return container.ExecInspect{ExecID: execID, Running: f.execRunning[execID]}, nil
}

func inspectWithExecs(ids ...string) container.InspectResponse {
	return container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{ExecIDs: ids},
	}
}

func TestHasOtherActiveSessions(t *testing.T) {
	ctx := context.Background()

	t.Run("inspect error returns error", func(t *testing.T) {
		_, err := HasOtherActiveSessions(ctx, &fakeInspector{inspectErr: errors.New("daemon down")}, "cid", "myexec")
		if err == nil {
			t.Fatal("expected error when inspect fails")
		}
	})

	t.Run("no other execs is false", func(t *testing.T) {
		f := &fakeInspector{inspect: inspectWithExecs("myexec")}
		active, err := HasOtherActiveSessions(ctx, f, "cid", "myexec")
		if err != nil || active {
			t.Fatalf("want (false,nil), got (%v,%v)", active, err)
		}
	})

	t.Run("another running exec is true", func(t *testing.T) {
		f := &fakeInspector{
			inspect:     inspectWithExecs("myexec", "other"),
			execRunning: map[string]bool{"other": true},
		}
		active, err := HasOtherActiveSessions(ctx, f, "cid", "myexec")
		if err != nil || !active {
			t.Fatalf("want (true,nil), got (%v,%v)", active, err)
		}
	})

	t.Run("exec inspect error returns error", func(t *testing.T) {
		f := &fakeInspector{inspect: inspectWithExecs("other"), execErr: errors.New("gone")}
		_, err := HasOtherActiveSessions(ctx, f, "cid", "myexec")
		if err == nil {
			t.Fatal("expected error when exec inspect fails")
		}
	})
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/container/ -run TestHasOtherActiveSessions -v`
Expected: FAIL — `HasOtherActiveSessions` currently returns a single `bool` (signature mismatch: "too many return values" / assignment mismatch).

- [ ] **Step 3: Write the implementation**

In `internal/container/lifecycle.go`, replace the two functions (lines 579-604). First confirm `github.com/docker/docker/api/types/container` is imported in this file (it is used elsewhere in the package; add the import if this file lacks it). Then:

```go
// execInspector is the minimal Docker surface HasOtherActiveSessions needs.
// *client.Client satisfies it; tests use a fake.
type execInspector interface {
	ContainerInspect(ctx context.Context, containerID string) (container.InspectResponse, error)
	ContainerExecInspect(ctx context.Context, execID string) (container.ExecInspect, error)
}

// HasOtherActiveSessions reports whether the container has a running exec
// session other than myExecID. It returns an error if the daemon can't be
// queried — callers must treat that as "cannot determine" and fail closed
// (do not tear the container down), because a false "no sessions" would let one
// exiting session SIGKILL a container hosting other live sessions.
func HasOtherActiveSessions(ctx context.Context, cli execInspector, containerID, myExecID string) (bool, error) {
	inspect, err := cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return false, fmt.Errorf("inspecting container: %w", err)
	}
	for _, eid := range inspect.ExecIDs {
		if eid == myExecID {
			continue
		}
		execInfo, err := cli.ContainerExecInspect(ctx, eid)
		if err != nil {
			return false, fmt.Errorf("inspecting exec %s: %w", eid, err)
		}
		if execInfo.Running {
			return true, nil
		}
	}
	return false, nil
}

// HasAnyActiveSessions reports whether the container has any running exec session.
func HasAnyActiveSessions(ctx context.Context, cli execInspector, containerID string) (bool, error) {
	return HasOtherActiveSessions(ctx, cli, containerID, "")
}
```

Ensure `fmt` is imported in `lifecycle.go` (it almost certainly is; add it if not).

- [ ] **Step 4: Update the cleanup caller (`cmd/run.go:83-90`)**

Replace the `HasOtherActiveSessions` block in `cleanup`:

```go
	// Don't tear down the container if other sessions are still attached.
	// Use a fresh context — r.ctx may already be cancelled by signal handlers.
	checkCtx, checkCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer checkCancel()
	active, err := container.HasOtherActiveSessions(checkCtx, r.cli, cID, execID)
	if err != nil {
		// Fail closed: if we can't tell, assume other sessions may be active and
		// leave the container running — unless the user forced teardown.
		if !r.force {
			verbose("Could not determine active sessions; leaving container running: " + err.Error())
			return
		}
		verbose("Could not determine active sessions; forcing teardown (--force): " + err.Error())
	} else if active {
		verbose("Other sessions still active — leaving container running")
		return
	}
```

- [ ] **Step 5: Update the resolveContainer caller (`cmd/run.go:330`)**

Replace the `HasAnyActiveSessions` condition so an error is treated as "active" (fail closed — don't destroy a container we can't inspect):

```go
		// Config changed, but don't kill active sessions — reuse the container
		// and let the changes take effect on the next clean start.
		active, aerr := container.HasAnyActiveSessions(r.ctx, r.cli, id)
		if active || aerr != nil {
```

(The body inside that `if` is unchanged.)

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/container/ -run TestHasOtherActiveSessions -v && go build ./...`
Expected: PASS; build clean (both `cmd/run.go` call sites updated).

- [ ] **Step 7: Commit**

```bash
git add internal/container/lifecycle.go internal/container/lifecycle_test.go cmd/run.go
git commit -m "fix(security): fail closed when active-session check can't reach the daemon

HasOtherActiveSessions/HasAnyActiveSessions now return an error instead of
false on inspect failure. Teardown leaves the container running when it can't
tell (unless --force); recreate treats an inspect error as 'active'. Prevents a
slow daemon from causing rm -f of a container another session is using."
```

---

## Task 6: `init` no longer destroys config on abort or non-TTY

**Files:**
- Modify: `cmd/init.go` (TTY seam, `--defaults`, abort/non-TTY handling)
- Modify: `cmd/root.go:43-46` (register `--defaults` on `initCmd`)
- Create: `cmd/init_test.go`

**Interfaces:**
- Produces: `var stdinIsTTY = isTTY` (overridable seam); `func nonInteractiveInit(defaults bool) (write bool, err error)`; `func abortErr(err error) error`.
- Consumes: `Coded`/`ExitCancelled`/`ExitError` (Task 1), `writeConfig` (unchanged), `huh.ErrUserAborted`.

- [ ] **Step 1: Write the failing test**

Create `cmd/init_test.go`:

```go
package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/huh"

	"github.com/Devon-White/claude-bunker/internal/config"
)

func TestNonInteractiveInit(t *testing.T) {
	// Without --defaults, init must refuse (no write) and return an error.
	write, err := nonInteractiveInit(false)
	if err == nil {
		t.Error("expected an error when not a TTY and --defaults not given")
	}
	if write {
		t.Error("must not write a config when refusing")
	}
	// With --defaults, init writes.
	write, err = nonInteractiveInit(true)
	if err != nil {
		t.Errorf("--defaults should not error, got %v", err)
	}
	if !write {
		t.Error("--defaults should write a config")
	}
}

func TestAbortErr(t *testing.T) {
	if got := abortErr(huh.ErrUserAborted); ExitCodeFor(got) != ExitCancelled {
		t.Errorf("user abort should map to ExitCancelled, got code %d", ExitCodeFor(got))
	}
	other := errors.New("form failed")
	if got := abortErr(other); !errors.Is(got, other) {
		t.Errorf("non-abort error should pass through, got %v", got)
	}
}

func TestRunInit_NonTTYWithoutDefaultsLeavesFileUntouched(t *testing.T) {
	ws := t.TempDir()
	t.Setenv("CLAUDE_BUNKER_WS", ws)

	cfgPath := config.ConfigPath(ws)
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"apt":["ripgrep"]}` + "\n")
	if err := os.WriteFile(cfgPath, original, 0o644); err != nil {
		t.Fatal(err)
	}

	// Force the non-interactive path.
	orig := stdinIsTTY
	stdinIsTTY = func() bool { return false }
	t.Cleanup(func() { stdinIsTTY = orig })

	err := runInit(initCmd, nil)
	if err == nil {
		t.Fatal("expected runInit to error on non-TTY without --defaults")
	}

	got, readErr := os.ReadFile(cfgPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != string(original) {
		t.Errorf("config was modified: got %q, want %q", got, original)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/ -run 'TestNonInteractiveInit|TestAbortErr|TestRunInit_NonTTY' -v`
Expected: FAIL — `undefined: nonInteractiveInit`, `undefined: abortErr`, `undefined: stdinIsTTY`, and `runInit` currently writes `{}` and returns nil.

- [ ] **Step 3: Add the TTY seam and helpers to `cmd/init.go`**

Add near the top of `cmd/init.go` (after the imports):

```go
// stdinIsTTY is a seam over isTTY so tests can force the non-interactive path.
var stdinIsTTY = isTTY

// nonInteractiveInit decides what init does when stdin is not a terminal.
// Returns write=true when a default config should be written; otherwise an
// error explaining how to proceed. It never silently overwrites.
func nonInteractiveInit(defaults bool) (write bool, err error) {
	if defaults {
		return true, nil
	}
	return false, Coded(ExitError, errors.New(
		"init needs an interactive terminal; re-run with --defaults to write a default config non-interactively"))
}

// abortErr maps a huh form abort (Esc/Ctrl+C) to a cancellation exit code and
// passes other errors through. Callers return this WITHOUT writing a config.
func abortErr(err error) error {
	if errors.Is(err, huh.ErrUserAborted) {
		return Coded(ExitCancelled, errors.New("init cancelled"))
	}
	return err
}
```

Add `"errors"` to the `cmd/init.go` imports.

- [ ] **Step 4: Rewrite the non-TTY and abort branches in `runInit`**

Replace the non-interactive fallback (lines 53-56) with:

```go
	// Non-interactive: never silently overwrite. Require --defaults to write.
	if !stdinIsTTY() {
		defaults, _ := cmd.Flags().GetBool("defaults")
		write, err := nonInteractiveInit(defaults)
		if err != nil {
			return err
		}
		if write {
			return writeConfig(cfgPath, nil)
		}
		return nil
	}
```

Replace EACH of the four `return writeConfig(cfgPath, nil)` abort branches (lines 68, 93, 105 — the ones reached when a form returns an `err`) with `return abortErr(err)`. Concretely:

- Line ~66-69 (`selectLanguages`): `if err != nil { return abortErr(err) }`
- Line ~91-94 (`selectSettings`): `if err != nil { return abortErr(err) }`
- Line ~103-106 (`selectVersion`): `if err != nil { return abortErr(err) }`

(There are exactly three form-error `return writeConfig(cfgPath, nil)` sites plus the one non-TTY site handled above. After this change, `writeConfig(cfgPath, nil)` is called only on the deliberate `--defaults` path and — via the normal flow — on successful completion at line 118.)

- [ ] **Step 5: Register the `--defaults` flag on `initCmd` (`cmd/root.go`)**

In `cmd/root.go` `init()`, after the loop that adds verbose/quiet flags (line 46), add:

```go
	initCmd.Flags().Bool("defaults", false, "Write a default config non-interactively (no prompts)")
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./cmd/ -run 'TestNonInteractiveInit|TestAbortErr|TestRunInit_NonTTY' -v && go build ./...`
Expected: PASS; build clean.

- [ ] **Step 7: Commit**

```bash
git add cmd/init.go cmd/root.go cmd/init_test.go
git commit -m "fix(init): never overwrite config on abort or non-TTY

Aborting the wizard (Esc/Ctrl+C) or running init without a TTY previously wrote
'{}' over an existing config.json. Abort now returns a cancellation error and
leaves the file untouched; non-TTY requires an explicit --defaults."
```

---

## Task 7: Test suite must not touch the real `~/.claude`

**Files:**
- Modify: `internal/sessions/store.go` (lazy path via `CLAUDE_BUNKER_STORE_DIR`)
- Create: `internal/sessions/store_test.go`
- Create: `internal/sessions/main_test.go` (`TestMain`)

**Interfaces:**
- Produces: `func storeDir() string` (honors `CLAUDE_BUNKER_STORE_DIR`, else `~/.claude`). `jsonMapStore` resolves its file path lazily.
- Consumes: nothing new.

- [ ] **Step 1: Write the failing test**

Create `internal/sessions/store_test.go`:

```go
package sessions

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoreHonorsStoreDirEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_BUNKER_STORE_DIR", dir)

	s := newJSONMapStore("probe.json")
	if err := s.Set("k", "v"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// The file must be written under the temp dir, NOT under the real ~/.claude.
	want := filepath.Join(dir, "probe.json")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("expected store file at %s: %v", want, err)
	}
	if got := s.Get("k"); got != "v" {
		t.Errorf("Get = %q, want v", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/sessions/ -run TestStoreHonorsStoreDirEnv -v`
Expected: FAIL — the current store resolves `~/.claude` at construction time (before the env var is read), so the file is not written under the temp dir.

- [ ] **Step 3: Make the store path lazy and env-overridable**

Replace `internal/sessions/store.go` lines 13-63 (the struct through `persist`):

```go
type jsonMapStore struct {
	mu       sync.Mutex
	filename string
	cache    map[string]string
	loaded   bool
}

// storeDir returns the base directory for bunker's JSON stores. It honors
// CLAUDE_BUNKER_STORE_DIR (used for test isolation and custom setups) and
// otherwise falls back to ~/.claude. Returns "" if no directory can be found,
// in which case the store operates in-memory only.
func storeDir() string {
	if d := os.Getenv("CLAUDE_BUNKER_STORE_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude")
}

// newJSONMapStore creates a store backed by <storeDir>/<filename>, resolved
// lazily so the directory can be overridden at runtime (e.g. in tests).
func newJSONMapStore(filename string) *jsonMapStore {
	return &jsonMapStore{filename: filename}
}

// path resolves the on-disk path, or "" for in-memory-only operation.
func (s *jsonMapStore) path() string {
	dir := storeDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, s.filename)
}

// ensureLoaded reads the JSON file into cache on the first call. Must be called with mu held.
func (s *jsonMapStore) ensureLoaded() {
	if s.loaded {
		return
	}
	s.loaded = true
	s.cache = map[string]string{}
	p := s.path()
	if p == "" {
		return
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return
	}
	// Ignore unmarshal errors — treat corrupt files as empty.
	_ = json.Unmarshal(data, &s.cache)
}

// persist writes the cache to disk. Must be called with mu held.
func (s *jsonMapStore) persist() error {
	p := s.path()
	if p == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.cache, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o644)
}
```

(`Get`/`Set`/`All`/`Prune` are unchanged — they already call `ensureLoaded`/`persist`.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/sessions/ -run TestStoreHonorsStoreDirEnv -v`
Expected: PASS.

- [ ] **Step 5: Isolate the whole sessions package with `TestMain`**

Create `internal/sessions/main_test.go`:

```go
package sessions

import (
	"os"
	"testing"
)

// TestMain redirects the JSON stores to a throwaway directory for the entire
// package test run, so tests can never wipe the developer's real
// ~/.claude/session-names.json (FetchSnapshot calls PruneStaleNames, which with
// fake container IDs would otherwise delete every real custom name).
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "bunker-sessions-test-*")
	if err != nil {
		panic(err)
	}
	os.Setenv("CLAUDE_BUNKER_STORE_DIR", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
```

- [ ] **Step 6: Run the full sessions package and confirm the real store is untouched**

Run:
```bash
# Snapshot the real store (if any) before running tests
cp -n ~/.claude/session-names.json /tmp/names.before 2>/dev/null || true
go test ./internal/sessions/ -v
# Confirm it was not modified by the test run
diff <(cat ~/.claude/session-names.json 2>/dev/null) /tmp/names.before 2>/dev/null && echo "REAL STORE UNTOUCHED" || echo "check: real store had no prior file (also fine)"
```
Expected: all sessions tests PASS; the real `~/.claude/session-names.json` is unchanged (or never existed).

- [ ] **Step 7: Commit**

```bash
git add internal/sessions/store.go internal/sessions/store_test.go internal/sessions/main_test.go
git commit -m "fix(test): isolate session stores from the real ~/.claude

Store paths now resolve lazily via CLAUDE_BUNKER_STORE_DIR, and a package
TestMain points the sessions tests at a temp dir. Previously FetchSnapshot
tests ran PruneStaleNames against the real store and deleted every custom
container name."
```

---

## Final verification

- [ ] **Run the full suite and build**

Run: `go build ./... && go test ./...`
Expected: build clean; all packages pass. In particular `go test ./internal/sessions/` does not modify `~/.claude`.

- [ ] **Manual smoke of the headline fix**

Run: `go build -o /tmp/cb . && /tmp/cb sessions bogus; echo "exit=$?"`
Expected: a visible `[claude-bunker] ERROR: ...` line on stderr and a non-zero exit — not silent.

---

## Self-review notes (coverage against spec §11 Phase 0 + §8.1)

- Error visibility → Task 1 (main.go prints `Execute()` errors; exit-code seam).
- Fail-closed posture with `--force`/`--no-sandbox` registered → Task 2 (flags) + Task 3 (config, `--force`) + Task 4 (seed, `--no-sandbox`) + Task 5 (teardown inspect error, `--force`). Matches the §8.1 override table.
- `init` data-loss fix → Task 6.
- Test HOME isolation → Task 7.

Deliberately deferred (later phases, per the spec): the full exit-code catalog (codes 2/4 wired everywhere), the full root flag architecture / `--` passthrough, stream discipline, `--json`, `doctor`, and the session/attach/teardown *guard* rework (Phase 1). Phase 0 only makes the inspect-error path fail closed.
