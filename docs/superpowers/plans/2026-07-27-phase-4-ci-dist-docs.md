# Phase 4 — CI, Distribution & Docs Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Finish the redesign (spec §10): a test/lint CI gate, closing the last name-store coverage gap, goreleaser distribution polish (commit+date version, packaged completions + man pages, a gated Homebrew formula), a repo-wide housekeeping pass, and a README/CLAUDE.md rewrite that matches the current devcontainer-based reality.

**Architecture:** All changes are additive or documentation/tooling. The two existing release workflows (`release.yml` → goreleaser → chains `base-image.yml`) are load-bearing and MUST NOT be modified except for the one documented Homebrew-secret env addition (gated). A new `ci.yml` provides the PR/tag test gate. Housekeeping lands as one isolated mechanical commit so it doesn't pollute feature diffs.

**Tech Stack:** Go 1.26.0, Cobra v1.10.2 (+ `cobra/doc` for man pages), goreleaser v2, GitHub Actions. No new runtime dependencies (`cobra/doc` is a build-time tool dep).

## Global Constraints

- Go 1.26.0 (`go.mod` is the single source of truth; workflows pin via `go-version-file: go.mod`).
- Do NOT modify `.github/workflows/release.yml` or `base-image.yml` except the single gated `HOMEBREW_TAP_GITHUB_TOKEN` env line (Task 3, and only alongside the brews block). They chain release→base-image via `gh workflow run`; changing triggers/permissions breaks the chain.
- Every task keeps `go build ./...`, `go vet ./...`, and `go test ./...` green, and `gofmt -l .` empty after Task 1.
- Housekeeping is behavior-preserving only (gofmt + safe modernizers); the `ImageInspectWithRaw`→`ImageInspect` API migration is a SEPARATE commit (real API change).
- Docs must describe the ACTUAL current reality: one config file `.devcontainer/devcontainer.json` (bunker extras under `customizations["claude-bunker"]`); the firewall allowlist from `internal/container/domains.go` (NO npm/pypi by default — added per-language by `init`); enforcement via a runtime read-only `/etc/claude-code/managed-settings.json` (host `settings.json`/`settings.local.json` are NOT injected); the `refresh-firewall.sh` 5-min re-resolve daemon (no restart needed for IP rotation).
- macOS test note: recent full-suite runs are green on darwin (the old socket was deleted in Phase 1); ubuntu is the CI test floor, macOS may be added if green.
- TDD where code is involved; exact commands + expected output; no placeholders.

## Task Order & Dependencies

1. **Task 1 — Housekeeping** (repo-wide `gofmt -w` + safe modernizers, one commit; `ImageInspectWithRaw` migration a second commit). Do FIRST so the CI gofmt/vet gate is satisfiable and later diffs stay clean.
2. **Task 2 — Name-store tests** (`internal/sessions` — `PruneStaleNames`, `SetCustomName`/`GetCustomName`, `RenameContainer`, custom-name resolution). Independent. (Isolation is already fixed — do NOT redo it.)
3. **Task 3 — goreleaser / distribution** (`cmd.Commit`/`cmd.Date` + version renderers; `cmd/genman` man generator + `RootCmd()`; `.goreleaser.yml` completions/manpages/ldflags/archives; Homebrew `brews:` gated on an external tap repo — deferred/documented). Needed before Task 4's build job.
4. **Task 4 — CI workflow** (new `.github/workflows/ci.yml`: lint = gofmt+vet, test = `go test ./...`, build = `goreleaser build --snapshot`). Depends on Task 1 (gofmt-clean) and Task 3 (`cmd/genman` exists for goreleaser before-hooks). Integration smoke (§6.6) is DEFERRED (documented) — depends on an unlanded apt fix + privileged Docker.
5. **Task 5 — Docs** (near-total README.md rewrite + surgical CLAUDE.md edits to the current devcontainer reality). Last, so it documents the final state.

---

<!-- TASK SECTIONS SPLICED BELOW DURING ASSEMBLY -->

### Task 1: Housekeeping — gofmt + safe modernizers

Two commits, no feature diff. **Commit 1** is behavior-preserving formatter + modernizer noise removal (`gofmt`, `any`, `slices.Contains`, `fmt.Fprintf`, `strings.SplitSeq`, `errors.AsType`). **Commit 2** is a separate, reviewable Docker-API migration (`ImageInspectWithRaw` → `ImageInspect`). Baseline at start of task is already green (`go build ./...`, `go vet ./...` zero output, `go test ./...` pass) and `git status` is clean, so every gofmt-dirty file below is pre-existing on master.

All stdlib/API symbols cited here were verified against the live toolchain (go1.26.2, go.mod `go 1.26.0`):
- `errors.AsType[E error](err error) (E, bool)` — present (`go doc errors.AsType`).
- `strings.SplitSeq(s, sep string) iter.Seq[string]` — present (`go doc strings.SplitSeq`).
- `(*client.Client).ImageInspect(ctx, imageID string, ...ImageInspectOption) (image.InspectResponse, error)` — present; the deprecated `ImageInspectWithRaw` returns `(image.InspectResponse, []byte, error)`, i.e. the **same** `image.InspectResponse` value, so the `.Created` field both sites read is unchanged.

**Files:**

- Modify (Commit 1, mechanical/whitespace via `gofmt -w .`): `cmd/completion.go`, `cmd/init.go`, `cmd/sessions_list.go`, `internal/config/naming.go`, `internal/container/domains.go`, `internal/container/lifecycle.go`, `internal/sandbox/plugins.go`, `internal/sessions/state.go` (the 8 `gofmt -l .` files — pure alignment / import-order / trailing-blank-line).
- Modify (Commit 1, `interface{}`→`any` repo-wide): 8 non-test files — `cmd/init.go`, `internal/config/project.go`, `internal/container/features.go`, `internal/devcontainer/devcontainer.go`, `internal/devcontainer/generate.go`, `internal/devcontainer/load.go`, `internal/sandbox/plugins.go`, `internal/sandbox/seed.go`; plus 11 test files — `cmd/init_test.go`, `cmd/prune_test.go`, `cmd/status_test.go`, `internal/config/expand_test.go`, `internal/config/fingerprint_test.go`, `internal/container/features_test.go`, `internal/container/generate_test.go`, `internal/devcontainer/devcontainer_test.go`, `internal/devcontainer/generate_test.go`, `internal/devcontainer/jsonc_test.go`, `internal/devcontainer/load_test.go`.
- Modify (Commit 1, hand edits): `cmd/root.go` (two `slices.Contains` rewrites + `"slices"` import), `internal/config/fingerprint.go` (line 68 `fmt.Fprintf`), `cmd/init.go` (lines 493 & 521 `strings.SplitSeq`), `cmd/errors.go` (line 54 `errors.AsType`).
- Modify (Commit 2, API migration): `cmd/status.go` (line 95), `internal/container/build.go` (line 216).
- Test: no new tests. Regression guard is the existing suite — `cmd/errors_test.go` exercises `ExitCodeFor`/`CodedError` (covers the `errors.AsType` rewrite); `cmd/status_test.go` covers `statusInfo`/`ImageBuilt`. `mergeSettings` has no dedicated test, but `strings.SplitSeq` yields exactly the substrings `strings.Split` would, so `go test ./...` staying green is a sufficient guard.

**Interfaces:**
- Consumes: existing code only.
- Produces: `gofmt -l .` empty; zero behavior change. Two call sites move from `cli.ImageInspectWithRaw(ctx, id) (image.InspectResponse, []byte, error)` to `cli.ImageInspect(ctx, id) (image.InspectResponse, error)`.

---

#### Commit 1 — gofmt + safe modernizers

**Step 1.1 — Hand-edit `cmd/root.go`: two contains-loops → `slices.Contains`, add import.**

Add `"slices"` to the first import group (gofmt will order it after `"os"`, before `"strconv"`). Current block:

```go
import (
	"fmt"
	"os"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/Devon-White/claude-bunker/internal/container"
)
```

becomes:

```go
import (
	"fmt"
	"os"
	"slices"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/Devon-White/claude-bunker/internal/container"
)
```

Replace the `--no-color` scan (lines 132–138):

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

with:

```go
	noColor := slices.Contains(os.Args[1:], "--no-color")
	applyColorProfile(noColor)
```

Replace the `--json` scan (lines 148–158) — keep the surrounding `case "version", "--version", "-v":` logic intact, only collapse the loop:

```go
			hasJSON := false
			for _, a := range os.Args[2:] {
				if a == "--json" {
					hasJSON = true
					break
				}
			}
			if !hasJSON {
```

with:

```go
			hasJSON := slices.Contains(os.Args[2:], "--json")
			if !hasJSON {
```

(`os`, `fmt`, `strconv` all remain used elsewhere in the file; no other import churn.)

**Step 1.2 — Hand-edit `internal/config/fingerprint.go` line 68: `Sprintf`-into-`Write` → `Fprintf`.**

`h` is the `sha256.New()` hash, which is an `io.Writer`. Replace:

```go
			h.Write([]byte(fmt.Sprintf("%s=%v,", k, opts[k])))
```

with:

```go
			fmt.Fprintf(h, "%s=%v,", k, opts[k])
```

(`fmt` stays imported — used by `fmt.Sprintf("%x", ...)` at lines 106 & 148.)

**Step 1.3 — Hand-edit `cmd/init.go` lines 493 & 521: `strings.Split` range → `strings.SplitSeq`.**

Both loops only iterate; no index/slice reuse, so the swap is safe. `strings` is already imported and `slices` is already imported (no new imports). Line 493:

```go
		for _, d := range strings.Split(s.allowDomains, ",") {
```

→

```go
		for d := range strings.SplitSeq(s.allowDomains, ",") {
```

Line 521:

```go
		for _, pair := range strings.Split(s.envVars, ",") {
```

→

```go
		for pair := range strings.SplitSeq(s.envVars, ",") {
```

**Step 1.4 — Hand-edit `cmd/errors.go` line 53–55: `errors.As` → `errors.AsType`.**

Replace:

```go
	var ce *CodedError
	if errors.As(err, &ce) {
		return ce.Code
	}
```

with:

