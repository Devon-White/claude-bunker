# Phase 2d — Wire the Dev Container module into the live engine

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `.devcontainer/devcontainer.json` claude-bunker's config format end to end — `claude-bunker init` generates it, and the engine reads it — closing the three hardening gates from the Phase 2c review first. This is the visible payoff of "adopt Dev Containers officially": a bunkered repo carries a portable, VS-Code-openable `.devcontainer/` that also configures bunker.

**Architecture:** The `config` package cannot import `devcontainer` (that package imports `config` → cycle). So the read orchestration (`LoadProjectConfig`) lives in the `devcontainer` package, and the `cmd` layer (which imports both) calls it. Clean break: the live path reads `.devcontainer/devcontainer.json`, not the legacy `.claude/.claude-bunker/config.json`. Bunker-managed features (claude-code, and future firewall/hardening) are stripped from the engine's `ProjectConfig` on read — they live in the generated file only for the *portable* path (VS Code), because bunker's own base image already provides Claude Code natively.

**Tech Stack:** Go 1.26, stdlib. Table-driven tests with `t.Run`.

## Global Constraints

- Go 1.26+; single static binary; no new deps.
- **Clean break:** the live engine reads `.devcontainer/devcontainer.json`. The legacy `config.LoadProjectConfig`/`ConfigPath` (config.json) stay in the `config` package for now (still unit-tested) but are no longer called from the live path; their removal is a later cleanup.
- **Bunker-managed feature prefixes** (stripped from the engine's ProjectConfig on read; bunker provides them natively / applies them itself): `ghcr.io/anthropics/devcontainer-features/claude-code`, `ghcr.io/Devon-White/claude-bunker/firewall`, `ghcr.io/Devon-White/claude-bunker/hardening`.
- **Secret safety:** a literal-looking `ghToken` (not empty and not a `${VAR}`/`$VAR` reference) must NEVER be written into the committed `.devcontainer/devcontainer.json`. `Generate` omits it and warns.
- Standard paths: read/write `<workspace>/.devcontainer/devcontainer.json`. The generated file's `image` is a portable base (`mcr.microsoft.com/devcontainers/base:debian`) and includes the claude-code feature — both for the VS Code path; bunker's own build ignores the image and strips the feature.
- Fail-closed posture (Phase 0) is preserved: a malformed devcontainer.json fails closed unless `--force`.
- Run `go build ./...` and `go test ./...`; both stay green. Commit after each task.

---

## File Structure

- `internal/devcontainer/generate.go` — **modify.** `Generate` gains the literal-`ghToken` guard.
- `internal/devcontainer/load.go` — **new.** `LoadProjectConfig(workspace) (config.ProjectConfig, bool, error)` — find/parse/map/merge + strip bunker-managed features. `stripBunkerFeatures`, `isEnvRef` helpers.
- `internal/devcontainer/load_test.go`, `internal/devcontainer/devcontainer_test.go`, `internal/devcontainer/generate_test.go` — **new/modify.** Gate (c) edge-case tests + gate (a)/(b) tests.
- `cmd/run.go` — **modify.** `runner.loadConfig` reads via `devcontainer.LoadProjectConfig`.
- `cmd/status.go` — **modify.** Reads via `devcontainer.LoadProjectConfig`.
- `cmd/init.go` — **modify.** Pre-populate from `devcontainer.LoadProjectConfig`; write `.devcontainer/devcontainer.json` via `devcontainer.Generate`.

---

## Task 1: Gate (a) `ghToken` secret guard + Gate (c) edge-case tests

**Files:**
- Modify: `internal/devcontainer/generate.go`
- Modify: `internal/devcontainer/generate_test.go`, `internal/devcontainer/devcontainer_test.go`

**Interfaces:**
- Produces: `func isEnvRef(s string) bool` (true if `s` contains `${` or starts with `$`). `Generate` omits `ghToken` from the output when it is non-empty and NOT an env reference, logging a warning via `internal/log`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/devcontainer/generate_test.go`:

```go
func TestGenerate_OmitsLiteralGhToken(t *testing.T) {
	cfg := config.ProjectConfig{GhToken: "ghp_literalSecret123"}
	data, err := Generate(cfg, GenerateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "ghp_literalSecret123") {
		t.Error("a literal ghToken must NOT be written to the committed devcontainer.json")
	}
}

func TestGenerate_KeepsEnvRefGhToken(t *testing.T) {
	cfg := config.ProjectConfig{GhToken: "${GH_TOKEN}"}
	data, err := Generate(cfg, GenerateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "${GH_TOKEN}") {
		t.Error("a ${VAR} ghToken reference should be preserved")
	}
}
```

Add to `internal/devcontainer/devcontainer_test.go` (gate c — the edge cases the 2c reviewer verified manually):

```go
func TestParse_Malformed(t *testing.T) {
	if _, err := Parse([]byte("{not valid json"), nil); err == nil {
		t.Error("malformed devcontainer.json must return an error")
	}
}

func TestToProjectConfig_MissingCustomizations(t *testing.T) {
	dc, err := Parse([]byte(`{"image":"x"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := ToProjectConfig(dc) // must not panic; extras are zero-value
	if cfg.Exclude != nil || cfg.Plugins != "" {
		t.Errorf("missing customizations should yield zero extras: %+v", cfg)
	}
}

func TestCommandToString_ObjectAndNull(t *testing.T) {
	if got := commandToString([]byte(`{"a":"x"}`)); got != "" {
		t.Errorf("object command → empty, got %q", got)
	}
	if got := commandToString([]byte(`null`)); got != "" {
		t.Errorf("null command → empty, got %q", got)
	}
	if got := commandToString(nil); got != "" {
		t.Errorf("nil command → empty, got %q", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/devcontainer/ -run 'TestGenerate_OmitsLiteralGhToken|TestGenerate_KeepsEnvRefGhToken|TestParse_Malformed|TestToProjectConfig_MissingCustomizations|TestCommandToString_ObjectAndNull' -v`
Expected: `TestGenerate_OmitsLiteralGhToken` FAILS (literal token currently written). The others likely PASS already (they document existing behavior) — that is fine; if any fails, it reveals a real gap to fix. `isEnvRef` is undefined until Step 3.

- [ ] **Step 3: Add the guard**

In `internal/devcontainer/generate.go`, add the import `bunkerlog "github.com/Devon-White/claude-bunker/internal/log"` and the helper:

```go
// isEnvRef reports whether s is an environment-variable reference (${VAR} or $VAR)
// rather than a literal value. Literal secrets must not be written to the
// committed devcontainer.json.
func isEnvRef(s string) bool {
	return strings.Contains(s, "${") || strings.HasPrefix(s, "$")
}
```

In `Generate`, replace the `ghToken` emission (`if cfg.GhToken != "" { bc["ghToken"] = cfg.GhToken }`) with:

```go
	if cfg.GhToken != "" {
		if isEnvRef(cfg.GhToken) {
			bc["ghToken"] = cfg.GhToken
		} else {
			bunkerlog.Warn("ghToken looks like a literal secret; omitting it from devcontainer.json. Pass it via --gh-token or the $GH_TOKEN env var, or use a \"${GH_TOKEN}\" reference.")
		}
	}
```

Add `"strings"` to the imports if not present.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/devcontainer/ -v` then `go build ./...`
Expected: all PASS; build clean.

- [ ] **Step 5: Commit**

```bash
git add internal/devcontainer/generate.go internal/devcontainer/generate_test.go internal/devcontainer/devcontainer_test.go
git commit -m "fix(devcontainer): never write a literal ghToken to devcontainer.json; add edge-case tests"
```

---

## Task 2: Gate (b) feature provenance + `LoadProjectConfig` read orchestration

**Files:**
- Create: `internal/devcontainer/load.go`
- Create: `internal/devcontainer/load_test.go`

**Interfaces:**
- Produces: `func LoadProjectConfig(workspace string) (config.ProjectConfig, bool, error)` — reads `<workspace>/.devcontainer/devcontainer.json`; returns `(zeroConfig, false, nil)` when absent; parses (localEnv via `os.LookupEnv`), maps via `ToProjectConfig`, applies `Merge`-equivalent forcing for user-authored files, and strips bunker-managed features from `cfg.Features`. `func stripBunkerFeatures(map[string]map[string]interface{}) map[string]map[string]interface{}` (pure).

- [ ] **Step 1: Write the failing test**

Create `internal/devcontainer/load_test.go`:

```go
package devcontainer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStripBunkerFeatures(t *testing.T) {
	in := map[string]map[string]interface{}{
		"ghcr.io/anthropics/devcontainer-features/claude-code:1": {},
		"ghcr.io/Devon-White/claude-bunker/firewall:1":           {},
		"ghcr.io/devcontainers/features/node:1":                  {"version": "20"},
	}
	got := stripBunkerFeatures(in)
	if _, ok := got["ghcr.io/anthropics/devcontainer-features/claude-code:1"]; ok {
		t.Error("claude-code (bunker-managed) must be stripped")
	}
	if _, ok := got["ghcr.io/Devon-White/claude-bunker/firewall:1"]; ok {
		t.Error("firewall (bunker-managed) must be stripped")
	}
	if _, ok := got["ghcr.io/devcontainers/features/node:1"]; !ok {
		t.Error("user feature must survive")
	}
}

func TestLoadProjectConfig(t *testing.T) {
	// Absent file → not found, no error.
	ws := t.TempDir()
	_, found, err := LoadProjectConfig(ws)
	if err != nil || found {
		t.Fatalf("absent devcontainer.json: found=%v err=%v", found, err)
	}

	// Present file → parsed, mapped, bunker features stripped, forced fields applied.
	dir := filepath.Join(ws, ".devcontainer")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{
  "features": {
    "ghcr.io/anthropics/devcontainer-features/claude-code:1": {},
    "ghcr.io/devcontainers/features/node:1": {"version": "20"}
  },
  "customizations": { "claude-bunker": { "exclude": ["secrets/"], "plugins": "project" } }
}`
	if err := os.WriteFile(filepath.Join(dir, "devcontainer-lock.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err) // unrelated file present; must be ignored
	}
	if err := os.WriteFile(filepath.Join(dir, "devcontainer.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, found, err := LoadProjectConfig(ws)
	if err != nil || !found {
		t.Fatalf("present devcontainer.json: found=%v err=%v", found, err)
	}
	if _, ok := cfg.Features["ghcr.io/anthropics/devcontainer-features/claude-code:1"]; ok {
		t.Error("claude-code feature must be stripped from the engine config")
	}
	if _, ok := cfg.Features["ghcr.io/devcontainers/features/node:1"]; !ok {
		t.Error("user feature must be present")
	}
	if len(cfg.Exclude) != 1 || cfg.Exclude[0] != "secrets/" {
		t.Errorf("exclude not mapped: %+v", cfg.Exclude)
	}
	if cfg.Plugins != "project" {
		t.Errorf("plugins = %q", cfg.Plugins)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/devcontainer/ -run 'TestStripBunkerFeatures|TestLoadProjectConfig' -v`
Expected: FAIL — `stripBunkerFeatures`, `LoadProjectConfig` undefined.

- [ ] **Step 3: Write the implementation**

Create `internal/devcontainer/load.go`:

```go
package devcontainer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Devon-White/claude-bunker/internal/config"
)

// bunkerManagedFeaturePrefixes are OCI feature refs bunker provides itself
// (native install / its own engine) and therefore strips from the engine's
// ProjectConfig on read. They remain in the file only for the portable
// (VS Code / Codespaces) build.
var bunkerManagedFeaturePrefixes = []string{
	"ghcr.io/anthropics/devcontainer-features/claude-code",
	"ghcr.io/Devon-White/claude-bunker/firewall",
	"ghcr.io/Devon-White/claude-bunker/hardening",
}

// DevContainerPath returns the standard devcontainer.json path for a workspace.
func DevContainerPath(workspace string) string {
	return filepath.Join(workspace, ".devcontainer", "devcontainer.json")
}

// stripBunkerFeatures removes bunker-managed features from a features map,
// returning a new map. User features are preserved.
func stripBunkerFeatures(features map[string]map[string]interface{}) map[string]map[string]interface{} {
	out := make(map[string]map[string]interface{}, len(features))
	for ref, opts := range features {
		managed := false
		for _, p := range bunkerManagedFeaturePrefixes {
			if strings.HasPrefix(ref, p) {
				managed = true
				break
			}
		}
		if !managed {
			out[ref] = opts
		}
	}
	return out
}

// LoadProjectConfig reads <workspace>/.devcontainer/devcontainer.json and maps
// it to the engine's ProjectConfig. Returns (zeroConfig, false, nil) when the
// file is absent. For a user-authored file (no GENERATED marker) it still forces
// bunker's security fields; either way it strips bunker-managed features (which
// bunker provides natively) from the engine config.
func LoadProjectConfig(workspace string) (config.ProjectConfig, bool, error) {
	data, err := os.ReadFile(DevContainerPath(workspace))
	if err != nil {
		if os.IsNotExist(err) {
			return config.ProjectConfig{}, false, nil
		}
		return config.ProjectConfig{}, false, err
	}

	dc, err := Parse(data, func(name string) (string, bool) { return os.LookupEnv(name) })
	if err != nil {
		return config.ProjectConfig{}, true, fmt.Errorf("reading %s: %w", DevContainerPath(workspace), err)
	}
	if !IsBunkerGenerated(data) {
		dc = Merge(dc) // user-authored: force security fields into the in-memory spec
	}

	cfg := ToProjectConfig(dc)
	cfg.Features = stripBunkerFeatures(cfg.Features)
	return cfg, true, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/devcontainer/ -run 'TestStripBunkerFeatures|TestLoadProjectConfig' -v` then `go build ./...` then `go test ./...`
Expected: PASS; build clean; full suite green.

- [ ] **Step 5: Commit**

```bash
git add internal/devcontainer/load.go internal/devcontainer/load_test.go
git commit -m "feat(devcontainer): LoadProjectConfig reads devcontainer.json; strip bunker-managed features"
```

---

## Task 3: Wire the READ path (run.go, status.go, init.go pre-populate)

**Files:**
- Modify: `cmd/run.go` (`runner.loadConfig`, ~line 306)
- Modify: `cmd/status.go` (~line 98)
- Modify: `cmd/init.go` (~lines 60-66, wizard pre-populate)

**Interfaces:**
- Consumes: `devcontainer.LoadProjectConfig` (Task 2). The `cmd` package imports `internal/devcontainer`.

**Context:** These three sites call `config.LoadProjectConfig` (reads config.json). Switch them to `devcontainer.LoadProjectConfig` (reads devcontainer.json). Preserve the Phase 0 fail-closed handling in `run.go`.

- [ ] **Step 1: Update `runner.loadConfig` (cmd/run.go)**

Add `"github.com/Devon-White/claude-bunker/internal/devcontainer"` to the imports. Replace the first two lines of `loadConfig`:

```go
func (r *runner) loadConfig(flags bunkerFlags) {
	cfg, _, err := devcontainer.LoadProjectConfig(r.workspace)
	if fatal := failClosed(err, flags.force, "Fix .devcontainer/devcontainer.json, or re-run with --force to ignore it."); fatal != nil {
		die("Failed to parse devcontainer.json: " + fatal.Error())
	}
	if err != nil {
		warn("Continuing despite devcontainer.json error (--force): " + err.Error())
	}
	r.projectCfg = cfg
	// ... rest unchanged (auth resolution, proxy detect)
```

- [ ] **Step 2: Update status.go**

In `cmd/status.go` (~line 98), change `cfg, cfgErr := config.LoadProjectConfig(workspace)` to:

```go
	cfg, _, cfgErr := devcontainer.LoadProjectConfig(workspace)
```

Add the `devcontainer` import; drop the `config` import if it becomes unused (check — status.go may use other config funcs).

- [ ] **Step 3: Update init.go pre-populate**

In `cmd/init.go` `runInit` (~lines 60-66), the wizard pre-populates from the existing config. Change the existence check + load to devcontainer:

```go
	cfgPath := devcontainer.DevContainerPath(workspace)

	var existing *config.ProjectConfig
	if _, err := os.Stat(cfgPath); err == nil {
		if loaded, _, err := devcontainer.LoadProjectConfig(workspace); err == nil {
			existing = &loaded
			info("Updating existing devcontainer: " + cfgPath)
		}
	}
```

Add the `devcontainer` import. (The `writeConfig`/generation change is Task 4 — this task only changes the READ/pre-populate + the `cfgPath` variable to the devcontainer path.)

- [ ] **Step 4: Build + full suite**

Run: `go build ./...` then `go test ./...`
Expected: build clean; full suite green. (The `config.LoadProjectConfig`/`ConfigPath` functions remain, still covered by `internal/config` tests; they're just no longer called from the live path. Any `cmd` test that relied on reading config.json for the live path must be updated to write a devcontainer.json instead — fix such tests here.)

- [ ] **Step 5: Commit**

```bash
git add cmd/run.go cmd/status.go cmd/init.go
git commit -m "feat(cmd): read .devcontainer/devcontainer.json as the config source (clean break from config.json)"
```

---

## Task 4: Wire the WRITE path — `init` generates `.devcontainer/devcontainer.json`

**Files:**
- Modify: `cmd/init.go` (`runInit` write path)

**Interfaces:**
- Consumes: `devcontainer.Generate`, `devcontainer.DevContainerPath`. Produces: `func writeDevContainer(workspace string, cfg config.ProjectConfig) error`.

**Context:** `runInit` currently builds a `map[string]interface{}` (via `buildConfig`+`mergeSettings`) and writes it as `config.json` via `writeConfig(cfgPath, cfg)`. Switch to: convert that map to a `config.ProjectConfig`, then `Generate` a `.devcontainer/devcontainer.json`.

- [ ] **Step 1: Write the failing test**

Add to `cmd/init_test.go`:

```go
func TestWriteDevContainer(t *testing.T) {
	ws := t.TempDir()
	seed := false
	cfg := config.ProjectConfig{
		Features:    map[string]map[string]interface{}{"ghcr.io/devcontainers/features/node:1": {"version": "20"}},
		Exclude:     []string{"secrets/"},
		SeedHistory: &seed,
	}
	if err := writeDevContainer(ws, cfg); err != nil {
		t.Fatalf("writeDevContainer: %v", err)
	}
	p := filepath.Join(ws, ".devcontainer", "devcontainer.json")
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("generated file missing: %v", err)
	}
	if !bytes.HasPrefix(bytes.TrimLeft(data, "\n"), []byte("// GENERATED by claude-bunker")) {
		t.Error("generated file must start with the GENERATED marker")
	}
	// Round-trips back to the engine config with the user feature (claude-code stripped).
	loaded, found, err := devcontainer.LoadProjectConfig(ws)
	if err != nil || !found {
		t.Fatalf("round-trip load: found=%v err=%v", found, err)
	}
	if _, ok := loaded.Features["ghcr.io/devcontainers/features/node:1"]; !ok {
		t.Error("user feature lost in generate→load round-trip")
	}
	if len(loaded.Exclude) != 1 {
		t.Errorf("exclude lost: %+v", loaded.Exclude)
	}
}
```

Add imports `bytes`, `path/filepath`, `os`, the config + devcontainer packages to `cmd/init_test.go` as needed.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/ -run TestWriteDevContainer -v`
Expected: FAIL — `writeDevContainer` undefined.

- [ ] **Step 3: Implement `writeDevContainer` and route init through it**

Add to `cmd/init.go`:

```go
// writeDevContainer generates .devcontainer/devcontainer.json from the wizard's
// config and writes it. The generated file references the claude-code feature
// and a portable base image for the VS Code path; bunker's own build ignores
// the image and strips the feature (it installs Claude Code natively).
func writeDevContainer(workspace string, cfg config.ProjectConfig) error {
	name := filepath.Base(workspace) + " (bunkered)"
	data, err := devcontainer.Generate(cfg, devcontainer.GenerateOpts{
		Name:              name,
		Image:             "mcr.microsoft.com/devcontainers/base:debian",
		ClaudeCodeFeature: "ghcr.io/anthropics/devcontainer-features/claude-code:1",
	})
	if err != nil {
		return fmt.Errorf("generating devcontainer.json: %w", err)
	}
	p := devcontainer.DevContainerPath(workspace)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		return err
	}
	success("Saved " + p)
	return nil
}

// mapToProjectConfig converts the wizard's config map to a ProjectConfig via JSON
// (the map keys are ProjectConfig's json tags).
func mapToProjectConfig(m map[string]interface{}) (config.ProjectConfig, error) {
	var pc config.ProjectConfig
	if len(m) == 0 {
		return pc, nil
	}
	data, err := json.Marshal(m)
	if err != nil {
		return pc, err
	}
	err = json.Unmarshal(data, &pc)
	return pc, err
}
```

Then in `runInit`, replace the final `return writeConfig(cfgPath, cfg)` (and the `--defaults` non-TTY `writeConfig(cfgPath, nil)`) with the devcontainer write:

```go
	// (success path, after buildConfig+mergeSettings)
	pc, err := mapToProjectConfig(cfg)
	if err != nil {
		return fmt.Errorf("building config: %w", err)
	}
	return writeDevContainer(workspace, pc)
```

For the `--defaults` non-TTY branch (which wrote `writeConfig(cfgPath, nil)`), write a minimal generated devcontainer.json: `return writeDevContainer(workspace, config.ProjectConfig{})`. Keep the abort branches (`abortErr`) unchanged — they still must not write anything.

(`writeConfig` and `config.ConfigPath` may now be unused in init.go — remove `writeConfig` if nothing references it, and keep the abort/`--defaults`/TTY logic intact. Ensure `encoding/json` stays imported.)

- [ ] **Step 4: Run tests + build**

Run: `go test ./cmd/ -run 'TestWriteDevContainer|TestRunInit|TestNonInteractiveInit|TestAbortErr' -v` then `go build ./...` then `go test ./...`
Expected: PASS; build clean; full suite green.

- [ ] **Step 5: Commit**

```bash
git add cmd/init.go cmd/init_test.go
git commit -m "feat(init): generate .devcontainer/devcontainer.json (portable, VS-Code-openable)"
```

---

## Self-review notes (coverage)

- Gate (a) literal-ghToken guard → Task 1. Gate (c) edge-case tests → Task 1. Gate (b) provenance strip → Task 2.
- Read path (clean break) → Task 3. Write path (init generates) → Task 4.
- Import cycle avoided: read orchestration lives in `devcontainer` (imports `config`); `cmd` calls it.

## Follow-on (not in this plan)

- Remove the now-unused legacy `config.LoadProjectConfig`/`ConfigPath`/config.json plumbing + its tests (cleanup).
- `allowDomains` → firewall Feature option once that Feature is published (Phase 2b-portability).
- `onCreateCommand` → in-container post-create lifecycle stage (needs reuse marker).
- README/docs: the new `.devcontainer/` flow, and "commit `devcontainer.json` + `devcontainer-lock.json`" (Phase 4).
