# Phase 3b — Exit-code catalog, `--json` output, `doctor` command

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Three more clig.dev/gh-convention wins: (1) a real **exit-code catalog** — a Docker-unavailable failure exits `4` (not a generic `1`), so scripts can distinguish "Docker isn't running" from "the command failed"; (2) **`--json`** on `version` and `status` for orchestration/scripting (matching the existing `sessions list --json`); (3) a **`doctor`** command for first-run/troubleshooting that checks Docker reachability + versions + config presence and exits non-zero if the environment isn't ready.

**Architecture:** All in `cmd/`. Reuses the Phase-0 `CodedError`/`ExitCodeFor` seam (`cmd/errors.go`, with `ExitDockerUnavailable = 4`, `ExitCancelled = 2`) — `main.go` already exits with `ExitCodeFor(err)`. A `dockerClient()` cmd-layer helper wraps `container.NewClient` failures as `Coded(ExitDockerUnavailable, …)`; a `dieCode` variant carries a code on the `die` path. `status` is refactored to gather a struct then render text-or-JSON.

**Tech Stack:** Go 1.26, Cobra, docker/docker (moby). Table-driven tests with `t.Run`.

## Global Constraints

- Go 1.26+; single static binary; no new deps.
- **Exit-code catalog (spec §9.2):** `0` success (or forwarded claude code); `1` generic error; `2` user cancelled a bunker prompt (already: `init` abort → `Coded(ExitCancelled)`); `4` Docker unavailable/not running. The `run`/`shell` path still propagates claude's own exit code verbatim.
- Diagnostics go to **stderr** (Phase 3a); payload/`--json` goes to **stdout**. `--json` output must be the ONLY thing on stdout (no styled status lines) so it's pipeable to `jq`.
- `--json` schemas are additive-only and stable.
- `doctor` performs NO container build and NO network beyond a Docker daemon ping; it must be fast and safe to run anytime.
- Run `go build ./...` and `go test ./...`; both stay green. Commit after each task.

---

## File Structure

- `cmd/errors.go` — **modify.** Add `dieCode(code int, msg string)`; refactor `die` to call it. (die currently lives in `cmd/ui.go` — move or add `dieCode` beside it; keep the exit-code constants in errors.go.)
- `cmd/docker.go` — **new.** `dockerClient() (*client.Client, error)` wrapping `container.NewClient` failure as `Coded(ExitDockerUnavailable, …)`.
- `cmd/*.go` (8 RunE sites + run.go) — **modify.** Use `dockerClient()` / `dieCode(ExitDockerUnavailable, …)`.
- `cmd/root.go` — **modify.** `version --json`.
- `cmd/status.go` — **modify.** Gather-then-render; `--json`.
- `cmd/doctor.go` — **new.** `doctor` command.
- `cmd/*_test.go` — tests.

---

## Task 1: Exit-code catalog — Docker-unavailable → exit 4

**Files:**
- Modify: `cmd/ui.go` (`die` → `dieCode`)
- Create: `cmd/docker.go`, `cmd/docker_test.go`
- Modify: `cmd/run.go`, `cmd/status.go`, `cmd/prune.go`, `cmd/logs.go`, `cmd/sessions_list.go`, `cmd/sessions_stop.go`, `cmd/sessions_logs.go`, `cmd/sessions_attach.go`, `cmd/sessions_tui.go` (the 9 NewClient sites)

**Interfaces:**
- Produces: `func dieCode(code int, msg string)` (like `die` but `os.Exit(code)`); `die(msg)` becomes `dieCode(ExitError, msg)`. `func dockerClient() (*client.Client, error)` — returns `Coded(ExitDockerUnavailable, err)` on `container.NewClient` failure.

- [ ] **Step 1: Write the failing test**

Create `cmd/docker_test.go`:

