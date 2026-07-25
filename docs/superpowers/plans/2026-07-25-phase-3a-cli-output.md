# Phase 3a — CLI Output & Interactivity Correctness

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix three real CLI-hygiene bugs (clig.dev conventions): (1) status chatter (`info`/`verbose`/`success`) prints to **stdout**, so `claude-bunker -p "q" > out.txt` mixes `[claude-bunker] Building…` into the payload; (2) ANSI color is always emitted, even into pipes/files and when `NO_COLOR` is set; (3) a confirmation prompt in a non-interactive context (`sessions stop`/`prune` without `--force`) silently returns "no" and exits 0, so scripts think it succeeded when it did nothing.

**Architecture:** All changes live in `cmd/` (mostly `cmd/ui.go`). Introduce testable output-writer seams; route all diagnostic output to stderr; add a pure color-decision honoring `NO_COLOR`/`CLICOLOR`/`--no-color`/TTY and wire it into lipgloss; make `confirmAction` return an error on non-TTY so callers exit non-zero with a `--force` hint.

**Tech Stack:** Go 1.26, Cobra, charmbracelet/lipgloss (+ its `termenv`), golang.org/x/term. Table-driven tests with `t.Run`.

## Global Constraints

- Go 1.26+; single static binary; no new deps (lipgloss already pulls in `termenv`).
- **Stream discipline:** ALL human-facing diagnostic output (`info`/`verbose`/`success`/`warn`/`die`) goes to **stderr**. Machine/payload output (`version`, any `--json`) stays on **stdout**. This keeps stdout clean for piping.
- **Color:** disabled when any of: `NO_COLOR` set (to any value, per no-color.org), `CLICOLOR=0`, `--no-color` flag, or the diagnostic stream (stderr) is not a TTY. `CLICOLOR_FORCE` (non-empty, non-"0") forces color on even for a non-TTY.
- **Non-interactive confirm:** `confirmAction` must NOT silently return false when there's no TTY. It returns an error telling the user to pass `--force` (both `prune` and `sessions stop` already have `--force`). Callers propagate the error (non-zero exit).
- Preserve the existing `verbosity` gating (`-1` quiet suppresses info/verbose/success; warn/die always print).
- Run `go build ./...` and `go test ./...`; both stay green. Commit after each task.

---

## File Structure

- `cmd/ui.go` — **modify.** Output-writer seams (`outW`/`errW`); route info/verbose/success/warn/die to `errW`; `isStderrTTY`; `shouldUseColor` + `applyColorProfile`; `confirmAction` returns `(bool, error)`.
- `cmd/ui_test.go` — **new.** Stream-discipline, color-decision, and confirm-decision tests.
- `cmd/root.go` — **modify.** Apply the color profile early in `Execute`; register `--no-color`.
- `cmd/run.go` — **modify.** `extractBunkerFlags` parses `--no-color`.
- `cmd/sessions_stop.go`, `cmd/prune.go` — **modify.** Handle `confirmAction`'s new error return.

---

## Task 1: Output-writer seams + stream discipline (diagnostics → stderr)

**Files:**
- Modify: `cmd/ui.go`
- Create: `cmd/ui_test.go`

**Interfaces:**
- Produces: package vars `var outW io.Writer = os.Stdout` and `var errW io.Writer = os.Stderr` (overridable in tests). `info`/`verbose`/`success`/`warn`/`die` write to `errW`. (Payload commands keep writing to `os.Stdout` directly.)

- [ ] **Step 1: Write the failing test**

Create `cmd/ui_test.go`:

```go
package cmd

import (
	"bytes"
	"testing"
)

func TestDiagnosticsGoToStderr(t *testing.T) {
	var out, errb bytes.Buffer
	origOut, origErr := outW, errW
	outW, errW = &out, &errb
	t.Cleanup(func() { outW, errW = origOut, origErr })

	origVerbosity := verbosity
	verbosity = 1
	t.Cleanup(func() { verbosity = origVerbosity })

	info("building")
	verbose("detail")
	success("done")
	warn("careful")

	if out.Len() != 0 {
		t.Errorf("diagnostics must NOT go to stdout; got %q", out.String())
	}
	for _, want := range []string{"building", "detail", "done", "careful"} {
		if !bytes.Contains(errb.Bytes(), []byte(want)) {
			t.Errorf("stderr missing %q; got %q", want, errb.String())
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/ -run TestDiagnosticsGoToStderr -v`
Expected: FAIL — `outW`/`errW` undefined, and `info`/`verbose`/`success` currently use `fmt.Println` (stdout), so the stdout buffer would be non-empty once the seams exist.

