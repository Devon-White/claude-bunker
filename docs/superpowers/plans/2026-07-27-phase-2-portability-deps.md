# Phase 2 (Portability) — Deps as Standard Features + Lock Merge + Cleanup

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Make dependency handling portable and stop inventing bunker-isms — express extra OS packages via the STANDARD community `apt-packages` Dev Container Feature instead of the bunker-specific `customizations["claude-bunker"].apt` field; fix the `devcontainer-lock.json` wholesale-overwrite that drops the claude-code entry VS Code needs; remove dead legacy config code; and update the docs.

**Architecture:** The `apt-packages` feature (`ghcr.io/rocker-org/devcontainer-features/apt-packages`) is a normal user feature — bunker already resolves any non-bunker-managed feature (`stripBunkerFeatures` preserves user features; `ResolveFeatures` installs them) and VS Code/Codespaces honor it too. So dependencies become a single portable mechanism with no separate bunker code path. The lock write is changed to merge-preserve rather than overwrite.

**Tech Stack:** Go 1.26.0. No new deps. The apt-packages feature is fetched host-side at build (like the claude-code feature already is).

## Global Constraints

- Go 1.26.0; keep `go build ./...`, `go vet ./...`, `go test ./...` green and `gofmt -l .` empty after each task.
- **Breaking config change is allowed** (user confirmed — no external users). The bunker `apt` field is removed, not kept for compatibility; a deprecation warning on read is a courtesy, not a compat shim.
- **Deps portability rule:** OS packages are expressed as the standard `apt-packages` feature in the top-level `features` map (portable to VS Code/Codespaces). NEVER via `onCreateCommand`/`postCreateCommand` (can't be layer-cached; re-runs every build).
- The apt-packages feature ref is pinned: `ghcr.io/rocker-org/devcontainer-features/apt-packages:1`; its option is `{"packages": "<comma-separated>"}`.
- The feature must NOT be added to `bunkerManagedFeaturePrefixes` (it's a user feature — bunker resolves + installs it, and it stays in the committed file for VS Code).
- Removing `apt` also removes bunker's own Dockerfile apt-install layer + `validAptPkg` injection guard — correct, because the feature owns its install and validation.

## Task Order & Dependencies

1. **Task 1 — Replace the `apt` field with the standard apt-packages feature.** The meaty one: rewire the `init` wizard to emit the feature; remove `ProjectConfig.Apt` and every handler (config/fingerprint/expand, devcontainer parse+generate, status, the container Dockerfile apt layer). Touches ~8 files.
2. **Task 2 — `devcontainer-lock.json` merge fix.** `ResolveFeatures`' lock write must merge newly-resolved digests into the existing lock, preserving entries for features not resolved this run (bunker-managed/stripped features present in the committed devcontainer.json), instead of overwriting. Independent of Task 1.
3. **Task 3 — Remove dead legacy config code.** Delete `config.LoadProjectConfig` + `config.ConfigPath` from `internal/config/project.go` (no live callers; the live path uses `devcontainer.LoadProjectConfig`). Keep the `ProjectConfig` type, `NormalizeDomains`, `PluginLevel`, etc. Touches project.go (after Task 1's edits to the same file).
4. **Task 4 — Docs.** README + CLAUDE.md: document "add OS packages via the apt-packages feature", remove references to the bunker `apt` field, never lifecycle commands for deps.

---

### Task 1: Replace the `apt` field with the standard apt-packages feature

**Files:**
- Modify: `cmd/init.go` — the wizard writes an `apt-packages` feature entry into `features` instead of `cfg["apt"]`; read-back pre-populates from that feature entry.
- Modify: `internal/config/project.go` — remove the `Apt []string` field; update `HasGeneratedLayers()`.
- Modify: `internal/config/fingerprint.go` — remove the apt hash (line ~48-49); apt packages now hash via the feature digest already folded in.
- Modify: `internal/config/expand.go` — remove the `cfg.Apt` env-expansion loop (line ~116-117) and the doc comment mention.
- Modify: `internal/devcontainer/devcontainer.go` — remove `Apt` from `bunkerCustomizations` (line ~39) and its mapping (line ~86).
- Modify: `internal/devcontainer/generate.go` — remove writing `apt` into the customizations block (line ~72-73).
- Modify: `cmd/status.go` — remove apt from `printResolvedConfig` (line ~179, 188-189).
- Modify: `internal/container/generate.go` — remove the apt-install Dockerfile layer + `validAptPkg` (line ~12, 31, 48-58).
- Modify: `internal/config/features.go` — fix the message mentioning `"apt" field` (line ~16) to point at the feature.
- Test: `cmd/init_test.go` (or the existing init/generate tests) — assert the wizard emits the feature entry; `internal/devcontainer/*_test.go` — assert no `apt` in generated customizations.

**Interfaces:**
- Consumes: the existing `init` wizard state (`s.aptPackages`), the generated `features` map, `ResolveFeatures`, `stripBunkerFeatures`.
- Produces: generated devcontainer.json where extra apt packages appear as `"features": { "ghcr.io/rocker-org/devcontainer-features/apt-packages:1": { "packages": "a,b" } }`; `ProjectConfig.Apt` no longer exists.

- [ ] **Step 1: Write the failing test** — assert init writes the feature.

Add/extend an init test (mirror how existing init tests assert generated config). Example shape (adapt to the actual init test harness — read `cmd/init_test.go` first):

```go
func TestInit_AptPackagesBecomeStandardFeature(t *testing.T) {
	// Given the wizard collected two apt packages, the generated config must
	// express them as the standard apt-packages FEATURE (portable), not a
	// bunker-specific "apt" field.
	s := &initState{aptPackages: "jq ripgrep"} // match the real field/type
	cfg := buildGeneratedConfig(s)             // the function that assembles the map (find the real one)

	feats, _ := cfg["features"].(map[string]any)
	entry, ok := feats["ghcr.io/rocker-org/devcontainer-features/apt-packages:1"]
	if !ok {
		t.Fatalf("expected apt-packages feature entry; features=%v", feats)
	}
	opts, _ := entry.(map[string]any)
	if got := opts["packages"]; got != "jq,ripgrep" {
		t.Errorf("packages option = %v, want \"jq,ripgrep\"", got)
	}
	// And no bunker-specific apt field:
	if _, exists := cfg["apt"]; exists {
		t.Error("generated config must not contain a bunker \"apt\" field")
	}
}
```

(Read `cmd/init.go` to find the real state field name, the config-assembly function, and how `cfg["apt"] = pkgs` is currently produced at line ~515, and how packages are split — the wizard takes space-separated input; the feature wants comma-separated.)

- [ ] **Step 2: Run test to verify it fails** — `go test ./cmd/ -run TestInit_AptPackagesBecomeStandardFeature -v` → FAIL (still writes `apt`).

- [ ] **Step 3: Rewire the init wizard.** In `cmd/init.go`, where it currently does `cfg["apt"] = pkgs` (~line 515): instead, if the user entered packages, add the feature entry to the generated `features` map:

```go
if pkgs := strings.Fields(s.aptPackages); len(pkgs) > 0 {
	feats, _ := cfg["features"].(map[string]any)
	if feats == nil {
		feats = map[string]any{}
	}
	feats["ghcr.io/rocker-org/devcontainer-features/apt-packages:1"] = map[string]any{
		"packages": strings.Join(pkgs, ","),
	}
	cfg["features"] = feats
}
```
Update the read-back/pre-populate path (~line 329-331) to read packages from that feature entry (if present in the loaded config's `features`) rather than from `existing.Apt`. Update the wizard copy (~line 371, 410) if it references a bunker "apt" concept — keep the prompt "Extra apt packages" (still accurate) but the label at ~371 can stay user-facing.

- [ ] **Step 4: Remove `ProjectConfig.Apt` and all handlers.**
  - `internal/config/project.go`: delete the `Apt []string` field (line 22); in `HasGeneratedLayers()` (line 72) drop `len(c.Apt) > 0` (features now cover it: `len(c.Features) > 0 || len(c.Env) > 0 || c.OnCreateCommand != ""`).
  - `internal/config/fingerprint.go`: delete the apt hash lines (48-49) and the comment mention (25).
  - `internal/config/expand.go`: delete the `cfg.Apt` loop (116-117) and the comment mention (95).
  - `internal/devcontainer/devcontainer.go`: delete `Apt []string` from `bunkerCustomizations` (39) and `Apt: bc.Apt` (86).
  - `internal/devcontainer/generate.go`: delete the `if len(cfg.Apt) > 0 { bc["apt"] = cfg.Apt }` block (72-73).
  - `cmd/status.go`: remove `len(cfg.Apt) > 0` from the hasConfig check (179) and the apt configLine (188-189).
  - `internal/container/generate.go`: delete the apt-install layer (48-58) and the now-unused `validAptPkg` regex (12) and the comment (31) referencing apt.
  - `internal/config/features.go`: change the message at line 16 from "or use the \"apt\" field for plain apt packages" to reference the apt-packages feature.

- [ ] **Step 5: (courtesy) Deprecation warning on read.** In `internal/devcontainer` where `customizations["claude-bunker"]` is parsed, if a legacy `apt` key is still present in the raw customizations, emit a one-line warn (via the existing warn mechanism or a returned notice) like: `warn: the "apt" field is deprecated; add packages via the apt-packages feature and re-run 'claude-bunker init'`. This prevents silent loss for pre-existing repos. Keep it lightweight; if there's no clean warn seam in the devcontainer package, skip and note it (the package must stay import-clean — don't pull in cmd/ui).

- [ ] **Step 6: Run tests + build.** `go build ./...`; `go vet ./...`; `go test ./...` green; `gofmt -l .` empty. Grep to confirm no stray `cfg.Apt`/`.Apt`/`"apt"` field references remain: `grep -rn "\.Apt\b\|cfg\[\"apt\"\]\|bc\[\"apt\"\]" --include=*.go .` returns nothing (test files included).

- [ ] **Step 7: Manual smoke.** `go build -o /tmp/cbi . && CLAUDE_BUNKER_WS=$(mktemp -d) /tmp/cbi init --defaults` then inspect the generated `.devcontainer/devcontainer.json` — confirm there is NO `apt` under customizations. (init --defaults may not add packages; if the wizard is required for packages, note that and rely on the unit test for the feature-emission assertion.)

- [ ] **Step 8: Commit** — `git add -A && git commit -m "feat(deps): express apt packages via the standard apt-packages feature (portable), drop the bunker apt field"`

---

### Task 2: Fix the `devcontainer-lock.json` wholesale overwrite

**Files:**
- Modify: `internal/container/features.go` — `ResolveFeatures` lock write (line ~102) and the build-path write (line ~197) must merge with the existing lock instead of overwriting.
- Possibly Modify: `internal/container/lockfile.go` — add a merge helper if cleaner.
- Test: `internal/container/features_test.go` (or lockfile_test.go) — a run that resolves user features preserves a pre-existing entry for a stripped/unresolved feature.

**Interfaces:**
- Consumes: `LoadLockFile(workspace)`, `buildLockFile(refToDigest, refToVersion)`, `LockFile.Save(workspace)`.
- Produces: a lock write that is the UNION of (existing lock entries) and (newly-resolved entries), with newly-resolved winning on conflict — so features present in the committed devcontainer.json but stripped from bunker's resolution (claude-code, and future firewall/hardening) keep their locked digest.

- [ ] **Step 1: Write the failing test.** Seed a `devcontainer-lock.json` with a claude-code entry, then run the resolve/lock-write path with a set of resolved user features that does NOT include claude-code; assert the resulting lock STILL contains the claude-code entry AND the new user feature. (Read `internal/container/features_test.go` and `lockfile.go` for the exact `LockFile`/`LockedFeature` shapes and how `buildLockFile` is called; use `CLAUDE_BUNKER_CACHE_DIR`/a temp workspace for isolation.)

```go
func TestResolveFeatures_LockPreservesStrippedEntries(t *testing.T) {
	ws := t.TempDir()
	// Seed an existing lock with a bunker-managed feature entry VS Code needs.
	seed := LockFile{Features: map[string]LockedFeature{
		"ghcr.io/anthropics/devcontainer-features/claude-code:1": {Version: "1.0.0", Resolved: "ghcr.io/...@sha256:abc", Integrity: "sha256:abc"},
	}}
	if err := seed.Save(ws); err != nil { t.Fatal(err) }

	// Simulate the lock write for a run that resolved only a user feature.
	refToDigest := map[string]string{"ghcr.io/rocker-org/devcontainer-features/apt-packages:1": "sha256:def"}
	refToVersion := map[string]string{"ghcr.io/rocker-org/devcontainer-features/apt-packages:1": "1.2.0"}
	if err := writeMergedLock(ws, refToDigest, refToVersion); err != nil { t.Fatal(err) } // the new merge-aware writer

	got, err := LoadLockFile(ws)
	if err != nil { t.Fatal(err) }
	if _, ok := got.Features["ghcr.io/anthropics/devcontainer-features/claude-code:1"]; !ok {
		t.Error("claude-code lock entry must be PRESERVED (VS Code needs it), not dropped")
	}
	if _, ok := got.Features["ghcr.io/rocker-org/devcontainer-features/apt-packages:1"]; !ok {
		t.Error("newly-resolved apt-packages entry must be present")
	}
}
```
(Adjust `LockedFeature` fields to the real struct; name the merge writer to match the implementation.)

- [ ] **Step 2: Run test to verify it fails** — `go test ./internal/container/ -run TestResolveFeatures_LockPreservesStrippedEntries -v` → FAIL (claude-code dropped).

- [ ] **Step 3: Implement the merge.** Change the lock write so it loads the existing lock and merges: start from the existing `LockFile.Features`, then set/overwrite the newly-resolved refs. Extract a small helper, e.g.:

```go
// writeMergedLock loads the existing lock, overlays the freshly-resolved feature
// digests/versions, and saves — so entries for features present in the committed
// devcontainer.json but not resolved this run (e.g. bunker-managed features that
// were stripped before resolution) are preserved for VS Code/Codespaces.
func writeMergedLock(workspace string, refToDigest, refToVersion map[string]string) error {
	existing, _ := LoadLockFile(workspace)
	merged := buildLockFile(refToDigest, refToVersion) // fresh entries
	if existing.Features != nil {
		out := map[string]LockedFeature{}
		for ref, lf := range existing.Features { out[ref] = lf }   // base = existing
		for ref, lf := range merged.Features { out[ref] = lf }     // fresh wins
		merged.Features = out
	}
	return merged.Save(workspace)
}
```
Replace both `buildLockFile(...).Save(workspace)` call sites (features.go:102 and :197) with `writeMergedLock(workspace, refToDigest, refToVersion)`. NOTE: on a genuine `--rebuild`/re-resolve, freshly-resolved refs still win (they're overlaid last), so digests update correctly; only untouched entries are preserved.

- [ ] **Step 4: Run tests + build.** `go test ./internal/container/... -run 'Lock' -v` pass; `go build ./...`; `go test ./...` green; `gofmt -l .` empty.

- [ ] **Step 5: Commit** — `git add internal/container/features.go internal/container/lockfile.go internal/container/*_test.go && git commit -m "fix(lock): merge devcontainer-lock.json instead of overwriting (preserve stripped feature entries for VS Code)"`

---

### Task 3: Remove dead legacy config code

**Files:**
- Modify: `internal/config/project.go` — delete `LoadProjectConfig` (line ~77) and `ConfigPath` (line ~169), and any private helper used ONLY by them.
- Modify: `internal/config/project_test.go` — remove/adjust tests that exercised `LoadProjectConfig`/`ConfigPath` (they test dead code).

**Interfaces:**
- Consumes: nothing new.
- Produces: `internal/config/project.go` without the legacy `.claude/.claude-bunker/config.json` loader. `ProjectConfig` type, `NormalizeDomains`, `IsValidDomain`, `validateDomains`, `PluginLevel*`, `ShouldSeedHistory`, `HasGeneratedLayers`, `ExpandProjectConfig` all REMAIN (live).

- [ ] **Step 1: Confirm dead.** `grep -rn "config.LoadProjectConfig\|config.ConfigPath\|\.ConfigPath(" --include='*.go' . | grep -v '_test.go' | grep -v 'internal/config/project.go'` returns NOTHING (live path uses `devcontainer.LoadProjectConfig`). Also grep for any test-only callers and plan to delete those tests.

- [ ] **Step 2: Delete the functions + their dead tests.** Remove `LoadProjectConfig` and `ConfigPath` from project.go. Remove any `internal/config/project_test.go` tests that call them. If `ConfigPath` used a private constant/helper not used elsewhere, remove that too (grep to confirm it's not used by the surviving functions).

- [ ] **Step 3: Run tests + build.** `go build ./...`; `go vet ./...`; `go test ./...` green; `gofmt -l .` empty. Confirm no compile error from a surviving reference.

- [ ] **Step 4: Commit** — `git add internal/config/project.go internal/config/project_test.go && git commit -m "chore(config): remove dead legacy .claude-bunker/config.json loader"`

---

### Task 4: Docs — deps via the apt-packages feature

**Files:**
- Modify: `README.md` — the config/deps sections.
- Modify: `CLAUDE.md` — the config key list.

**Interfaces:**
- Consumes: the current README/CLAUDE.md (post-Phase-4 rewrite).
- Produces: docs where extra OS packages are documented via the standard `apt-packages` feature, with the bunker `apt` field removed and a "never install OS packages via lifecycle commands" note.

- [ ] **Step 1: Update README.** In the devcontainer/config section: remove `apt` from the `customizations["claude-bunker"]` field list. Add a short "Adding OS packages" subsection showing the standard feature:
  ```jsonc
  "features": {
    "ghcr.io/rocker-org/devcontainer-features/apt-packages:1": { "packages": "jq,ripgrep" }
  }
  ```
  Explain: this is portable (installs in bunker AND in VS Code/Codespaces), and `claude-bunker init` writes it for you when you list packages. Add a one-liner: do NOT install OS packages via `onCreateCommand`/`postCreateCommand` (not layer-cached; re-runs each build) — those are for project setup (`npm install`, codegen).

- [ ] **Step 2: Update CLAUDE.md.** In the `customizations["claude-bunker"]` key list, remove `apt`. Note that OS packages are expressed via the standard apt-packages feature (a normal user feature bunker resolves and VS Code honors).

- [ ] **Step 3: Verify.** `grep -in '"apt"\|customizations\["claude-bunker"\].*apt\|\bapt\b field' README.md CLAUDE.md` shows no remaining claim of a bunker `apt` field; the apt-packages feature is documented. Re-read the changed sections for consistency.

- [ ] **Step 4: Commit** — `git add README.md CLAUDE.md && git commit -m "docs: document OS packages via the standard apt-packages feature (not the removed apt field)"`

---

## Self-Review Checklist (after implementation)

- No `ProjectConfig.Apt` or bunker `apt` field anywhere (grep clean); packages flow only through `features`.
- The apt-packages feature is NOT in `bunkerManagedFeaturePrefixes` (it must resolve + install + stay in the committed file).
- Lock write preserves stripped/unresolved entries (claude-code) while updating resolved ones; `--rebuild` still refreshes digests.
- `config.LoadProjectConfig`/`ConfigPath` gone; `ProjectConfig` type + `NormalizeDomains` + friends intact.
- Docs steer to the feature; no lifecycle-command-for-deps guidance.
- Full suite + gofmt + vet green; Windows cross-build green.