```go
	if ce, ok := errors.AsType[*CodedError](err); ok {
		return ce.Code
	}
```

(`errors` stays imported — now via `errors.AsType`.)

**Step 1.5 — Repo-wide `interface{}` → `any`.**

Run the gofmt rewrite (verified non-destructively to turn `map[string]interface{}` into `map[string]any`; `any` is a pure alias, zero behavior change). This covers all 8 non-test files and 11 test files:

```bash
gofmt -r 'interface{} -> any' -w .
```

**Step 1.6 — `gofmt -w .` LAST, so the Step 1.1–1.4 edits are reformatted and the 8 pre-existing dirty files are cleaned:**

```bash
gofmt -w .
```

**Step 1.7 — Verify (all must pass; `gofmt -l .` MUST print nothing):**

```bash
gofmt -l .        # expect: no output
go build ./...    # expect: no output, exit 0
go vet ./...      # expect: no output, exit 0
go test ./...     # expect: all packages ok/cached, exit 0
```

If `gofmt -l .` prints any path, re-run `gofmt -w .` and re-check — do not commit until it is empty.

**Step 1.8 — Commit (stage exactly the touched files):**

```bash
git add cmd/completion.go cmd/init.go cmd/sessions_list.go \
        internal/config/naming.go internal/container/domains.go \
        internal/container/lifecycle.go internal/sandbox/plugins.go \
        internal/sessions/state.go \
        cmd/root.go internal/config/fingerprint.go cmd/errors.go \
        cmd/init_test.go cmd/prune_test.go cmd/status_test.go \
        internal/config/project.go internal/config/expand_test.go \
        internal/config/fingerprint_test.go internal/container/features.go \
        internal/container/features_test.go internal/container/generate_test.go \
        internal/devcontainer/devcontainer.go internal/devcontainer/generate.go \
        internal/devcontainer/load.go internal/devcontainer/devcontainer_test.go \
        internal/devcontainer/generate_test.go internal/devcontainer/jsonc_test.go \
        internal/devcontainer/load_test.go internal/sandbox/seed.go
git commit -m "chore: gofmt + safe modernizers (any, slices.Contains, SplitSeq, AsType)"
```

> Note: `git add -A` over the repo is acceptable and simpler here since the working tree started clean and Commit 2's files (`cmd/status.go`, `internal/container/build.go`) are NOT yet touched at this point — nothing else can leak in. If in doubt, run `git status` first and confirm only Commit-1 files appear.

---

#### Commit 2 — migrate off deprecated `ImageInspectWithRaw`

Both call sites already discard the raw `[]byte` with `_`, so this is a mechanical arity change. No new imports are required (verified): `cmd/status.go` does not name the `image` package — `imgInspect` is inferred and only `.Created` (a string) is read; `internal/container/build.go` already imports `github.com/docker/docker/api/types/image` and still uses it at line 137 (`image.PullOptions{}`), so the import stays live.

**Step 2.1 — `cmd/status.go` line 95.** Replace:

```go
	imgInspect, _, err := cli.ImageInspectWithRaw(ctx, imageTag)
```

with:

```go
	imgInspect, err := cli.ImageInspect(ctx, imageTag)
```

(The following `imgInspect.Created` / `time.Parse` block is unchanged — `image.InspectResponse.Created` is the same field.)

**Step 2.2 — `internal/container/build.go` line 216** (inside `ImageExists`). Replace:

```go
	_, _, err := cli.ImageInspectWithRaw(ctx, imageTag)
	return err == nil
```

with:

```go
	_, err := cli.ImageInspect(ctx, imageTag)
	return err == nil
```

**Step 2.3 — Verify:**

```bash
gofmt -l cmd/status.go internal/container/build.go   # expect: no output
go build ./...    # expect: exit 0
go vet ./...      # expect: no output, exit 0
go test ./...     # expect: all pass, exit 0
```

**Step 2.4 — Commit:**

```bash
git add cmd/status.go internal/container/build.go
git commit -m "chore: migrate off deprecated ImageInspectWithRaw"
```

---

**Done when:** two commits landed; `gofmt -l .` empty; `go build ./... && go vet ./... && go test ./...` all green; no `interface{}`, no hand-rolled contains-loop in `cmd/root.go`, no `ImageInspectWithRaw` call remaining in the tree (`grep -rn ImageInspectWithRaw .` → no hits).

---

### Task 2: Name-store test coverage

Adds direct regression guards for the second JSON store (`session-names.json` /
`nameStore`) and the container-rename direction. These are the only genuinely
thin spots left after the §10 real-HOME-wipe fix: `PruneStaleNames` (the exact
function the §10 bug was about) is only exercised transitively inside
`FetchSnapshot`, `SetCustomName`/`GetCustomName` have no direct round-trip test,
and `RenameContainer` is completely untested. The sessions package already has a
package-wide `TestMain` (`internal/sessions/main_test.go`) that points
`CLAUDE_BUNKER_STORE_DIR` at a throwaway temp dir for the whole test run, so new
tests here are auto-isolated from the developer's real `~/.claude`. **Do NOT add,
change, or duplicate any isolation plumbing** — the §10 fix is already merged and
verified.

**Files:**
- Modify: `internal/sessions/manager_test.go` — extend the existing `mockClient`
  to capture `ContainerRename` args (behavior-preserving: new zero-value fields;
  the mock already stubs the method at line 55).
- Create: `internal/sessions/names_test.go` — the new coverage (one ~90-line
  test file, white-box `package sessions`).

**Interfaces:**

Consumes (all live, verified against source):
- `NewManager(cli DockerClient) *Manager` — `manager.go:48`
- `func SetCustomName(containerID, name string) error` — `names.go:13`
- `func GetCustomName(containerID string) string` — `names.go:8`
- `func PruneStaleNames(activeIDs map[string]bool)` — `names.go:18`
- `func (m *Manager) RenameContainer(ctx, containerID, newName string) error` —
  `manager.go:435` (persists via `SetCustomName`, then calls
  `m.cli.ContainerRename` and **swallows** its error at `manager.go:442`)
- `func (m *Manager) ResolveContainer(ctx, nameOrPrefix string) (ContainerState, error)`
  — `manager.go:262` (custom-name exact/prefix match at `manager.go:271-277`;
  `FetchSnapshot` applies custom name to `DisplayName` at `manager.go:81`)
- `execAgentsJSON` seam — `agents.go:66`, `var execAgentsJSON = func(ctx context.Context, cli *client.Client, containerID string) (string, error)`
- `ctr.LabelKey` — `internal/container/constants.go:49` (`"claude-bunker"`)
- the existing `mockClient` + `TestMain` HOME-redirect isolation.

Produces: `internal/sessions/names_test.go` (tests only; no production-source
change).

---

Because the production code under test **already exists and already passes**,
this is a regression-guard/coverage task rather than a feature. The TDD "red"
step is therefore demonstrated by momentarily breaking the source to prove each
new test actually fails when the behavior regresses, then reverting — not by
writing a test against not-yet-written code.

#### Step 2.1 — Extend `mockClient` to capture `ContainerRename` args

Edit `internal/sessions/manager_test.go`. The current struct (lines 17-23) is:

```go
// mockClient implements DockerClient for testing.
type mockClient struct {
	containers []container.Summary
	inspect    map[string]container.InspectResponse
	top        map[string]container.TopResponse
	stopErr    error
	removeErr  error
}
```

Replace it with (adds four zero-value fields; every existing test constructs
`mockClient` with named fields, so this is fully behavior-preserving):

```go
// mockClient implements DockerClient for testing.
type mockClient struct {
	containers []container.Summary
	inspect    map[string]container.InspectResponse
	top        map[string]container.TopResponse
	stopErr    error
	removeErr  error

	// ContainerRename capture (asserted by names_test.go).
	renameErr    error
	renameCalled bool
	renamedID    string
	renamedName  string
}
```

The current `ContainerRename` stub (lines 55-57) is:

```go
func (m *mockClient) ContainerRename(_ context.Context, _, _ string) error {
	return nil
}
```

Replace it with (records the args and returns the configurable error; defaults to
`nil`, so existing tests are unaffected):

```go
func (m *mockClient) ContainerRename(_ context.Context, id, newName string) error {
	m.renameCalled = true
	m.renamedID = id
	m.renamedName = newName
	return m.renameErr
}
```

#### Step 2.2 — Create `internal/sessions/names_test.go`

Write the file exactly as below. Notes on design that the implementer must not
"simplify" away:
- Every test uses **namespaced container IDs** (`prune-*`, `rt-1`, `rename-*`,
  `resolve-cid`). `nameStore` is a package-level global shared across the whole
  test binary; unique keys make each test order-independent even though
  `FetchSnapshot`/`ResolveContainer` call `PruneStaleNames` on unrelated IDs.
- `TestResolveContainer_CustomNameMatch` seeds the custom name on the **same ID**
  as the mock container so `FetchSnapshot`'s post-loop `PruneStaleNames(activeIDs)`
  (`manager.go:101`) keeps it (the ID is in `activeIDs`).
- The running-container resolve path mirrors the existing
  `TestResolveContainer_ExactMatch`: stub `execAgentsJSON` to `"[]"` and supply
  `inspect`/`top` for the container ID.