- [ ] **Step 3: Add the seams and route diagnostics to stderr**

In `cmd/ui.go`, add the imports `"io"` (keep `"os"`, `"fmt"`) and the seams near the top:

```go
// Output streams. Seams so tests can capture output. Diagnostics go to errW
// (stderr) to keep stdout clean for piping; payload commands write to stdout.
var (
	outW io.Writer = os.Stdout
	errW io.Writer = os.Stderr
)
```

Rewrite the output functions to use `errW`:

```go
func info(msg string) {
	if verbosity >= 0 {
		fmt.Fprintln(errW, prefixStyle.Render("[claude-bunker]"), infoMsgStyle.Render(msg))
	}
}

func verbose(msg string) {
	if verbosity >= 1 {
		fmt.Fprintln(errW, prefixStyle.Render("[claude-bunker]"), verboseMsgStyle.Render(msg))
	}
}

func warn(msg string) {
	if verbosity >= 0 {
		fmt.Fprintln(errW,
			prefixStyle.Render("[claude-bunker]"),
			warnLabelStyle.Render("WARNING:"),
			msg,
		)
	}
}

func success(msg string) {
	if verbosity >= 0 {
		fmt.Fprintln(errW, prefixStyle.Render("[claude-bunker]"), successMsgStyle.Render(msg))
	}
}
```

