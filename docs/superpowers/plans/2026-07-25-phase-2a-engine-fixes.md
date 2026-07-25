# Phase 2a — Dev Container Engine Divergence Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix two concrete devcontainer-engine divergences that break real behavior today and are needed regardless of the later `devcontainer.json` generate/read work: (1) user `apt` packages fail to install on any local-base build because `apt-get update` is skipped after the base cleaned the package lists, and (2) devcontainer feature **option defaults** are never applied, so a feature relying on its own default option values installs with those unset.

**Architecture:** Both fixes live entirely inside `internal/container/` (`generate.go`, `features.go`) — pure Dockerfile-generation and feature-metadata logic, no changes to the container-create path, no new dependencies, no config-format change. This is the first, lowest-risk slice of Phase 2; the larger pieces (feature-contributed create-opts threading, feature digest pinning + fingerprint re-partition, and the `devcontainer.json` generate/read/merge) are sequenced as follow-on plans with their own design decisions — see "Phase 2 remaining work" at the end.

**Tech Stack:** Go 1.26, docker/docker (moby) client, go-containerregistry (crane). Table-driven tests with `t.Run`.

## Global Constraints

- Go 1.26+; single static binary — do NOT add new dependencies.
- Container constants: user `claude-bunker`, workspace `/workspace`. The generated Dockerfile always ends `WORKDIR /workspace` then `USER claude-bunker`.
- The devcontainer feature model already implemented and CORRECT (do not "fix" these — they work): `installsAfter` topological ordering (`features.go:sortFeatures`, Kahn's algorithm), and `containerEnv` emitted as `ENV` before the feature install script (`generate.go`). This plan does NOT touch them.
- Feature options are passed to `install.sh` via a sourced `devcontainer-features.env` file whose keys are transformed by `safeOptionEnvName` (the devcontainer `getSafeId` rule: non-word→`_`, strip leading digits/underscores, uppercase). Option defaults must flow through that SAME path.
- Security-relevant: `apt` package names are validated by `validAptPkg`; feature option values are single-quoted + newline-stripped in `writeFeatureFiles`. Preserve those protections — a merged default value must go through the same escaping as a user value.
- Run `go build ./...` and `go test ./...` from repo root; both must stay green. Commit after each task.

---

## File Structure

- `internal/container/generate.go` — **modify.** `GenerateDockerfile`: always `apt-get update` in the generated apt layer (drop the base-Dockerfile substring heuristic).
- `internal/container/generate_test.go` — **modify.** Add a test proving `apt-get update` is emitted even when the base ran its own update.
- `internal/container/features.go` — **modify.** `featureMetadata` parses `options`; `ResolveFeatures` merges option defaults under user-supplied options before writing feature files.
- `internal/container/features_test.go` — **modify.** Add tests for metadata `options` parsing and default-merge precedence.

---

## Task 1: Always run `apt-get update` in the generated apt layer

**Files:**
- Modify: `internal/container/generate.go:60-68`
- Modify: `internal/container/generate_test.go`

**Interfaces:** none changed. `GenerateDockerfile` signature unchanged.

**Context:** `generate.go:64` currently does `if strings.Contains(opts.BaseDockerfile, "apt-get update") { emit "apt-get install" } else { emit "apt-get update && apt-get install" }`. The intent was to skip a redundant ~30MB list re-download. But every base Dockerfile that runs `apt-get update` also ends its RUN with `rm -rf /var/lib/apt/lists/*` (standard image hygiene — see `base.dockerfile.tmpl`), so by the time the generated user-apt layer runs, the lists are gone. Result: on any local-base build (dev version, `--rebuild`/NoCache, offline or failed GHCR pull) with `apt` configured, `apt-get install` fails with "Unable to locate package." The fix is to always `apt-get update` in the generated layer.

- [ ] **Step 1: Write the failing test**

Add to `internal/container/generate_test.go`:

```go
func TestGenerateDockerfile_AptAlwaysUpdates(t *testing.T) {
	// A base that runs its own apt-get update AND cleans the lists (standard
	// image hygiene). The generated apt layer must still run apt-get update,
	// because the lists were removed by the base layer.
	base := "FROM debian:bookworm-slim\nRUN apt-get update && apt-get install -y curl && rm -rf /var/lib/apt/lists/*"
	got, err := GenerateDockerfile(DockerfileOpts{BaseDockerfile: base, AptPackages: []string{"ripgrep"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The GENERATED apt layer (after the base) must contain "apt-get update && apt-get install".
	genLayer := got[len(base):]
	if !strings.Contains(genLayer, "apt-get update && apt-get install") {
		t.Errorf("generated apt layer must run apt-get update before install; got:\n%s", genLayer)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/container/ -run TestGenerateDockerfile_AptAlwaysUpdates -v`
Expected: FAIL — the base contains "apt-get update", so the current heuristic emits only `apt-get install` in the generated layer (no `apt-get update &&`).

- [ ] **Step 3: Write the fix**

In `internal/container/generate.go`, replace the conditional block (lines 60-68) with an unconditional update:

```go
		b.WriteString("\n# Apt packages\n")
		b.WriteString("USER root\n")
		// Always refresh the package lists in this layer: the base image cleans
		// /var/lib/apt/lists/* after its own installs (standard hygiene), so the
		// lists are absent here even when the base ran apt-get update earlier.
		b.WriteString("RUN apt-get update && apt-get install -y --no-install-recommends \\\n")
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/container/ -run 'TestGenerateDockerfile' -v`
Expected: PASS — the new test passes, and the existing `TestGenerateDockerfile_AptPackages` (base without its own update) still passes (it always expected `apt-get install`, which is still present).

- [ ] **Step 5: Build + full container package**

Run: `go build ./... && go test ./internal/container/`
Expected: build clean; container package green.

- [ ] **Step 6: Commit**

```bash
git add internal/container/generate.go internal/container/generate_test.go
git commit -m "fix(container): always apt-get update in the generated apt layer

The base image cleans /var/lib/apt/lists/* after its own installs, so the
substring heuristic that skipped apt-get update left user apt installs failing
with 'Unable to locate package' on every local-base build."
```

---

## Task 2: Apply devcontainer feature option defaults

**Files:**
- Modify: `internal/container/features.go`
- Modify: `internal/container/features_test.go`

**Interfaces:**
- `featureMetadata` gains `Options map[string]featureOption` where `featureOption` has a `Default` field.
- Produces: `func mergeOptionDefaults(userOpts map[string]interface{}, meta featureMetadata) map[string]interface{}` — returns a new map: the feature's option defaults, overridden by any user-supplied option. Never mutates `userOpts`.

**Context:** `featureMetadata` (features.go:36-40) parses only `id`, `installsAfter`, `containerEnv`. A devcontainer-feature.json also declares `options`, each with a `default` (e.g. `"version": {"type":"string","default":"lts"}`). The official CLI applies those defaults when the user doesn't specify the option. Bunker never does, so a feature that relies on its default option values (very common — most features default `version` to a sensible value) installs with the option unset, which can change or break what gets installed. Fix: parse `options` and merge defaults under the user's options before they flow to `writeFeatureFiles` and `ResolvedFeature.Options`.

- [ ] **Step 1: Write the failing test**

Add to `internal/container/features_test.go`:

```go
func TestMergeOptionDefaults(t *testing.T) {
	meta := featureMetadata{
		ID: "node",
		Options: map[string]featureOption{
			"version": {Default: "lts"},
			"nvmVersion": {Default: "latest"},
		},
	}

	t.Run("applies defaults when user omits them", func(t *testing.T) {
		got := mergeOptionDefaults(nil, meta)
		if got["version"] != "lts" || got["nvmVersion"] != "latest" {
			t.Errorf("defaults not applied: %+v", got)
		}
	})

	t.Run("user value overrides default", func(t *testing.T) {
		got := mergeOptionDefaults(map[string]interface{}{"version": "20"}, meta)
		if got["version"] != "20" {
			t.Errorf("user value should win: got %v", got["version"])
		}
		if got["nvmVersion"] != "latest" {
			t.Errorf("unspecified option should still get its default: %v", got["nvmVersion"])
		}
	})

	t.Run("does not mutate the caller's map", func(t *testing.T) {
		user := map[string]interface{}{"version": "20"}
		_ = mergeOptionDefaults(user, meta)
		if len(user) != 1 {
			t.Errorf("caller map was mutated: %+v", user)
		}
	})

	t.Run("option without a default is skipped", func(t *testing.T) {
		m := featureMetadata{Options: map[string]featureOption{"flag": {Default: nil}}}
		got := mergeOptionDefaults(nil, m)
		if _, present := got["flag"]; present {
			t.Errorf("option with nil default should not be added: %+v", got)
		}
	})
}

func TestReadFeatureMetadata_ParsesOptions(t *testing.T) {
	dir := t.TempDir()
	jsonContent := `{"id":"node","options":{"version":{"type":"string","default":"lts","description":"Node version"}},"containerEnv":{"FOO":"bar"}}`
	if err := os.WriteFile(filepath.Join(dir, "devcontainer-feature.json"), []byte(jsonContent), 0644); err != nil {
		t.Fatal(err)
	}
	meta, err := readFeatureMetadata(dir)
	if err != nil {
		t.Fatalf("readFeatureMetadata: %v", err)
	}
	opt, ok := meta.Options["version"]
	if !ok {
		t.Fatal("options.version not parsed")
	}
	if opt.Default != "lts" {
		t.Errorf("default = %v, want lts", opt.Default)
	}
}
```

(`os`, `path/filepath` are already imported in `features_test.go` if `TestWriteFeatureFiles` uses temp dirs; add them if the test file lacks them.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/container/ -run 'TestMergeOptionDefaults|TestReadFeatureMetadata_ParsesOptions' -v`
Expected: FAIL — `featureOption` and `mergeOptionDefaults` undefined; `featureMetadata` has no `Options` field.

- [ ] **Step 3: Add the metadata field + merge helper**

In `internal/container/features.go`, extend `featureMetadata` (lines 36-40) and add the option type + merge helper:

```go
// featureMetadata is the subset of devcontainer-feature.json we care about.
type featureMetadata struct {
	ID               string                    `json:"id"`
	RawInstallsAfter []json.RawMessage         `json:"installsAfter"`
	ContainerEnv     map[string]string         `json:"containerEnv"`
	Options          map[string]featureOption  `json:"options"`
}

// featureOption is the subset of a devcontainer-feature.json option we use.
// The spec allows string, boolean, or enum options; Default carries whichever
// JSON scalar the feature declared.
type featureOption struct {
	Default interface{} `json:"default"`
}

// mergeOptionDefaults returns a new options map: the feature's declared option
// defaults, overridden by any user-supplied option. It never mutates userOpts.
// Options with no default (Default == nil) are not added — an unset option with
// no default is left for install.sh to handle.
func mergeOptionDefaults(userOpts map[string]interface{}, meta featureMetadata) map[string]interface{} {
	merged := make(map[string]interface{}, len(meta.Options)+len(userOpts))
	for name, opt := range meta.Options {
		if opt.Default != nil {
			merged[name] = opt.Default
		}
	}
	for k, v := range userOpts {
		merged[k] = v
	}
	return merged
}
```

- [ ] **Step 4: Apply the merge in `ResolveFeatures`**

In `ResolveFeatures` (features.go), after `meta` is read and its ID resolved, merge the defaults into the options used for BOTH the written env file and the `ResolvedFeature`. Replace the section around lines 114-135:

```go
			meta, err := readFeatureMetadata(featureDir)
			if err != nil {
				// Metadata is optional; use defaults
				meta = featureMetadata{ID: name}
			}
			if meta.ID == "" {
				meta.ID = name
			}

			// Merge the feature's declared option defaults under the user's
			// options, so a feature that relies on its default option values
			// (e.g. version) installs correctly when the user omits them.
			effectiveOpts := mergeOptionDefaults(opts, meta)

			if err := writeFeatureFiles(featureDir, effectiveOpts); err != nil {
				return fmt.Errorf("writing feature files for %s: %w", name, err)
			}

			resolved[i] = ResolvedFeature{
				ID:            meta.ID,
				Source:        ref,
				InstallDir:    featureDir,
				Options:       effectiveOpts,
				Env:           meta.ContainerEnv,
				InstallsAfter: meta.installsAfterRefs(),
			}
			return nil
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/container/ -run 'TestMergeOptionDefaults|TestReadFeatureMetadata|TestWriteFeatureFiles' -v`
Expected: PASS. (Existing `TestWriteFeatureFiles` still passes — it calls `writeFeatureFiles` directly, unaffected.)

- [ ] **Step 6: Build + full suite**

Run: `go build ./... && go test ./...`
Expected: build clean; all packages green.

- [ ] **Step 7: Commit**

```bash
git add internal/container/features.go internal/container/features_test.go
git commit -m "feat(container): apply devcontainer feature option defaults

featureMetadata now parses options; ResolveFeatures merges each feature's
declared option defaults under the user-supplied options, so a feature that
relies on its default option values installs correctly when the user omits them."
```

---

## Self-review notes (coverage)

- apt-lists divergence (spec §6.6, ranked issue 11) → Task 1.
- feature option defaults (spec §6.4) → Task 2.
- The other §6.4 items already verified correct in the current code and intentionally NOT touched: `installsAfter` topological ordering (`sortFeatures`), `containerEnv` before install.

## Phase 2 remaining work (sequenced follow-on plans — NOT in this plan)

Each has a design decision or cross-package/higher-risk change, so they are separate plans:

1. **Feature-contributed create-time options** (capAdd/securityOpt/mounts): parse from `featureMetadata`, thread from `ResolveFeatures` → `BuildImage` result → `CreateAndStartOpts` → `HostConfig` (union with bunker's forced `NET_ADMIN`/`NET_RAW` + seccomp). Cross-package threading (build → run.go → create). Note: most feature-contributed caps (e.g. docker-in-docker) are moot under bunker's firewall, so this is lower practical priority.
2. **Feature digest pinning + fingerprint re-partition** (spec §6.8): the resolved digest is logged (`features.go` `downloadAndExtract`) but never captured into the fingerprint, so an upstream feature change under the same tag doesn't invalidate the image cache. **Design decision for the user:** where the resolved digests are recorded (a `.claude/.claude-bunker/features.lock` committed with the repo, vs. the per-container fingerprint cache) and when they are re-resolved (only on `--rebuild` / explicit re-lock, vs. every run). The fingerprint then hashes the pinned digest string (offline-safe, per §6.8).
3. **onCreateCommand lifecycle stage** (spec §6.4/§6.5): currently emitted as a root build-time `RUN` (unrestricted network, no `/workspace`); move to an in-container post-create step run as the remote user, with a first-creation marker so it doesn't re-run on reuse. Needs the reuse-marker infrastructure.
4. **`devcontainer.json` generate/read/merge** (spec §6.1–6.3): the `internal/devcontainer/` module — the larger config-format adoption, with the two-mode ownership rules and the `customizations["claude-bunker"]` namespace. Largest piece; its own plan.