```go
package sessions

import (
	"context"
	"errors"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"

	ctr "github.com/Devon-White/claude-bunker/internal/container"
)

// TestPruneStaleNames is a direct regression guard for the §10 bug: the exact
// function (names.go PruneStaleNames -> jsonMapStore.Prune) that, before the
// STORE_DIR isolation fix, could wipe the developer's real ~/.claude custom
// names. It must keep entries whose ID is in the active set and drop the rest.
func TestPruneStaleNames(t *testing.T) {
	ids := []string{"prune-a", "prune-b", "prune-c"}
	for _, id := range ids {
		if err := SetCustomName(id, "name-"+id); err != nil {
			t.Fatalf("SetCustomName(%q) failed: %v", id, err)
		}
	}

	PruneStaleNames(map[string]bool{"prune-a": true, "prune-c": true})

	if got := GetCustomName("prune-a"); got != "name-prune-a" {
		t.Errorf("prune-a: want survive %q, got %q", "name-prune-a", got)
	}
	if got := GetCustomName("prune-c"); got != "name-prune-c" {
		t.Errorf("prune-c: want survive %q, got %q", "name-prune-c", got)
	}
	if got := GetCustomName("prune-b"); got != "" {
		t.Errorf("prune-b: want pruned (empty), got %q", got)
	}
}

// TestCustomName_RoundTrip covers SetCustomName/GetCustomName including the
// empty-value delete semantics of jsonMapStore.Set (store.go:94).
func TestCustomName_RoundTrip(t *testing.T) {
	const id = "rt-1"

	if got := GetCustomName(id); got != "" {
		t.Fatalf("want empty before set, got %q (store contaminated?)", got)
	}

	if err := SetCustomName(id, "custom-display"); err != nil {
		t.Fatalf("SetCustomName failed: %v", err)
	}
	if got := GetCustomName(id); got != "custom-display" {
		t.Errorf("after set: want %q, got %q", "custom-display", got)
	}

	// Empty value deletes the entry.
	if err := SetCustomName(id, ""); err != nil {
		t.Fatalf("SetCustomName(delete) failed: %v", err)
	}
	if got := GetCustomName(id); got != "" {
		t.Errorf("after delete: want empty, got %q", got)
	}
}

// TestRenameContainer asserts RenameContainer both persists the custom name and
// drives the Docker rename with matching args — the container-rename "direction"
// of the spec's rename coverage.
func TestRenameContainer(t *testing.T) {
	const id = "rename-1"
	cli := &mockClient{}
	mgr := NewManager(cli)

	if err := mgr.RenameContainer(context.Background(), id, "new-name"); err != nil {
		t.Fatalf("RenameContainer failed: %v", err)
	}

	if got := GetCustomName(id); got != "new-name" {
		t.Errorf("custom name: want %q, got %q", "new-name", got)
	}
	if !cli.renameCalled {
		t.Error("expected ContainerRename to be called")
	}
	if cli.renamedID != id || cli.renamedName != "new-name" {
		t.Errorf("ContainerRename args: got (%q, %q), want (%q, %q)",
			cli.renamedID, cli.renamedName, id, "new-name")
	}
}

// TestRenameContainer_DockerErrorSwallowed verifies manager.go:442: a failing
// Docker rename is ignored, yet the custom display name still persists.
func TestRenameContainer_DockerErrorSwallowed(t *testing.T) {
	const id = "rename-2"
	cli := &mockClient{renameErr: errors.New("name conflict")}
	mgr := NewManager(cli)

	if err := mgr.RenameContainer(context.Background(), id, "renamed"); err != nil {
		t.Fatalf("RenameContainer should swallow the Docker error, got: %v", err)
	}
	if got := GetCustomName(id); got != "renamed" {
		t.Errorf("custom name must persist despite Docker error: got %q", got)
	}
	if !cli.renameCalled {
		t.Error("expected ContainerRename to be attempted")
	}
}

// TestResolveContainer_CustomNameMatch seeds a custom name and asserts it both
// drives DisplayName (FetchSnapshot, manager.go:81) and satisfies the exact
// match in ResolveContainer (manager.go:272).
func TestResolveContainer_CustomNameMatch(t *testing.T) {
	orig := execAgentsJSON
	defer func() { execAgentsJSON = orig }()
	execAgentsJSON = func(_ context.Context, _ *client.Client, _ string) (string, error) {
		return "[]", nil
	}

	const cid = "resolve-cid"
	if err := SetCustomName(cid, "my-favorite"); err != nil {
		t.Fatalf("SetCustomName failed: %v", err)
	}

	cli := &mockClient{
		containers: []container.Summary{
			{ID: cid, State: "running", Labels: map[string]string{ctr.LabelKey: "project-a1b2c3d4"}},
		},
		inspect: map[string]container.InspectResponse{
			cid: {ContainerJSONBase: &container.ContainerJSONBase{State: &container.State{}}},
		},
		top: map[string]container.TopResponse{
			cid: {Titles: []string{"PID", "COMMAND"}, Processes: [][]string{}},
		},
	}

	mgr := NewManager(cli)
	c, err := mgr.ResolveContainer(context.Background(), "my-favorite")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.ID != cid {
		t.Errorf("expected ID %q, got %q", cid, c.ID)
	}
	if c.DisplayName != "my-favorite" {
		t.Errorf("expected DisplayName to reflect custom name, got %q", c.DisplayName)
	}
}
```

#### Step 2.3 — Verify the new tests pass against current (correct) source

```bash
go test ./internal/sessions/... -run 'TestPruneStaleNames|TestCustomName_RoundTrip|TestRenameContainer|TestResolveContainer_CustomNameMatch' -count=1 -v
```

Expected: all five new tests (`TestPruneStaleNames`, `TestCustomName_RoundTrip`,
`TestRenameContainer`, `TestRenameContainer_DockerErrorSwallowed`,
`TestResolveContainer_CustomNameMatch`) print `--- PASS` and the package line
ends `ok  github.com/Devon-White/claude-bunker/internal/sessions`.

#### Step 2.4 — Prove the guards actually catch a regression (temporary break → red → revert)

This is the "red" half of TDD for a coverage task: confirm each new test fails
when its target behavior regresses, then restore the source. **Both edits below
are reverted before committing.**

Break `PruneStaleNames` — in `internal/sessions/names.go`, invert the keep
predicate (`return activeIDs[key]` → `return !activeIDs[key]`), then run:

```bash
go test ./internal/sessions/... -run TestPruneStaleNames -count=1
```

Expected: `--- FAIL: TestPruneStaleNames` (prune-a/prune-c reported dropped,
prune-b reported surviving), package line `FAIL`. Revert the edit.

Break `RenameContainer` — in `internal/sessions/manager.go`, comment out the
`_ = m.cli.ContainerRename(ctx, containerID, newName)` line (`manager.go:442`),
then run:

```bash
go test ./internal/sessions/... -run TestRenameContainer -count=1
```