In `die`, change `fmt.Fprintln(os.Stderr, ...)` to `fmt.Fprintln(errW, ...)` (keep the `os.Exit(1)` and cleanup).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/ -run TestDiagnosticsGoToStderr -v` then `go build ./...`
Expected: PASS; build clean.

- [ ] **Step 5: Commit**

```bash
git add cmd/ui.go cmd/ui_test.go
git commit -m "fix(cli): route all diagnostics to stderr so stdout stays clean for piping"
```

---

## Task 2: Color discipline (NO_COLOR / CLICOLOR / --no-color / TTY)

**Files:**
- Modify: `cmd/ui.go` (`shouldUseColor`, `applyColorProfile`, `isStderrTTY`)
- Modify: `cmd/root.go` (call `applyColorProfile` in `Execute`; register `--no-color` on subcommands)
- Modify: `cmd/run.go` (`extractBunkerFlags` parses `--no-color`)
- Modify: `cmd/ui_test.go`

**Interfaces:**
- Produces: `func shouldUseColor(noColorFlag bool, env func(string) (string, bool), stderrIsTTY bool) bool` (pure); `func applyColorProfile(noColorFlag bool)` (calls `shouldUseColor` with the real env/TTY and sets lipgloss's color profile); `func isStderrTTY() bool`.

- [ ] **Step 1: Write the failing test**

Add to `cmd/ui_test.go`:

```go
func TestShouldUseColor(t *testing.T) {
	env := func(m map[string]string) func(string) (string, bool) {
		return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
	}
	tests := []struct {
		name        string
		noColorFlag bool
		vars        map[string]string
		stderrTTY   bool
		want        bool
	}{
		{"tty no env → color", false, nil, true, true},
		{"not a tty → no color", false, nil, false, false},
		{"NO_COLOR set (even empty) → no color", false, map[string]string{"NO_COLOR": ""}, true, false},
		{"CLICOLOR=0 → no color", false, map[string]string{"CLICOLOR": "0"}, true, false},
		{"--no-color → no color", true, nil, true, false},
		{"CLICOLOR_FORCE overrides non-tty", false, map[string]string{"CLICOLOR_FORCE": "1"}, false, true},
		{"NO_COLOR beats CLICOLOR_FORCE", false, map[string]string{"NO_COLOR": "1", "CLICOLOR_FORCE": "1"}, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldUseColor(tt.noColorFlag, env(tt.vars), tt.stderrTTY); got != tt.want {
				t.Errorf("shouldUseColor = %v, want %v", got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/ -run TestShouldUseColor -v`
Expected: FAIL — `shouldUseColor` undefined.

- [ ] **Step 3: Implement the decision + wiring**

In `cmd/ui.go` add (imports: `"os"`, `"github.com/charmbracelet/lipgloss"`, `"github.com/muesli/termenv"` — termenv is already an indirect dep via lipgloss; and `"golang.org/x/term"`):

```go
// shouldUseColor decides whether to emit ANSI color, per the no-color.org and
// CLICOLOR conventions. Precedence: NO_COLOR (any value) disables; else
// CLICOLOR_FORCE (non-empty, != "0") forces on; else --no-color / CLICOLOR=0
// disable; else color follows whether the diagnostic stream is a TTY.
func shouldUseColor(noColorFlag bool, env func(string) (string, bool), stderrIsTTY bool) bool {
	if _, ok := env("NO_COLOR"); ok {
		return false
	}
	if v, ok := env("CLICOLOR_FORCE"); ok && v != "" && v != "0" {
		return true
	}
	if noColorFlag {
		return false
	}
	if v, ok := env("CLICOLOR"); ok && v == "0" {
		return false
	}
	return stderrIsTTY
}

// isStderrTTY reports whether stderr (where diagnostics go) is an interactive terminal.
func isStderrTTY() bool {
	return term.IsTerminal(int(os.Stderr.Fd()))
}

// applyColorProfile sets lipgloss's color profile from the real environment.
// Call once early in Execute, before any styled output.
func applyColorProfile(noColorFlag bool) {
	if !shouldUseColor(noColorFlag, os.LookupEnv, isStderrTTY()) {
		lipgloss.SetColorProfile(termenv.Ascii)
	}
}
```

In `cmd/root.go` `Execute`, near the top (before any command runs / output), parse a `--no-color` presence from `os.Args` and apply:

```go
	noColor := false
	for _, a := range os.Args[1:] {
		if a == "--no-color" {
			noColor = true
			break
		}
	}
	applyColorProfile(noColor)
```

Register `--no-color` on the subcommands loop in `init()` (alongside verbose/quiet) so it shows in `--help` and is accepted by subcommands:

```go
	cmd.Flags().Bool("no-color", false, "Disable ANSI color output")
```

In `cmd/run.go` `extractBunkerFlags`, add `--no-color` to the boolean flags map (so the root/default path accepts it without passing it through to claude):

```go
		"--no-color":   &f.noColor,
```

and add `noColor bool` to the `bunkerFlags` struct. (It doesn't need to do anything else there — `Execute` already applied the profile; this just prevents `--no-color` leaking through to `claude`.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/ -run 'TestShouldUseColor|TestDiagnosticsGoToStderr' -v` then `go build ./...` then `go test ./...`
Expected: PASS; build clean; full suite green.

- [ ] **Step 5: Commit**

```bash
git add cmd/ui.go cmd/root.go cmd/run.go cmd/ui_test.go
git commit -m "feat(cli): honor NO_COLOR/CLICOLOR/--no-color and disable color for non-TTY output"
```

---

## Task 3: Non-interactive confirm errors (no silent success)

**Files:**
- Modify: `cmd/ui.go` (`confirmAction` returns `(bool, error)`)
- Modify: `cmd/sessions_stop.go`, `cmd/prune.go` (handle the error)
- Modify: `cmd/ui_test.go`

**Interfaces:**
- Changes: `func confirmAction(title string) (bool, error)` — non-TTY → `(false, errNonInteractiveConfirm)`; TTY → prompt, `(confirmed, nil)`. Produces `var errNonInteractiveConfirm = errors.New("cannot prompt for confirmation in a non-interactive context; pass --force to skip the prompt")`.

- [ ] **Step 1: Write the failing test**

Add to `cmd/ui_test.go`:

```go
import "errors" // add to the import block

func TestConfirmAction_NonInteractiveErrors(t *testing.T) {
	orig := stdinIsTTY
	stdinIsTTY = func() bool { return false }
	t.Cleanup(func() { stdinIsTTY = orig })

	ok, err := confirmAction("Proceed?")
	if ok {
		t.Error("must not return true in a non-interactive context")
	}
	if !errors.Is(err, errNonInteractiveConfirm) {
		t.Errorf("must return errNonInteractiveConfirm, got %v", err)
	}
}
```

(`stdinIsTTY` is the seam introduced in Phase 0 for `init`; reuse it. If `confirmAction` currently calls `isTTY()` directly, switch it to `stdinIsTTY()` so the test can override it.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/ -run TestConfirmAction_NonInteractiveErrors -v`
Expected: FAIL — `confirmAction` returns a single `bool` (assignment mismatch) and `errNonInteractiveConfirm` is undefined.

- [ ] **Step 3: Change `confirmAction`**

In `cmd/ui.go` (add `"errors"` to imports):

```go
var errNonInteractiveConfirm = errors.New("cannot prompt for confirmation in a non-interactive context; pass --force to skip the prompt")

// confirmAction shows a yes/no prompt and returns the choice. In a non-interactive
// context it returns errNonInteractiveConfirm rather than a silent "no", so
// scripts get a non-zero exit instead of a false success.
func confirmAction(title string) (bool, error) {
	if !stdinIsTTY() {
		return false, errNonInteractiveConfirm
	}
	var confirmed bool
	err := huh.NewForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title(title).
				Affirmative("Yes").
				Negative("No").
				Value(&confirmed),
		),
	).Run()
	if err != nil {
		return false, nil // treat a form error/abort as "no"
	}
	return confirmed, nil
}
```

- [ ] **Step 4: Update the two callers**

`cmd/sessions_stop.go` (~line 54):
```go
	if !force {
		ok, err := confirmAction(fmt.Sprintf("Stop container %s?", c.DisplayName))
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
	}
```

`cmd/prune.go` (~line 130): the enclosing function must return the error. If it currently returns nothing, change its signature to `error` and thread the return up to the `RunE` (or, if it's already within a func returning error, just return it):
```go
	if !force {
		ok, err := confirmAction(fmt.Sprintf("Remove %d %s(s)?", len(toRemove), spec.resourceName))
		if err != nil {
			return err
		}
		if !ok {
			info("Aborted.")
			return nil
		}
	}
```
(Read `prune.go` to determine the enclosing function's signature; make the minimal change so the error reaches a `RunE` and produces a non-zero exit. If the function is currently `func(...)` with no return, give it an `error` return and update its single call site.)

- [ ] **Step 5: Run tests + build**

Run: `go test ./cmd/ -run 'TestConfirmAction|TestDiagnostics|TestShouldUseColor' -v` then `go build ./...` then `go test ./...`
Expected: PASS; build clean; full suite green.

- [ ] **Step 6: Commit**

```bash
git add cmd/ui.go cmd/sessions_stop.go cmd/prune.go cmd/ui_test.go
git commit -m "fix(cli): non-interactive confirm errors with a --force hint instead of silent no-op"
```

---

## Self-review notes (coverage vs spec §9)

- Stream discipline (status/info/verbose → stderr) → Task 1.
- TTY/color: NO_COLOR/CLICOLOR/--no-color + non-TTY auto-disable → Task 2.
- Non-interactive confirm fails with a hint instead of silent no-op → Task 3.

## Follow-on Phase 3 slices (not in this plan)

- Exit-code catalog (§9.2): `die`/RunE carry codes (docker-unavailable → 4, cancelled → 2); the `CodedError`/`ExitCodeFor` seam already exists from Phase 0.
- `--json` on `status`, `version`, `prune` (additive schema; matches existing `sessions list --json`).
- `doctor` command (docker reachable + version, firewall self-test), `--dry-run`, docker-ping timeout, first-run hint.
- Full root flag architecture: registered+documented root flags + `--` passthrough contract in help.