```go
package cmd

import "testing"

func TestDockerClient_WrapsUnavailableAsCode4(t *testing.T) {
	// Force a Docker-client failure by pointing DOCKER_HOST at a dead socket.
	t.Setenv("DOCKER_HOST", "unix:///nonexistent/docker.sock")
	_, err := dockerClient()
	if err == nil {
		t.Skip("Docker appears reachable in this environment; cannot exercise the failure path")
	}
	if ExitCodeFor(err) != ExitDockerUnavailable {
		t.Errorf("docker-unavailable error must map to exit %d, got %d", ExitDockerUnavailable, ExitCodeFor(err))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/ -run TestDockerClient_WrapsUnavailableAsCode4 -v`
Expected: FAIL — `dockerClient` undefined.

- [ ] **Step 3: Add `dockerClient` and `dieCode`**

Create `cmd/docker.go`:

```go
package cmd

import (
	"github.com/docker/docker/client"

	"github.com/Devon-White/claude-bunker/internal/container"
)

// dockerClient creates the Docker API client, tagging a connection/daemon
// failure with the Docker-unavailable exit code so callers (via main's
// ExitCodeFor) exit 4 instead of a generic 1.
func dockerClient() (*client.Client, error) {
	cli, err := container.NewClient()
	if err != nil {
		return nil, Coded(ExitDockerUnavailable, err)
	}
	return cli, nil
}
```

In `cmd/ui.go`, refactor `die` to carry a code:

```go
// dieCode prints a styled fatal error to stderr, tears down the active runner,
// and exits with the given code.
func dieCode(code int, msg string) {
	fmt.Fprintln(errW,
		prefixStyle.Render("[claude-bunker]"),
		errorLabelStyle.Render("ERROR:"),
		msg,
	)
	if activeRunner != nil {
		activeRunner.cancel()
		activeRunner.cleanup()
		if activeRunner.cli != nil {
			activeRunner.cli.Close()
		}
	}
	os.Exit(code)
}

// die is dieCode with the generic error exit code.
func die(msg string) {
	dieCode(ExitError, msg)
}
```

- [ ] **Step 4: Wire the 9 NewClient sites**

- `cmd/run.go` (~line 234): `cli, err := container.NewClient(); if err != nil { die(err.Error()) }` → change to:
  ```go
  cli, err := dockerClient()
  if err != nil {
      dieCode(ExitDockerUnavailable, err.Error())
  }
  ```
- The 8 RunE sites (`status.go:29`, `prune.go:36`, `logs.go:32`, `sessions_list.go:30`, `sessions_stop.go:30`, `sessions_logs.go:34`, `sessions_attach.go:37`, `sessions_tui.go:32`): replace `cli, err := ctr.NewClient()` (or `container.NewClient()`) + the `return fmt.Errorf("docker client: %w", err)` block with:
  ```go
  cli, err := dockerClient()
  if err != nil {
      return err
  }
  ```
  (The `Coded(4)` error flows to `main.go` → `ExitCodeFor` → exit 4. Drop any now-unused `ctr`/`container` import ONLY if the file has no other use of it — most keep it.)

- [ ] **Step 5: Document the catalog**

In `cmd/errors.go`, expand the exit-code const block's doc comment to list all four codes and their meaning (0/1/2/4), and note the `run` path propagates claude's code.

- [ ] **Step 6: Run tests + build**