Expected: `--- FAIL: TestRenameContainer` ("expected ContainerRename to be
called"). Revert the edit.

Re-confirm green after reverting both:

```bash
go test ./internal/sessions/... -count=1
```

Expected: `ok  github.com/Devon-White/claude-bunker/internal/sessions`.

#### Step 2.5 — Format, vet, full build & test

```bash
gofmt -l internal/sessions/names_test.go internal/sessions/manager_test.go
go vet ./internal/sessions/...
go build ./...
go test ./... -count=1
```

Expected: `gofmt -l` prints **nothing** (both files already formatted); `go vet`
prints nothing; `go build` succeeds silently; `go test ./...` reports `ok` for
every package. (Per project memory, the 2 pre-existing macOS socket-path
failures in `internal/sessions` are unrelated to this change — if they appear,
they are not a regression from this task; confirm they are the same
socket-path-length tests and no new failures were introduced.)

#### Step 2.6 — Commit

```bash
git add internal/sessions/names_test.go internal/sessions/manager_test.go
git commit -m "test(sessions): cover nameStore prune/round-trip and container rename

Add internal/sessions/names_test.go with direct regression guards for the
second JSON store and the rename direction that were only exercised
transitively: PruneStaleNames (the exact §10-bug function), SetCustomName/
GetCustomName round-trip incl. empty-value delete, RenameContainer persist+
Docker-rename (and the swallowed-error path), and custom-name resolution/
DisplayName. Extend mockClient to capture ContainerRename args. Auto-isolated
by the existing package TestMain (CLAUDE_BUNKER_STORE_DIR); no source change."
```

---

### Task 3: goreleaser distribution — commit+date version, completions, man pages, gated Homebrew

Wire full binary provenance (version + commit + build date) into the CLI, teach goreleaser to
generate & bundle shell completions and man pages, and stage a Homebrew formula that ships to a
tap once its one-time prerequisites exist. Code parts are TDD; the goreleaser/CI parts are
config-with-verification.

**Files:**
- Modify: `cmd/root.go` — replace the single `Version` var with a `Version/Commit/Date` block; add `RootCmd()`; extend `renderVersionJSON`.
- Modify: `cmd/ui.go` — `renderVersion` appends `(commit, date)` in dim style.
- Test:   `cmd/root_test.go` — `TestRenderVersionJSON` expects the new three-field object.
- Create: `cmd/genman/main.go` — man-page generator (mirrors `cmd/genbuild/main.go`).
- Modify: `go.mod` / `go.sum` — materialize `cobra/doc` transitive deps (via `go get` + `go mod tidy` AFTER `cmd/genman` exists — see Step 5 ordering note).
- Modify: `.goreleaser.yml` — before-hooks (completions + man pages), builds env+ldflags (commit+CommitDate), archives `files`, and a **gated** `brews` block.
- Modify (GATED — only lands with the `brews` block): `.github/workflows/release.yml` — add one `HOMEBREW_TAP_GITHUB_TOKEN` env line.

**Interfaces:**
- Consumes: `rootCmd` (`*cobra.Command`, `cmd/root.go:16`), `renderVersion(string) string` (`cmd/ui.go:229`), `renderVersionJSON(string) string` (`cmd/root.go:126`), the `completion [bash|zsh|fish|powershell]` command (`cmd/completion.go`), the `cmd/genbuild/main.go` helper-binary pattern, `dimStyle`/`brandStyle`/`boldStyle` (`cmd/ui.go:41-43`).
- Produces: `cmd.Version` / `cmd.Commit` / `cmd.Date` (package-level `string` vars, ldflags-injected), `cmd.RootCmd() *cobra.Command`, the `cmd/genman` helper binary, and goreleaser wiring for completions/manpages/ldflags/brews.

**Verified against live source before drafting** (do not re-derive; already checked):
- `cmd/root.go:13-14` today is exactly `// Version is set via ldflags at build time.` + `var Version = "dev"`. The import block (`cmd/root.go:3-11`) already imports `github.com/spf13/cobra`, so `RootCmd()` needs **no new import**. `strconv` already imported (line 6) — `renderVersionJSON` change needs no new import.
- `cmd/ui.go` already imports `"fmt"` (line 5) and defines `dimStyle` (line 41), `brandStyle` (line 42), `boldStyle` (line 43). `renderVersion` change needs **no new import**.
- `github.com/spf13/cobra/doc` ships in cobra v1.10.2 (present in module cache). `GenManTree(cmd *cobra.Command, header *GenManHeader, dir string) error` and `GenManHeader{Title, Section string}` confirmed from `doc/man_docs.go:38,94`.
- `go run . completion bash|zsh|fish` all succeed on the host (bash 881 / zsh 212 / fish 235 lines) — completions/man pages are arch-independent text, safe to generate in a host-arch before-hook.
- `README.md` exists at repo root. **No `LICENSE` file exists** (so `brews.license` stays commented out).

---

#### Step 1 (TDD — failing test): update `TestRenderVersionJSON`

Edit `cmd/root_test.go` (current body at lines 5-10) so the expectation matches the three-field
object with the **default** `Commit="none"` / `Date="unknown"` (the test never sets ldflags, so the
package defaults apply; `Version` is passed as the literal argument `"1.2.3"`):

```go
package cmd

import "testing"

func TestRenderVersionJSON(t *testing.T) {
	out := renderVersionJSON("1.2.3")
	if out != `{"version":"1.2.3","commit":"none","date":"unknown"}` {
		t.Errorf("renderVersionJSON = %q", out)
	}
}
```

**Verify it fails** (implementation not yet changed):

```bash
go test ./cmd/ -run TestRenderVersionJSON -count=1
```

Expected: FAIL — `renderVersionJSON = "{\"version\":\"1.2.3\"}"`.

---

#### Step 2 (implement): `cmd/root.go` — var block, `RootCmd()`, JSON fields

Replace lines 13-14:

```go
// Version is set via ldflags at build time.
var Version = "dev"
```

with:

```go
// Version, Commit, and Date are set via ldflags at build time.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// RootCmd returns the root command. Exported so out-of-package tools
// (cmd/genman man-page generation) can traverse the command tree.
func RootCmd() *cobra.Command { return rootCmd }
```

Replace `renderVersionJSON` (lines 126-128):

```go
// renderVersionJSON renders the version as a minimal JSON object.
func renderVersionJSON(version string) string {
	return `{"version":` + strconv.Quote(version) + `}`
}
```

with:

```go
// renderVersionJSON renders the version as a minimal JSON object.
func renderVersionJSON(version string) string {
	return `{"version":` + strconv.Quote(version) +
		`,"commit":` + strconv.Quote(Commit) +
		`,"date":` + strconv.Quote(Date) + `}`
}
```

---

#### Step 3 (implement): `cmd/ui.go` — `renderVersion` appends commit/date

Replace `renderVersion` (lines 229-231):

```go
func renderVersion(version string) string {
	return brandStyle.Render("claude-bunker") + " " + boldStyle.Render(version)
}
```

with:

```go
func renderVersion(version string) string {
	s := brandStyle.Render("claude-bunker") + " " + boldStyle.Render(version)
	if Commit != "none" || Date != "unknown" {
		s += dimStyle.Render(fmt.Sprintf(" (%s, %s)", Commit, Date))
	}
	return s
}
```

(`fmt` and `dimStyle` already in scope — no import change.)

---

#### Step 4 (verify + build): code parts green

```bash
gofmt -l cmd/root.go cmd/ui.go cmd/root_test.go        # expect: no output (all three clean)
go build ./...                                          # expect: exit 0
go vet ./cmd/...                                        # expect: exit 0
go test ./cmd/ -count=1                                 # expect: ok  github.com/Devon-White/claude-bunker/cmd
```

Behavioral smoke with ldflags (proves the wiring, matches goreleaser injection):

```bash
go run -ldflags "-X github.com/Devon-White/claude-bunker/cmd.Version=1.2.3 -X github.com/Devon-White/claude-bunker/cmd.Commit=abc1234 -X github.com/Devon-White/claude-bunker/cmd.Date=2026-07-27T00:00:00Z" . version
# expect: claude-bunker 1.2.3 (abc1234, 2026-07-27T00:00:00Z)
go run -ldflags "-X github.com/Devon-White/claude-bunker/cmd.Version=1.2.3 -X github.com/Devon-White/claude-bunker/cmd.Commit=abc1234 -X github.com/Devon-White/claude-bunker/cmd.Date=2026-07-27T00:00:00Z" . version --json
# expect: {"version":"1.2.3","commit":"abc1234","date":"2026-07-27T00:00:00Z"}
```

(With no ldflags, `go run . version` prints just `claude-bunker dev` — the `if Commit != "none" || Date != "unknown"` guard suppresses the empty `(none, unknown)` suffix on unstamped dev builds.)

**Commit A (code):**

```bash
git add cmd/root.go cmd/ui.go cmd/root_test.go
git commit -m "feat(version): inject commit and build date, surface in version output"
```

---

#### Step 5 (create): `cmd/genman/main.go`

> **ORDERING — do this file BEFORE the `go get`/`go mod tidy` in Step 6.** `go mod tidy` prunes any
> require that no in-module package imports. If you run `go get github.com/spf13/cobra/doc && go mod tidy`
> while nothing imports `cobra/doc`, tidy strips it right back out and the go.mod/go.sum delta is
> **empty** (verified). `cmd/genman/main.go` is what imports `cobra/doc`, so it must exist first.

Create `cmd/genman/main.go` (mirrors `cmd/genbuild/main.go`: `package main`, `os.Args[1]` output
dir with a default, `MkdirAll`, then the generate call):

```go
package main

import (
	"os"

	"github.com/spf13/cobra/doc"

	"github.com/Devon-White/claude-bunker/cmd"
)

func main() {
	out := "manpages"
	if len(os.Args) > 1 {
		out = os.Args[1]
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		panic(err)
	}
	hdr := &doc.GenManHeader{Title: "CLAUDE-BUNKER", Section: "1"}
	if err := doc.GenManTree(cmd.RootCmd(), hdr, out); err != nil {
		panic(err)
	}
}
```

---

#### Step 6 (deps): materialize `cobra/doc` transitive modules

Now that `cmd/genman` imports `cobra/doc`, lock the man-gen dependency hashes:

```bash
go get github.com/spf13/cobra/doc
go mod tidy
```

**Exact delta this produces** (verified by running it against the live tree):

`go.mod` — three new `// indirect` requires appended to the indirect `require` block:

```
	github.com/cpuguy83/go-md2man/v2 v2.0.7 // indirect
	github.com/russross/blackfriday/v2 v2.1.0 // indirect
	go.yaml.in/yaml/v3 v3.0.4 // indirect
```

`go.sum` — new full `h1:` + `/go.mod` hashes for the above plus module-graph completeness entries
(`go-md2man/v2 v2.0.7`, `blackfriday/v2 v2.1.0`, `go.yaml.in/yaml/v3 v3.0.4`, and the test-graph deps
`kr/pretty v0.3.1`, `kr/text v0.2.0`, `rogpeppe/go-internal v1.14.1`, `gopkg.in/check.v1`).

> **Map correction:** the understand-map predicted `go-md2man/v2 v2.0.6`; the resolver actually pulls
> **v2.0.7**, and additionally adds `go.yaml.in/yaml/v3 v3.0.4` as a new indirect require (the map only
> mentioned md2man + blackfriday). Take whatever the live `go get`/`go mod tidy` emit as authoritative —
> the versions above are the current resolution, not a hand-maintained list.

**Verify the generator builds and produces output:**

```bash
go build ./cmd/genman/            # expect: exit 0 (removes any stray ./genman binary it drops: `rm -f genman`)
gofmt -l cmd/genman/main.go       # expect: no output
go run ./cmd/genman /tmp/mantest  # expect: exit 0
ls /tmp/mantest/claude-bunker.1   # expect: file exists
```

> Note: `genman` emits **one `.1` per command** — `claude-bunker.1`, `claude-bunker-shell.1`,
> `claude-bunker-init.1`, `claude-bunker-sessions.1`, `claude-bunker-sessions-attach.1`, … (~15 files).
> The archives `files: manpages/*` glob (Step 7) bundles all of them; the Homebrew `man1.install Dir[...]`
> (Step 8) installs all of them. This is intentional, not a bug.

**Commit B (generator + deps):**

```bash
git add cmd/genman/main.go go.mod go.sum
git commit -m "feat(docs): add cmd/genman man-page generator"
```

---

#### Step 7 (config): rewrite `.goreleaser.yml`

Full replacement of the current 35-line file. Adds `before.hooks` (completions + man pages), an
`env` + expanded `ldflags` on the build (commit + **CommitDate**, reproducible), an archives `files`
block, and the gated `brews` section. `checksum` / `release` / `changelog` are unchanged from today.

```yaml
version: 2

before:
  hooks:
    - go mod tidy
    - mkdir -p completions manpages
    - sh -c 'go run . completion bash > completions/claude-bunker.bash'
    - sh -c 'go run . completion zsh  > completions/claude-bunker.zsh'
    - sh -c 'go run . completion fish > completions/claude-bunker.fish'
    - go run ./cmd/genman manpages

builds:
  - main: .
    binary: claude-bunker
    env:
      - CGO_ENABLED=0
    ldflags:
      - -s -w
      - -X github.com/Devon-White/claude-bunker/cmd.Version={{.Version}}
      - -X github.com/Devon-White/claude-bunker/cmd.Commit={{.Commit}}
      - -X github.com/Devon-White/claude-bunker/cmd.Date={{.CommitDate}}
    goos:
      - linux
      - darwin
      - windows
    goarch:
      - amd64
      - arm64

archives:
  - formats: ['tar.gz']
    format_overrides:
      - goos: windows
        formats: ['zip']
    name_template: "{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}"
    files:
      - src: completions/*
      - src: manpages/*
      - src: README.md

checksum:
  name_template: "checksums.txt"

brews:
  - name: claude-bunker
    repository:
      owner: Devon-White
      name: homebrew-tap
      token: "{{ .Env.HOMEBREW_TAP_GITHUB_TOKEN }}"
    homepage: "https://github.com/Devon-White/claude-bunker"
    description: "Run Claude Code in a hardened, firewalled sandbox container"
    # license: "MIT"   # uncomment once a LICENSE file exists in the repo root (none today)
    # skip_upload stays `true` until BOTH prerequisites in "Manual prerequisites"
    # below are satisfied (tap repo exists + HOMEBREW_TAP_GITHUB_TOKEN secret set).
    # Flip to `auto` to actually publish (auto still skips prereleases/snapshots).
    skip_upload: true
    install: |
      bin.install "claude-bunker"
      bash_completion.install "completions/claude-bunker.bash" => "claude-bunker"
      zsh_completion.install "completions/claude-bunker.zsh" => "_claude-bunker"
      fish_completion.install "completions/claude-bunker.fish"
      man1.install Dir["manpages/*.1"]
    test: |
      system "#{bin}/claude-bunker", "version"

release:
  prerelease: auto

changelog:
  sort: asc
  filters:
    exclude:
      - "^docs:"
      - "^test:"
```

Config notes (why each choice):
- `{{.CommitDate}}` (RFC3339, tied to the commit) — **not** `{{.Date}}` (build wall-clock) — keeps the
  stamped date reproducible so it plays nicely with the fingerprint/caching story in CLAUDE.md.
- The three completion hooks are wrapped in `sh -c '…'` because they use shell redirection (`>`);
  goreleaser splits hook strings with shellwords and does not invoke a shell, so a bare `go run … > file`
  would pass `>` as an argument. `mkdir -p completions manpages` and `go run ./cmd/genman manpages` have
  no shell metacharacters and run bare.
- `mkdir -p completions manpages` must precede the completion hooks because `>` creates the file but not
  its parent directory. `genman` creates its own dir via `MkdirAll`, but the shared `mkdir` is harmless.
- archives `files` uses the object form (`- src: …`) uniformly. goreleaser v2 also accepts bare-string
  entries, but keeping all three as `src:` objects avoids the mixed-form ambiguity in the map's snippet.

**Verify** (goreleaser is **not** installed on this host — do not block on it):

```bash
go build ./...                    # expect: exit 0 — sanity, config edit doesn't touch Go
go test ./cmd/... -count=1        # expect: ok
# The completion + man before-hooks are individually reproducible without goreleaser:
mkdir -p completions manpages
go run . completion bash > completions/claude-bunker.bash && head -1 completions/claude-bunker.bash
go run ./cmd/genman manpages && ls manpages/claude-bunker.1
rm -rf completions manpages       # scratch artifacts — do NOT commit; add to .gitignore if desired
```

Full end-to-end validation of the `.goreleaser.yml` (archives bundling, formula rendering) requires
goreleaser and should be run in CI or locally after `brew install goreleaser`:

```bash
# CI or a goreleaser-equipped machine only:
HOMEBREW_TAP_GITHUB_TOKEN=dummy goreleaser release --snapshot --clean --skip=publish
```

(The dummy env satisfies the `{{ .Env.HOMEBREW_TAP_GITHUB_TOKEN }}` template during snapshot; nothing is
pushed because `--skip=publish` + `skip_upload: true`.) This is a **note**, not a blocking gate.

**Commit C (goreleaser config):**

```bash
git add .goreleaser.yml
git commit -m "build(goreleaser): bundle completions and man pages, stamp commit/date"
```

---

#### Step 8 (GATED — Homebrew tap): `release.yml` env line + one-time manual prerequisites

The `brews` block above pushes the generated formula to a **separate** repo `Devon-White/homebrew-tap`.
The default `GITHUB_TOKEN` is scoped to the source repo and **cannot** push there, so a cross-repo PAT
must be threaded in. Add exactly **one** line to the existing env block in
`.github/workflows/release.yml` (today lines 35-36):

```yaml
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
```

becomes:

```yaml
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
          HOMEBREW_TAP_GITHUB_TOKEN: ${{ secrets.HOMEBREW_TAP_GITHUB_TOKEN }}
```

This is the **only** permitted change to `release.yml` (per the global constraint). Do not touch its
triggers, permissions, or the `gh workflow run base-image.yml` chain step.

**Manual prerequisites (one-time, MUST be done by a human before flipping `skip_upload` to `auto`):**
1. Create the public repo **`Devon-White/homebrew-tap`** (empty is fine).
2. Create a PAT with `repo` scope on that tap repo and add it to the `claude-bunker` repo as an Actions
   secret named **`HOMEBREW_TAP_GITHUB_TOKEN`**.
3. Once both exist, change `skip_upload: true` → `skip_upload: auto` in `.goreleaser.yml` so tagged
   (non-prerelease) releases publish the formula while prereleases/snapshots still skip.

Until those exist, `skip_upload: true` keeps `goreleaser release --clean` **succeeding** on a real tag
without a tap (the formula is generated but not uploaded), so this step does not break the existing
release chain.

**Verify:**

```bash
python3 -c "import yaml,sys; yaml.safe_load(open('.github/workflows/release.yml')); print('release.yml OK')"
# expect: release.yml OK   (adjust if pyyaml absent; otherwise a manual read of the diff)
git diff .github/workflows/release.yml   # confirm EXACTLY one added line
```

**Commit D (gated CI env):**

```bash
git add .github/workflows/release.yml
git commit -m "ci(release): pass HOMEBREW_TAP_GITHUB_TOKEN for Homebrew formula push"
```

---

#### Done-criteria checklist
- [ ] `gofmt -l cmd/ .goreleaser.yml` clean for the files this task creates/edits (pre-existing gofmt debt in `cmd/completion.go`, `cmd/init.go`, `cmd/sessions_list.go` is **Task 1's** job — do not fix here).
- [ ] `go build ./...`, `go vet ./...`, `go test ./... -count=1` green.
- [ ] `go run … version` / `version --json` show commit + date when ldflags are set, and omit the suffix on unstamped dev builds.
- [ ] `go run ./cmd/genman <dir>` writes `claude-bunker.1` (+ per-subcommand pages).
- [ ] `.goreleaser.yml` before-hooks reproduce completions + man pages standalone; full `goreleaser release --snapshot --clean --skip=publish` validation deferred to CI / a goreleaser-equipped host.
- [ ] Homebrew publish stays inert (`skip_upload: true`) until the tap repo + PAT secret exist.

---

### Task 4: CI workflow (test + lint + build gate)

**Files:**
- Create: `.github/workflows/ci.yml`
- Do NOT modify: `.github/workflows/release.yml`, `.github/workflows/base-image.yml` (release-only; release chains to base-image via `gh workflow run` — changing their triggers/permissions breaks the chain)

**Interfaces:**
- Consumes:
  - `go.mod` — single source of Go version (`go 1.26.0`); pinned via `actions/setup-go@v5` `go-version-file: go.mod` (identical pattern to the two existing workflows).
  - `.goreleaser.yml` — `version: 2`, `builds[0].main: .`, cross-builds linux/darwin/windows × amd64/arm64. The `build` job runs `goreleaser build --snapshot --clean` against it, which executes any `before.hooks` in that file. **Task 3 adds those hooks** (`cmd/genman` man-page generation + shell-completion generation), so the `build` job hard-depends on Task 3 having landed (`cmd/genman` must exist).
- Produces:
  - `.github/workflows/ci.yml` — a new, purely additive workflow with three jobs (`lint`, `test`, `build`) plus a commented-out `smoke` job documenting the deferred §6.6 integration smoke.

**Prerequisites / ordering (call out to implementer):**
- **Task 1 must land first** for the `lint` job to go green: `gofmt -l .` currently lists 8 files (`cmd/completion.go`, `cmd/init.go`, `cmd/sessions_list.go`, `internal/config/naming.go`, `internal/container/domains.go`, `internal/container/lifecycle.go`, `internal/sandbox/plugins.go`, `internal/sessions/state.go`). Task 1's housekeeping makes `gofmt -l .` empty; the gofmt gate below asserts exactly that.
- **Task 3 must land first** for the `build` job to go green: `goreleaser build --snapshot` runs the before-hooks Task 3 introduces; if `cmd/genman` is absent the hook step fails. If Task 4's `build` job is smoke-tested locally before Task 3 lands, expect a hook failure — that is the dependency, not a bug in this workflow.

---

#### Step 4.1 — Create `.github/workflows/ci.yml`

Write the file exactly as below. Style matches the existing workflows: `actions/checkout@v4`, `actions/setup-go@v5` with block-style `with: go-version-file: go.mod`, `goreleaser/goreleaser-action@v6` `version: "~> v2"`.

```yaml
name: CI

on:
  pull_request:
  push:
    branches: [ master ]
    tags: [ "v*" ]

permissions:
  contents: read

concurrency:
  group: ci-${{ github.ref }}
  cancel-in-progress: true

jobs:
  lint:
    runs-on: ubuntu-latest
    steps:
      - name: Checkout
        uses: actions/checkout@v4

      - name: Set up Go
        uses: actions/setup-go@v5
        with:
          go-version-file: go.mod

      - name: gofmt
        run: |
          out=$(gofmt -l .)
          if [ -n "$out" ]; then
            echo "The following files are not gofmt-formatted:"
            echo "$out"
            exit 1
          fi

      - name: go vet
        run: go vet ./...

  test:
    runs-on: ${{ matrix.os }}
    strategy:
      fail-fast: false
      matrix:
        # ubuntu is the required floor. macos-latest is included because the
        # full suite is green on darwin (go test ./... exits 0, verified
        # 2026-07-27; the older internal/sessions socket-path failure no longer
        # reproduces). windows is intentionally omitted: the TTY/resize code is
        # build-tagged (*_windows.go) and windows cross-compilation is already
        # exercised by the build job's `goreleaser build --snapshot`.
        os: [ ubuntu-latest, macos-latest ]
    steps:
      - name: Checkout
        uses: actions/checkout@v4

      - name: Set up Go
        uses: actions/setup-go@v5
        with:
          go-version-file: go.mod

      - name: go test
        run: go test ./... -count=1

  build:
    # Exercises the REAL .goreleaser.yml across all 6 os/arch targets, including
    # Task 3's before-hooks (cmd/genman man-page generation + shell completions).
    # DEPENDS ON Task 3: `goreleaser build` runs the before-hooks, so cmd/genman
    # must exist or this job fails at the hook step.
    runs-on: ubuntu-latest
    steps:
      - name: Checkout
        uses: actions/checkout@v4

      - name: Set up Go
        uses: actions/setup-go@v5
        with:
          go-version-file: go.mod

      - name: GoReleaser build (snapshot)
        uses: goreleaser/goreleaser-action@v6
        with:
          version: "~> v2"
          args: build --snapshot --clean

  # ---------------------------------------------------------------------------
  # DEFERRED (follow-up task): §6.6 integration smoke build (privileged Docker).
  # Not enabled here because it asserts the unlanded user-apt fix in
  # internal/container/generate.go and needs --cap-add=NET_ADMIN. GitHub
  # ubuntu-latest runners ship Docker and permit NET_ADMIN, so it IS runnable
  # in-CI once the fix lands. Keep it tag-only (slow + privileged). Shape:
  #
  # smoke:
  #   runs-on: ubuntu-latest
  #   if: github.ref_type == 'tag'
  #   steps:
  #     - uses: actions/checkout@v4
  #     - uses: actions/setup-go@v5
  #       with:
  #         go-version-file: go.mod
  #     - run: go build -o claude-bunker .
  #     # fixture .claude/.claude-bunker/config.json declares a USER apt package (e.g. jq)
  #     - run: ./claude-bunker --dump-dockerfile build-context
  #     - run: docker build -t bunker-smoke build-context
  #     - run: |
  #         docker run --rm --cap-add=NET_ADMIN --cap-add=NET_RAW bunker-smoke \
  #           bash -c 'command -v jq && /usr/local/bin/init-firewall.sh'
  #   # Asserts (a) the user apt package installed (guards the §6.6 apt-lists bug)
  #   # and (b) init-firewall.sh self-test passes (FirewallScriptPath =
  #   # /usr/local/bin/init-firewall.sh, per internal/container/constants.go).
  # ---------------------------------------------------------------------------
```

Design notes baked into the file (do not drop the comments — they encode decisions):
- **Trigger** `on: pull_request` + `push: {branches: [master], tags: ["v*"]}` covers spec §10's "PR AND tag" (and adds master pushes for post-merge coverage).
- **`concurrency`** cancels superseded runs per ref (`ci-${{ github.ref }}`, `cancel-in-progress: true`).
- **`permissions: contents: read`** — least privilege; this workflow neither writes releases nor pushes images (that stays in release.yml/base-image.yml).
- **`build`** uses `goreleaser build --snapshot --clean` — reuses the real `.goreleaser.yml` ldflags/targets rather than a hand-rolled `GOOS/GOARCH` loop, so every PR proves the release build still compiles on all 6 targets. `--snapshot` needs no tag history, so default (depth-1) checkout is fine.

#### Step 4.2 — Verify the workflow is well-formed

`actionlint` is not installed in this environment, so use the Python YAML parse (python3 + PyYAML are available):

```bash
python3 -c "import yaml,sys; yaml.safe_load(open('.github/workflows/ci.yml')); print('YAML OK')"
```

Expected output:
```
YAML OK
```

Note (do not be alarmed): the top-level key is the bare word `on:`, which is correct GitHub Actions syntax. If you later index the parsed dict as `d['on']` you'll get a `KeyError` because PyYAML (YAML 1.1) coerces the bare key `on` to the boolean `True` — the triggers live under `d[True]`. This is a PyYAML quirk only; GitHub Actions reads `on:` correctly. The `safe_load(...)` call above proves well-formedness without indexing, so it succeeds cleanly.

If `actionlint` happens to be available, also run it:
```bash
actionlint .github/workflows/ci.yml
```
Expected: no output, exit 0.

Sanity-check that the two existing workflows are untouched (must be zero diff):
```bash
git diff --name-only .github/workflows/release.yml .github/workflows/base-image.yml
```
Expected: no output (empty).

#### Step 4.3 — Commit

```bash
git add .github/workflows/ci.yml
git commit -m "ci: add test/lint/build workflow for PRs and tags"
```

Confirm only the new file is staged/committed and the two existing workflows are not in the commit:
```bash
git show --stat --oneline HEAD
```
Expected: a single changed file, `.github/workflows/ci.yml` (created).

---

### Task 5: Docs rewrite — README.md + CLAUDE.md

**Files:**
- Modify: `README.md` (near-total rewrite — replace all config/firewall/settings sections; keep Auth, Multiple projects, Agent teams, Env vars, Platform support)
- Modify: `CLAUDE.md` (surgical edits — Entry Flow, Two Config Systems, Package Layout, Fingerprinting, CLI Commands)

**Interfaces:** This is a docs-only task — no Go code changes, no new tests, no exported-symbol changes.
- Consumes (source of truth, must match verbatim):
  - `internal/container/domains.go` — `builtinDomains` (11 entries) + `sandboxExtraDomains` (`*.github.com`).
  - `internal/sandbox/seed.go` — `writeManagedSettings` (sandbox keys, `chmod 444`, `writableRoots`) and the `settings.json`/`settings.local.json` skip filter (lines 78-83).
  - `internal/devcontainer/devcontainer.go` — `DevContainer` struct (top-level keys) + `bunkerCustomizations` (extras); `internal/devcontainer/load.go` — `DevContainerPath` = `.devcontainer/devcontainer.json`; `internal/devcontainer/generate.go` — forced `capAdd`/`remoteUser`, `GeneratedMarker`, ghToken env-ref rule.
  - `internal/container/presets.go` — per-language `Domains` added by `init`.
  - `internal/container/lockfile.go` — `LockFilePath` = `.devcontainer/devcontainer-lock.json`.
  - `internal/config/fingerprint.go` — `CompareFingerprints(BuildInput, containerName) FingerprintResult`; unexported `imageFingerprint`/`containerFingerprint`.
  - `cmd/root.go` (CLI surface + bunker flags), `cmd/errors.go` (exit-code catalog 0/1/2/4), plus `cmd/prune.go`, `cmd/logs.go`, `cmd/doctor.go`, `cmd/status.go`, `cmd/init.go`, `cmd/sessions*.go`, `cmd/run.go:386` (claude exit-code passthrough).
- Produces: `README.md` + `CLAUDE.md` that make no FALSE claim about the config file, firewall allowlist, or settings injection.

---

#### Step 5.1 — Pre-flight fact re-verification (do this before editing)

Every fact below is verified against live source at draft time. Re-run these greps to confirm nothing drifted, then write only these facts:

```bash
# Config path is .devcontainer/devcontainer.json (NOT .claude/.claude-bunker/config.json on the live path)
grep -n "devcontainer.LoadProjectConfig" cmd/run.go cmd/init.go cmd/status.go
grep -rn "config.LoadProjectConfig\|config.ConfigPath" cmd/ internal/ | grep -v _test.go   # expect: NONE on live path (dead code only)

# Allowlist = 11 builtins + sandbox-only *.github.com, NO npm/pypi
sed -n '10,41p' internal/container/domains.go

# Settings: managed-settings.json chmod 444, writableRoots, and the deliberate skip of settings.json/settings.local.json
sed -n '105,148p' internal/sandbox/seed.go   # writeManagedSettings, mode "444", writableRoots ~/.cache
sed -n '76,84p'  internal/sandbox/seed.go    # skip filter for settings.json / settings.local.json

# CLI surface + bunker flags + exit codes
sed -n '75,101p' cmd/root.go                 # AddCommand list + root flag registrations
sed -n '9,28p'  cmd/errors.go                # exit-code catalog 0/1/2/4

# DNS: refresh daemon, 5-min default, launched at post-start via nohup
grep -n "INTERVAL" internal/container/scripts/refresh-firewall.sh   # INTERVAL="${2:-300}"  # 5 min
grep -n "refresh-firewall" internal/container/lifecycle.go          # nohup ... & at lifecycle.go:390
```

Confirmed facts to encode:
- **Config:** ONE file, `.devcontainer/devcontainer.json`. Standard devcontainer keys at top level: `features`, `containerEnv`, `onCreateCommand`, `postStartCommand`, `capAdd`, `remoteUser`, `image`, `name`, `securityOpt`. Bunker extras under `customizations["claude-bunker"]`: `exclude`, `allowDomains`, `apt`, `plugins`, `ghToken`, `seedHistory`, `workspace`. `init` forces `capAdd: [NET_ADMIN, NET_RAW]` and `remoteUser: claude-bunker`. Companion `.devcontainer/devcontainer-lock.json` pins feature digests; both should be committed.
- **Allowlist (firewall + sandbox):** `github.com`, `api.github.com`, `codeload.github.com`, `objects.githubusercontent.com`, `api.anthropic.com`, `sentry.io`, `statsig.anthropic.com`, `statsig.com`, `marketplace.visualstudio.com`, `vscode.blob.core.windows.net`, `update.code.visualstudio.com`. Sandbox-only wildcard: `*.github.com`. **NO npm/pypi by default** — those are added by `init` per language preset (`presets.go`).
- **Settings:** enforcement is a runtime read-only `/etc/claude-code/managed-settings.json` (`chmod 444`) written fresh on each fresh start; only the directory is created in the image (`base.dockerfile.tmpl:38`), the file is NOT baked. Host `settings.json` and `settings.local.json` are **deliberately NOT copied** into the container (they could weaken the sandbox). `.claude/commands/` and `.claude/agents/` ARE copied. Sandbox keys: `enabled:true`, `allowUnsandboxedCommands:false`, `enableWeakerNestedSandbox:true`, `writableRoots:["~/.cache"]` (`/home/claude-bunker/.cache`), `network.allowedDomains`.
- **DNS:** `refresh-firewall.sh` re-resolves + atomically swaps the ipset every ~5 min (`INTERVAL` default 300s), launched via `nohup … &` at post-start — CDN IP rotation is handled **without restart**.
- **Allowlist source of truth:** `internal/container/domains.go` (Go), NOT `init-firewall.sh`.
- **Exit codes:** `0` ok, `1` generic, `2` cancelled, `4` docker-unavailable; the default run path forwards `claude`'s own exit code verbatim (`cmd/run.go:386` `os.Exit(exitCode)`).

---

#### Step 5.2 — Rewrite README.md

Follow this 9-section outline. Sections marked **REPLACE** overwrite existing prose; keep everything not listed (Authentication, GitHub PAT, Multiple projects, Agent teams, Environment variables, Platform support, Security model, Troubleshooting except the DNS bullets, License).

**Section 1 — "Why" bullet fix (README L11).** Change the Network firewall bullet so it no longer lists npm as reachable-by-default:

> - **Network firewall** — default-deny iptables policy. By default only Anthropic's API, GitHub, Claude Code telemetry (Sentry/Statsig), and the VS Code Remote endpoints are reachable. Package-manager registries (npm, PyPI, crates.io, …) are **not** open by default — `claude-bunker init` adds them per language you select. Everything else is blocked.

**Section 2 — Install/Quick start.** Keep the Prerequisites + `go install` block. Add `init` as the recommended first step:

```bash
# Prerequisites: Docker + Go 1.26+
go install github.com/Devon-White/claude-bunker@latest

# Recommended: generate a project devcontainer (pick your languages)
cd ~/projects/my-app
claude-bunker init          # writes .devcontainer/devcontainer.json (+ devcontainer-lock.json)

# Run Claude in the sandbox
claude-bunker
```

Keep the passthrough examples (`--dangerously-skip-permissions`, `-p "…"`, `--model sonnet -p "…"`).

**Section 3 — REPLACE "Two config systems" (README L44-53) with "The devcontainer config".** Delete the two-file table entirely. New content:

> ## Configuration: `.devcontainer/devcontainer.json`
>
> claude-bunker is configured by a single, standard Dev Container file: `.devcontainer/devcontainer.json`. Standard devcontainer keys live at the top level; claude-bunker's own knobs live under `customizations["claude-bunker"]`. This keeps the file portable — it also opens in VS Code / Codespaces (bunker builds Claude Code natively and strips its own managed features on its build path).
>
> Run `claude-bunker init` to generate it. A generated file for a Node project looks like this:
>
> ```jsonc
> // GENERATED by claude-bunker — do not edit
> {
>   "capAdd": [
>     "NET_ADMIN",
>     "NET_RAW"
>   ],
>   "customizations": {
>     "claude-bunker": {
>       "allowDomains": [
>         "registry.npmjs.org"
>       ],
>       "apt": [
>         "ripgrep"
>       ],
>       "plugins": "project"
>     }
>   },
>   "features": {
>     "ghcr.io/anthropics/devcontainer-features/claude-code:1": {},
>     "ghcr.io/devcontainers/features/node:1": {
>       "version": "22"
>     }
>   },
>   "image": "mcr.microsoft.com/devcontainers/base:debian",
>   "name": "my-app (bunkered)",
>   "postStartCommand": "npm install",
>   "remoteUser": "claude-bunker"
> }
> ```
>
> **Standard devcontainer keys (top level):**
>
> | Key | Purpose |
> |-----|---------|
> | `features` | OCI [devcontainer features](https://containers.dev/features) (language runtimes, toolchains). Baked into the image. |
> | `containerEnv` | Environment variables injected at container creation. |
> | `onCreateCommand` | Command baked into the image as a build step (runs once at image build). |
> | `postStartCommand` | Command run after the container starts and the firewall is up (runs as `claude-bunker` in `/workspace`). |
> | `image` / `name` | Portable base image + label; used only by the VS Code / Codespaces path (bunker builds natively). |
> | `capAdd` / `remoteUser` | Forced by `init` to `NET_ADMIN`/`NET_RAW` and `claude-bunker` — the firewall requires these. |
>
> **claude-bunker extras (`customizations["claude-bunker"]`):**
>
> | Key | Purpose |
> |-----|---------|
> | `exclude` | Paths hidden inside the container via tmpfs overlays (genuinely invisible, not permission-blocked). |
> | `allowDomains` | Extra domains added to both the firewall and the sandbox allowlist (alongside the builtins). |
> | `apt` | Extra apt packages installed into the image. |
> | `plugins` | MCP/plugin seeding level: `project` (workspace `.mcp.json`), `user` (+ `~/.claude.json` and plugin cache), or `all` (+ enterprise `managed-mcp.json`). |
> | `ghToken` | GitHub token for git ops. **Only an env-reference is written** (`"${GH_TOKEN}"`); a literal token is refused and dropped so secrets never land in the committed file — pass literals via `--gh-token`/`$GH_TOKEN` instead. |
> | `seedHistory` | Whether to seed host session history into the container (default `true`). |
> | `workspace` | Subdirectory to start Claude in (full repo is still mounted at `/workspace`). |
>
> The `onCreateCommand`/`postStartCommand` values come from the repo, so a malicious devcontainer.json can run arbitrary commands (the standard devcontainer trust model — VS Code has the same issue). Review it before running claude-bunker on an untrusted repo; the firewall limits the blast radius.

**Section 4 — NEW subsection "Commit both files".**

> ### Commit `devcontainer.json` and `devcontainer-lock.json`
>
> Commit `.devcontainer/devcontainer.json` **and** `.devcontainer/devcontainer-lock.json`. The lockfile pins each feature to a resolved digest for reproducible builds; those digests feed the image fingerprint, so an upstream feature change under the same tag correctly invalidates the cache and triggers a rebuild.

**Section 5 — REPLACE "Claude Code settings" (README L201-278) with an accurate settings model.** Delete "Automatic sandbox injection" (L217-224) and the `settings.local.json` overwrite claim (L278) entirely. New content:

> ## Settings enforcement
>
> claude-bunker enforces sandbox settings through a single file it writes into the container on every fresh start: `/etc/claude-code/managed-settings.json`, mode `444` (read-only). It has the highest precedence in Claude Code and cannot be overridden — not by `settings.json`, not by `settings.local.json`, not by the `/sandbox` toggle. It is **written at runtime each start, not baked into the image** (the image only creates the `/etc/claude-code` directory).
>
> The enforced sandbox block is:
>
> ```json
> {
>   "sandbox": {
>     "enabled": true,
>     "allowUnsandboxedCommands": false,
>     "enableWeakerNestedSandbox": true,
>     "writableRoots": ["/home/claude-bunker/.cache"],
>     "network": { "allowedDomains": ["…builtins + *.github.com + your allowDomains…"] }
>   }
> }
> ```
>
> `enableWeakerNestedSandbox` is required because Docker containers lack `CAP_SYS_ADMIN`. `writableRoots` keeps `~/.cache` writable for tool caches while the rest of home stays sandboxed.
>
> **Your host `settings.json` / `settings.local.json` are deliberately NOT copied into the container.** If they were, they could redefine the `sandbox` key and weaken the enforcement — so they are skipped. The rest of your `.claude/` tree **is** copied: `.claude/commands/` and `.claude/agents/` work normally inside the sandbox. (The `.claude/.claude-bunker/` directory, a legacy location, is also skipped.)
>
> Because managed-settings.json is authoritative for the sandbox, put project rules that are *not* about the sandbox (permission allow/deny lists, hooks) wherever Claude Code normally reads them — those still apply.

Keep the permission allow/deny JSON examples that follow (they are still valid Claude Code settings), but remove any framing that claude-bunker injects them.

**Section 6 — REPLACE Commands + flags tables (README L55-79).**

> ## Commands
>
> | Command | Description |
> |---------|-------------|
> | `claude-bunker [flags]` | Run Claude in the sandbox. Unknown flags pass through to `claude`. |
> | `claude-bunker init` | Config wizard — writes `.devcontainer/devcontainer.json` (`--defaults`, `--dry-run`). |
> | `claude-bunker shell` | Open a bash shell in the sandbox (`--dry-run`). |
> | `claude-bunker status` | Show sandbox state (`--json`). |
> | `claude-bunker doctor` | Check Docker reachability, version, and devcontainer presence. |
> | `claude-bunker logs` | Container logs (`-f`/`--follow`, `--tail N`). |
> | `claude-bunker prune` | Remove Docker volumes **and images** (`--force`, `--all`, `--json`, `--dry-run`). |
> | `claude-bunker sessions` | Interactive TUI of all sandboxes/sessions/subagents (`--interval`). |
> | `claude-bunker sessions list` | List sessions (`--json`). |
> | `claude-bunker sessions stop <name>` | Stop a session (`-f`/`--force`, `--remove`, `--dry-run`). |
> | `claude-bunker sessions attach <name>` | Attach to a session (`--shell`, `--new`, `--resume`, `--keep`). |
> | `claude-bunker sessions logs <name>` | Session logs (`-f`/`--follow`, `--tail`). |
> | `claude-bunker completion <shell>` | Shell completion (bash/zsh/fish/powershell). |
> | `claude-bunker version` | Print version (`--json`). |
>
> ### claude-bunker flags (consumed by claude-bunker, not passed through)
>
> | Flag | Description |
> |------|-------------|
> | `--keep` | Keep the container running after exit. |
> | `--rebuild` | Force a clean image rebuild (clears cache). |
> | `--dry-run` | Plan build/create/launch without performing it. |
> | `--force` | Override fail-closed safety guards. |
> | `--no-sandbox` | Launch even if sandbox settings can't be seeded (**not recommended**). |
> | `--gh-token <token>` | GitHub token, injected via tmpfs. |
> | `--api-key <key>` | Anthropic API key, injected via tmpfs (never an env var). |
> | `--oauth-token <token>` | Claude Code OAuth token for headless/CI. |
> | `--verbose`, `-V` | Detailed startup output. |
> | `--quiet`, `-q` | Suppress informational output. |
> | `--no-color` | Disable ANSI color output. |
> | `--` | Passthrough terminator: everything after it goes to `claude` verbatim (e.g. `claude-bunker --keep -- --model opus -p "hi"`). |
>
> All other flags (`--dangerously-skip-permissions`, `-p`, `--model`, `--team`, …) pass through to the `claude` CLI.
>
> ### Exit codes
>
> | Code | Meaning |
> |------|---------|
> | `0` | Success. |
> | `1` | Generic/unclassified failure. |
> | `2` | Cancelled (interactive prompt aborted / confirmation refused). |
> | `4` | Docker unavailable (client couldn't reach the daemon). |
>
> Exit code `3` is intentionally unused: the default run path execs `claude` in the container and forwards **its** exit code verbatim rather than mapping it into this catalog.

**Section 7 — REPLACE the firewall/DNS content (README L126-155, L500, L508-509).** Fix the allowlist table (add `codeload.github.com`, `objects.githubusercontent.com`; remove npm/pypi rows), and replace "resolved once, restart to re-resolve" with the refresh daemon.

> ### Network firewall
>
> Default-deny with an allowlist. Reachable by default:
>
> | Destination | Why |
> |-------------|-----|
> | `api.anthropic.com` | Claude Code API |
> | `statsig.anthropic.com`, `statsig.com` | Telemetry |
> | `sentry.io` | Error reporting |
> | `github.com`, `api.github.com`, `codeload.github.com`, `objects.githubusercontent.com` | Git operations, `gh` CLI, release/blob downloads |
> | `marketplace.visualstudio.com`, `vscode.blob.core.windows.net`, `update.code.visualstudio.com` | VS Code Remote Containers support |
>
> The sandbox layer additionally permits the wildcard `*.github.com` (pattern-matched at request time; the firewall lists specific subdomains instead because wildcards can't be IP-resolved).
>
> **Package-manager registries are not open by default.** `claude-bunker init` adds them per language you select: Node → `registry.npmjs.org`; Python → `pypi.org`, `files.pythonhosted.org`; Go → `proxy.golang.org`, `sum.golang.org`, `storage.googleapis.com`; Rust → `crates.io`, `static.crates.io`, `index.crates.io`; and similar for Java/.NET/Ruby/PHP. Add your own with `allowDomains` under `customizations["claude-bunker"]`.
>
> All other outbound traffic is rejected (ICMP admin-prohibited). IPv6 is fully blocked to prevent bypass. On startup the firewall self-tests by confirming `example.com` is **blocked**; if that check fails, the container refuses to start.
>
> **IP rotation is handled automatically.** A background daemon (`refresh-firewall.sh`) re-resolves every allowed domain and atomically swaps the firewall ipset roughly every 5 minutes, so CDN-backed services that rotate IPs keep working **without restarting the container**. The sandbox layer (domain-name filtering) is a second line of defense that survives rotation regardless.

In "Known trade-offs" (L500), replace the "DNS resolution at startup" bullet with:

> - **Firewall filters by IP** — domains are resolved to IPs and refreshed every ~5 minutes by a background daemon. A brief window can exist right after a CDN rotates IPs and before the next refresh; the sandbox's domain-name filter covers that gap.

In Troubleshooting, delete "The firewall might have stale IPs. Exit Claude and run again" (L508-509) — the refresh daemon makes it obsolete. Replace with a note that if a domain still fails, it's likely not on the allowlist; add it via `allowDomains`.

Fix "Adding allowed domains → Globally" (L460): the allowlist lives in Go, not the shell script:

> **Globally (all projects):** the built-in allowlist is defined in `internal/container/domains.go` (`builtinDomains`). To change it for all projects, fork and edit that file — not `init-firewall.sh`, which only consumes the resolved list.

Also fix the dual-allowlist / `config.json` references (L146): say `allowDomains` (under `customizations["claude-bunker"]`) feeds both the iptables firewall and the sandbox filter.

**Section 8 — Fix "How it works" step 4 + rebuild detection (README L116, L124).**
- Step 4 (L116): replace with "claude-bunker writes the read-only `/etc/claude-code/managed-settings.json` with the sandbox config and firewall-matching domain allowlist, then copies your `.claude/commands` and `.claude/agents` in (but not `settings.json`/`settings.local.json`)."
- Rebuild detection (L124): "claude-bunker fingerprints `.devcontainer/devcontainer.json`, `.devcontainer/devcontainer-lock.json`, the generated Dockerfile, and embedded scripts. Editing any of them triggers an automatic rebuild or container recreation on the next run." Remove the `.claude-bunker/config.json` mention.

**Section 9 — Fix "Project structure" (README L520-543).**

```
claude-bunker/
  main.go                             # Entry point
  cmd/                                # CLI (root, run, shell, init, status, doctor, logs,
                                      #      prune, sessions*, completion, version)
  internal/
    config/                           # ProjectConfig, fingerprinting (CompareFingerprints), naming, expand
    devcontainer/                     # .devcontainer/devcontainer.json parse + generate + JSONC
    container/                        # Docker API: build, generate, lifecycle, exec, domains,
                                      #   lockfile, baseimage, volumes, features, presets, copy
      scripts/                        # init-firewall.sh, refresh-firewall.sh, firewall-common.sh,
                                      #   base.dockerfile.tmpl, tmux.conf
    buildlock/                        # Cross-process build lock
    sessions/                         # Session tree via `claude agents --json`
    sandbox/                          # managed-settings.json seeding, plugins, proxy
    log/                              # Logging helpers
    platform/                         # Platform-specific TTY / resize
  .goreleaser.yml
  .gitattributes
  .gitignore

# In your project (created by `claude-bunker init`):
your-project/
  .devcontainer/
    devcontainer.json                 # The config (commit this)
    devcontainer-lock.json            # Pinned feature digests (commit this)
```

Remove the `.claude/.claude-bunker/config.json` block entirely from the project-structure listing.

---

#### Step 5.3 — Surgical CLAUDE.md edits

Apply these exact replacements (source-verified).

1. **Entry Flow (L26, L28).** Replace `root command (`cmd/run.go`)` → `root command (`cmd/root.go`; run logic in `cmd/run.go`)`. Replace step 2:
   - Old: `2. Load project config from `.claude/.claude-bunker/config.json``
   - New: `2. Load project config from `.devcontainer/devcontainer.json` via `internal/devcontainer.LoadProjectConfig``

2. **Two Config Systems (L46-49).** Replace the whole block:
   - New:
     ```
     ### Config
     - **`.devcontainer/devcontainer.json`** — the single project config. Standard devcontainer keys at top level (features, containerEnv, onCreateCommand, postStartCommand, capAdd, remoteUser); bunker extras under `customizations["claude-bunker"]` (exclude, allowDomains, apt, plugins, ghToken, seedHistory, workspace). Parsed by `internal/devcontainer`.
     - **`.devcontainer/devcontainer-lock.json`** — pins feature digests (reproducible builds); digests fold into the image fingerprint.
     - Enforcement of Claude Code behavior is a runtime read-only `/etc/claude-code/managed-settings.json` (written each start); host `settings.json`/`settings.local.json` are NOT injected.
     ```
   - (Legacy `internal/config/project.go` `LoadProjectConfig`/`ConfigPath` still exist but have no live caller; note as dead code, optionally removable.)

3. **Package Layout table (L37-44).** Update rows:
   - `internal/config/` → add `features.go` to the description.
   - `internal/container/` → extend list: `lockfile.go`, `baseimage.go`, `volumes.go`, `features.go`, `presets.go`, `constants.go`, `copy.go`, `client.go`, `embed.go`.
   - `internal/container/scripts/` → add `tmux.conf`.
   - Add new rows:
     - `internal/devcontainer/` — parse/generate `.devcontainer/devcontainer.json` (JSONC + `${localEnv}`), map to `ProjectConfig`, strip bunker-managed features.
     - `internal/buildlock/` — cross-process build lock (unix/windows variants).
     - `internal/sessions/` — session/subagent tree via `claude agents --json` (store, watcher, manager).
     - `internal/log/` — logging helpers.

4. **Fingerprinting & Caching (L65).** Replace:
   - Old: ``ImageFingerprint()` hashes … `ContainerFingerprint()` hashes …`
   - New: `The public API is `CompareFingerprints(BuildInput, containerName) FingerprintResult` (unexported `imageFingerprint`/`containerFingerprint` do the hashing). The image fingerprint covers version + Dockerfile + scripts + apt + features + env + onCreateCommand **plus resolved feature digests from `devcontainer-lock.json`**; the container fingerprint covers domains + workspace + excludes + postStartCommand + plugins + seedHistory. Reproducible mod times (`2025-01-01T00:00:00Z`) keep Docker layer cache hits.`

5. **CLI Commands table (L78-86).** Add rows so it matches `cmd/root.go` AddCommand:
   - `claude-bunker sessions [list|stop|attach|logs]` — session manager (TUI + scripting subcommands)
   - `claude-bunker doctor` — environment readiness check (Docker/version/devcontainer)
   - `claude-bunker completion <shell>` — shell completion
   - `claude-bunker version` — print version (`--json`)
   - Keep existing: default run, shell, init, status, prune, logs, `--dump-dockerfile`.

Leave everything else in CLAUDE.md unchanged (Build & Test, Secrets, Conventions, Five Security Layers — its "managed-settings.json … injected to `/etc/claude-code/`" wording is accurate and should stay).

---

#### Step 5.4 — Verify (doc audit — no build impact, but confirm the tree still builds/formats)

```bash
# 1. No live reference to the retired config file (matches only inside code paths / dead-code notes are fine):
grep -n "\.claude/\.claude-bunker/config\.json" README.md CLAUDE.md
#    EXPECT: no line describing it as the *current/live* config. If it appears, it must be
#    explicitly labeled legacy/dead-code, never "place a … in your project".

# 2. No npm/pypi-by-default claim:
grep -niE "registry\.npmjs\.org|pypi\.org|npm.*reachable|only .*npm" README.md
#    EXPECT: matches ONLY in the per-language `init` context (Section 7), never as a default builtin.

# 3. Allowlist in README matches domains.go exactly (11 builtins + *.github.com):
grep -oE "codeload\.github\.com|objects\.githubusercontent\.com|update\.code\.visualstudio\.com|\*\.github\.com" README.md
#    EXPECT: all four present (these were missing before).

# 4. Exit codes + passthrough terminator documented:
grep -nE "^\| \`4\`|Docker unavailable|Passthrough terminator|`--` " README.md

# 5. Tree still builds/formats (docs shouldn't touch this, but confirm nothing was edited by accident):
gofmt -l .            # EXPECT: empty
go build ./...        # EXPECT: no output, exit 0
go vet ./...          # EXPECT: no output, exit 0
```

Then a human/second-model read confirms the acceptance criteria: no remaining reference to `.claude/.claude-bunker/config.json` as the live config, and no npm/pypi-by-default claim.

---

#### Step 5.5 — Commit

```bash
git add README.md CLAUDE.md
git commit -m "docs: rewrite README + CLAUDE.md for the devcontainer-based reality"
```

---