Run: `go test ./cmd/ -run 'TestDockerClient|TestExitCodeFor' -v` then `go build ./...` then `go test ./...`
Expected: PASS (the docker test may `Skip` if Docker is reachable — that's fine); build clean; full suite green.

- [ ] **Step 7: Commit**

```bash
git add cmd/docker.go cmd/docker_test.go cmd/ui.go cmd/errors.go cmd/run.go cmd/status.go cmd/prune.go cmd/logs.go cmd/sessions_list.go cmd/sessions_stop.go cmd/sessions_logs.go cmd/sessions_attach.go cmd/sessions_tui.go
git commit -m "feat(cli): Docker-unavailable exits 4 (exit-code catalog); dieCode carries codes"
```

---

## Task 2: `--json` on `version` and `status`

**Files:**
- Modify: `cmd/root.go` (`versionCmd` + the `Execute` version intercept)
- Modify: `cmd/status.go` (gather-then-render + `--json`)
- Create/modify: `cmd/status_test.go`, `cmd/root_test.go`

**Interfaces:**
- Produces: `type statusInfo struct {...}` with JSON tags; `func gatherStatus(ctx, cli, workspace) (statusInfo, error)`; `func renderStatusText(statusInfo)`. `version --json` → `{"version":"<v>"}`.

- [ ] **Step 1: Write the failing test**

Create `cmd/status_test.go`:

```go
package cmd

import (
	"encoding/json"
	"testing"
)

func TestStatusInfoJSON(t *testing.T) {
	s := statusInfo{
		Workspace: "/w", Container: "proj-abc", Image: "img:tag",
		State: "running", ID: "abcdef123456", Uptime: "5m 0s",
		Sessions: []string{"claude"},
	}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]interface{}
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"workspace", "container", "image", "state"} {
		if _, ok := back[k]; !ok {
			t.Errorf("status JSON missing key %q: %s", k, data)
		}
	}
	if back["state"] != "running" {
		t.Errorf("state = %v", back["state"])
	}
}
```

Add to `cmd/root_test.go` (create if absent):

```go
func TestRenderVersionJSON(t *testing.T) {
	out := renderVersionJSON("1.2.3")
	if out != `{"version":"1.2.3"}` {
		t.Errorf("renderVersionJSON = %q", out)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/ -run 'TestStatusInfoJSON|TestRenderVersionJSON' -v`
Expected: FAIL — `statusInfo`, `renderVersionJSON` undefined.

- [ ] **Step 3: `version --json`**

In `cmd/root.go`, add:

```go
func renderVersionJSON(version string) string {
	return `{"version":` + strconvQuote(version) + `}`
}
```

where `strconvQuote` is `strconv.Quote` (add `"strconv"` import) — use `strconv.Quote(version)` directly:

```go
func renderVersionJSON(version string) string {
	return `{"version":` + strconv.Quote(version) + `}`
}
```

Register `--json` on `versionCmd` and branch:

```go
var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version",
	Run: func(cmd *cobra.Command, args []string) {
		if j, _ := cmd.Flags().GetBool("json"); j {
			fmt.Println(renderVersionJSON(Version))
			return
		}
		fmt.Println(renderVersion(Version))
	},
}
```

In `init()`, `versionCmd.Flags().Bool("json", false, "Output as JSON")`. (The `Execute` early-intercept for bare `version` can stay text-only; `version --json` goes through cobra since a flag is present — verify: `os.Args[1] == "version"` intercept prints text and exits. To support `version --json`, adjust the intercept to only fast-path when there's no `--json` arg, else fall through to `rootCmd.Execute()`.)

- [ ] **Step 4: `status --json` (gather-then-render)**

In `cmd/status.go`, define the struct and refactor `runStatus` to gather then render:

```go
type statusInfo struct {
	Workspace  string   `json:"workspace"`
	Container  string   `json:"container"`
	Image      string   `json:"image"`
	State      string   `json:"state"`
	ID         string   `json:"id,omitempty"`
	Uptime     string   `json:"uptime,omitempty"`
	Sessions   []string `json:"sessions,omitempty"`
	ImageBuilt string   `json:"image_built,omitempty"`
}
```

Extract the existing gathering into `func gatherStatus(ctx context.Context, cli *client.Client, workspace string) (statusInfo, error)` (returns the populated struct; `State` = "not created" when no container), and move the current `fmt.Println(kvLine(...))` block into `func renderStatusText(s statusInfo)`. Then:

```go
func runStatus(cmd *cobra.Command, args []string) error {
	initVerbosity(cmd)
	ctx := context.Background()
	cli, err := dockerClient()
	if err != nil {
		return err
	}
	defer cli.Close()

	s, err := gatherStatus(ctx, cli, resolveWorkspace())
	if err != nil {
		return err
	}
	if j, _ := cmd.Flags().GetBool("json"); j {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(s)
	}
	renderStatusText(s)
	// the resolved-config section stays text-only (not in the struct for now)
	if cfg, _, cfgErr := devcontainer.LoadProjectConfig(resolveWorkspace()); cfgErr == nil {
		printResolvedConfig(cfg)
	}
	return nil
}
```

Register `--json` on `statusCmd` in `init()` (add an `init()` to status.go or the root loop): `statusCmd.Flags().Bool("json", false, "Output as JSON")`. Add `"encoding/json"`, `"os"`, and the docker `client` import to status.go as needed.

- [ ] **Step 5: Run tests + build**

Run: `go test ./cmd/ -run 'TestStatusInfoJSON|TestRenderVersionJSON' -v` then `go build ./...` then `go test ./...`
Expected: PASS; build clean; full suite green.

- [ ] **Step 6: Commit**

```bash
git add cmd/root.go cmd/status.go cmd/status_test.go cmd/root_test.go
git commit -m "feat(cli): --json output for version and status"
```

---

## Task 3: `doctor` command (environment preflight)

**Files:**
- Create: `cmd/doctor.go`, `cmd/doctor_test.go`
- Modify: `cmd/root.go` (register `doctorCmd`)

**Interfaces:**
- Produces: `doctorCmd`; `type checkResult struct { Name, Detail string; OK bool }`; `func runDoctorChecks(ctx, cli DockerPinger, version, workspace string) []checkResult` (pure over injected deps); a `DockerPinger` interface `{ Ping(ctx) (types.Ping, error); ServerVersion(ctx) (types.Version, error) }`. Exit non-zero (`Coded(ExitDockerUnavailable)`) when Docker isn't reachable.

- [ ] **Step 1: Write the failing test**

Create `cmd/doctor_test.go`:

```go
package cmd

import (
	"context"
	"errors"
	"testing"

	"github.com/docker/docker/api/types"
)

type fakePinger struct {
	pingErr error
	ver     string
}

func (f fakePinger) Ping(context.Context) (types.Ping, error) {
	if f.pingErr != nil {
		return types.Ping{}, f.pingErr
	}
	return types.Ping{APIVersion: "1.45"}, nil
}
func (f fakePinger) ServerVersion(context.Context) (types.Version, error) {
	return types.Version{Version: f.ver}, nil
}

func TestRunDoctorChecks(t *testing.T) {
	t.Run("docker reachable → docker check OK", func(t *testing.T) {
		results := runDoctorChecks(context.Background(), fakePinger{ver: "27.0"}, "1.0.0", t.TempDir())
		var dockerOK bool
		for _, r := range results {
			if r.Name == "Docker" {
				dockerOK = r.OK
			}
		}
		if !dockerOK {
			t.Error("Docker check should be OK when ping succeeds")
		}
	})
	t.Run("docker down → docker check fails", func(t *testing.T) {
		results := runDoctorChecks(context.Background(), fakePinger{pingErr: errors.New("no daemon")}, "1.0.0", t.TempDir())
		for _, r := range results {
			if r.Name == "Docker" && r.OK {
				t.Error("Docker check should fail when ping errors")
			}
		}
	})
}

func TestDoctorAllOK(t *testing.T) {
	ok := doctorAllOK([]checkResult{{Name: "a", OK: true}, {Name: "b", OK: true}})
	if !ok {
		t.Error("all-OK should be true")
	}
	if doctorAllOK([]checkResult{{OK: true}, {OK: false}}) {
		t.Error("one failing check → not OK")
	}
}
```

(Confirm the exact moby type for `ServerVersion`'s return — it may be `types.Version` or `system.Info`; adjust the fake + interface to the real signatures by reading `go doc github.com/docker/docker/client.Client.ServerVersion`. `Ping` returns `types.Ping`.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/ -run 'TestRunDoctorChecks|TestDoctorAllOK' -v`
Expected: FAIL — undefined `runDoctorChecks`, `checkResult`, `doctorAllOK`.

- [ ] **Step 3: Implement `doctor`**

Create `cmd/doctor.go`:

```go
package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/docker/docker/api/types"
	"github.com/spf13/cobra"

	"github.com/Devon-White/claude-bunker/internal/devcontainer"
)

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check that the environment is ready to run claude-bunker",
	Long:  "Verifies Docker is reachable, reports versions, and checks for a project devcontainer. Exits non-zero if the environment is not ready.",
	RunE:  runDoctor,
}

// DockerPinger is the minimal Docker surface doctor needs (satisfied by *client.Client).
type DockerPinger interface {
	Ping(ctx context.Context) (types.Ping, error)
	ServerVersion(ctx context.Context) (types.Version, error)
}

type checkResult struct {
	Name   string
	Detail string
	OK     bool
}

func runDoctor(cmd *cobra.Command, args []string) error {
	initVerbosity(cmd)
	ctx := context.Background()

	cli, err := dockerClient()
	if err != nil {
		// Report the Docker failure as a doctor result, then exit 4.
		printCheck(checkResult{Name: "Docker", Detail: err.Error(), OK: false})
		return err
	}
	defer cli.Close()

	results := runDoctorChecks(ctx, cli, Version, resolveWorkspace())
	for _, r := range results {
		printCheck(r)
	}
	if !doctorAllOK(results) {
		return Coded(ExitError, fmt.Errorf("one or more checks failed"))
	}
	return nil
}

// runDoctorChecks gathers environment checks over injected dependencies (pure).
func runDoctorChecks(ctx context.Context, docker DockerPinger, version, workspace string) []checkResult {
	var out []checkResult

	if _, err := docker.Ping(ctx); err != nil {
		out = append(out, checkResult{Name: "Docker", Detail: "daemon not reachable: " + err.Error(), OK: false})
	} else {
		detail := "reachable"
		if v, err := docker.ServerVersion(ctx); err == nil {
			detail = "reachable (server " + v.Version + ")"
		}
		out = append(out, checkResult{Name: "Docker", Detail: detail, OK: true})
	}

	out = append(out, checkResult{Name: "claude-bunker", Detail: version, OK: true})

	if _, err := os.Stat(devcontainer.DevContainerPath(workspace)); err == nil {
		out = append(out, checkResult{Name: "devcontainer.json", Detail: "present", OK: true})
	} else {
		out = append(out, checkResult{Name: "devcontainer.json", Detail: "not found — run `claude-bunker init`", OK: true}) // informational, not a failure
	}
	return out
}

func doctorAllOK(results []checkResult) bool {
	for _, r := range results {
		if !r.OK {
			return false
		}
	}
	return true
}

// printCheck writes one styled check line to stdout (payload).
func printCheck(r checkResult) {
	mark := successMsgStyle.Render("✓")
	if !r.OK {
		mark = errorLabelStyle.Render("✗")
	}
	fmt.Printf("%s %s: %s\n", mark, r.Name, r.Detail)
}
```

Register in `cmd/root.go` `init()`: `rootCmd.AddCommand(doctorCmd)`. (Ensure `doctorCmd.DisableFlagParsing = false` is set alongside the other subcommands if the root loop sets that pattern.)

- [ ] **Step 4: Run tests + build**

Run: `go test ./cmd/ -run 'TestRunDoctorChecks|TestDoctorAllOK' -v` then `go build ./...` then `go test ./...`
Expected: PASS; build clean; full suite green.

- [ ] **Step 5: Commit**

```bash
git add cmd/doctor.go cmd/doctor_test.go cmd/root.go
git commit -m "feat(cli): add doctor command (Docker/versions/devcontainer preflight)"
```

---

## Self-review notes (coverage vs spec §9)

- Exit-code catalog (docker-unavailable → 4, documented) → Task 1.
- `--json` on version + status → Task 2.
- `doctor` preflight → Task 3.

## Follow-on Phase 3 slices (not in this plan)

- `--json` on `prune`; `--dry-run` on mutating commands; docker-ping timeout; first-run onboarding hint.
- Full root flag architecture: registered+documented root flags + `--` passthrough contract in `--help` (the trickiest — root uses `DisableFlagParsing`).
- Wire `ExitCancelled` (2) at more cancel points if any remain beyond `init` abort.
