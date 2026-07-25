# Phase 2b — Feature Digest Pinning via `devcontainer-lock.json` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Pin devcontainer feature versions by digest using the **Dev Container spec's standard lockfile** — `devcontainer-lock.json`, in the repo's `.devcontainer/` directory, committed to source control — so feature builds are reproducible and tamper-evident, and an upstream feature change under a mutable tag is *detected* (triggers a rebuild) instead of silently absorbed.

**Architecture:** Bunker already resolves each feature's OCI digest during `ResolveFeatures` (it logs it today). This phase records those digests in the spec-standard `.devcontainer/devcontainer-lock.json`, pulls features **by digest** on subsequent builds when the lock is present (reproducible), and folds the locked digests into the image fingerprint so a lock change invalidates the cache. `--rebuild` re-resolves features fresh (pull by tag → new digests → rewrite the lock), matching the standard's "update lockfile" behavior; normal runs honor the committed lock.

**Tech Stack:** Go 1.26, go-containerregistry (crane) — already a dependency, supports pulling by both `ref:tag` and `ref@sha256:digest`. Table-driven tests with `t.Run`.

## Global Constraints

- Go 1.26+; single static binary — no new dependencies (crane already vendored).
- **Lockfile is the Dev Container spec standard, verbatim:** filename `devcontainer-lock.json`, located at `<workspace>/.devcontainer/devcontainer-lock.json` (same folder as `devcontainer.json` per the spec). Format:
  ```json
  { "features": {
    "ghcr.io/devcontainers/features/node:1": {
      "version": "1.0.4",
      "resolved": "ghcr.io/devcontainers/features/node@sha256:<hex>",
      "integrity": "sha256:<hex>"
  } } }
  ```
  Key = the feature reference as written in config (the map key from `ProjectCfg.Features`). `resolved` = the base OCI ref (tag stripped) with `@sha256:<digest>`. `integrity` = the same `sha256:<digest>`. `version` = the feature's declared version from its `devcontainer-feature.json` (empty string if the feature declares none). The file is meant to be committed to the repo (that is what makes builds reproducible across machines/CI).
- Reproducibility rule: when the lock has an entry for a feature and the run is not `--rebuild` (NoCache), pull that feature **by its locked digest**. Otherwise resolve fresh by tag and (re)write the lock entry.
- Do NOT change the existing correct feature behavior: `installsAfter` topological ordering (`sortFeatures`), option-default merging (`mergeOptionDefaults`, Phase 2a), `containerEnv`-before-install.
- Run `go build ./...` and `go test ./...` from repo root; both stay green. Commit after each task.

---

## File Structure

- `internal/container/lockfile.go` — **new.** `LockFile`/`LockedFeature` types, `LockFilePath`, `LoadLockFile`, `Save`, and the pure helpers `lockedResolvedRef` and `buildLockFile`. One responsibility: the standard lockfile format + I/O + pure transforms.
- `internal/container/lockfile_test.go` — **new.** Round-trip, absent-file, and helper tests.
- `internal/container/features.go` — **modify.** `ResolveFeatures` gains `workspace`/`noCache` params; captures each feature's digest + version; loads the lock to pull by digest; writes the updated lock.
- `internal/container/features_test.go` — **modify.** Tests for the digest-ref helper and lock-building.
- `internal/container/build.go` — **modify.** `BuildImageOpts` gains `Workspace`; threaded into `ResolveFeatures(features, workspace, noCache)`.
- `cmd/run.go` — **modify.** Pass `r.workspace` into `BuildImageOpts`; populate `BuildInput.FeatureDigests` from the lock before fingerprinting.
- `internal/config/fingerprint.go` — **modify.** `BuildInput` gains `FeatureDigests`; `imageFingerprint` hashes them.
- `internal/config/fingerprint_test.go` — **modify.** A test that a digest change flips the image fingerprint.

---

## Task 1: The standard lockfile — types, I/O, pure helpers

**Files:**
- Create: `internal/container/lockfile.go`
- Create: `internal/container/lockfile_test.go`

**Interfaces:**
- Produces: `type LockedFeature struct { Version, Resolved, Integrity string }`; `type LockFile struct { Features map[string]LockedFeature }`; `func LockFilePath(workspace string) string`; `func LoadLockFile(workspace string) (LockFile, error)` (returns an empty `LockFile{Features: map{}}` and nil error when the file is absent); `func (l LockFile) Save(workspace string) error`; pure helpers `func lockedResolvedRef(featureRef, digest string) string` and `func buildLockFile(refToDigest, refToVersion map[string]string) LockFile`.

- [ ] **Step 1: Write the failing test**

Create `internal/container/lockfile_test.go`:

```go
package container

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLockedResolvedRef(t *testing.T) {
	tests := []struct {
		name, ref, digest, want string
	}{
		{"tagged ref", "ghcr.io/devcontainers/features/node:1", "sha256:abc", "ghcr.io/devcontainers/features/node@sha256:abc"},
		{"ref with registry port and tag", "reg.example.com:5000/f/go:1.2", "sha256:def", "reg.example.com:5000/f/go@sha256:def"},
		{"untagged ref", "ghcr.io/devcontainers/features/node", "sha256:abc", "ghcr.io/devcontainers/features/node@sha256:abc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := lockedResolvedRef(tt.ref, tt.digest); got != tt.want {
				t.Errorf("lockedResolvedRef(%q,%q) = %q, want %q", tt.ref, tt.digest, got, tt.want)
			}
		})
	}
}

func TestLockFile_SaveLoadRoundTrip(t *testing.T) {
	ws := t.TempDir()

	// Absent file loads as empty, no error.
	l, err := LoadLockFile(ws)
	if err != nil {
		t.Fatalf("LoadLockFile(absent): %v", err)
	}
	if len(l.Features) != 0 {
		t.Fatalf("absent lock should be empty, got %d", len(l.Features))
	}

	l = buildLockFile(
		map[string]string{"ghcr.io/devcontainers/features/node:1": "sha256:abc"},
		map[string]string{"ghcr.io/devcontainers/features/node:1": "1.0.4"},
	)
	if err := l.Save(ws); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Written to the standard path.
	want := filepath.Join(ws, ".devcontainer", "devcontainer-lock.json")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("lock not at standard path %s: %v", want, err)
	}

	got, err := LoadLockFile(ws)
	if err != nil {
		t.Fatalf("LoadLockFile: %v", err)
	}
	f, ok := got.Features["ghcr.io/devcontainers/features/node:1"]
	if !ok {
		t.Fatal("feature missing after round-trip")
	}
	if f.Resolved != "ghcr.io/devcontainers/features/node@sha256:abc" {
		t.Errorf("resolved = %q", f.Resolved)
	}
	if f.Integrity != "sha256:abc" {
		t.Errorf("integrity = %q", f.Integrity)
	}
	if f.Version != "1.0.4" {
		t.Errorf("version = %q", f.Version)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/container/ -run 'TestLockedResolvedRef|TestLockFile_SaveLoadRoundTrip' -v`
Expected: FAIL — `lockedResolvedRef`, `LoadLockFile`, `buildLockFile`, `LockFile.Save` undefined.

- [ ] **Step 3: Write the implementation**

Create `internal/container/lockfile.go`:

```go
package container

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// LockedFeature is one entry in the Dev Container standard devcontainer-lock.json.
type LockedFeature struct {
	Version   string `json:"version,omitempty"`
	Resolved  string `json:"resolved"`
	Integrity string `json:"integrity,omitempty"`
}

// LockFile is the Dev Container spec lockfile: a map of feature reference →
// resolved digest, committed to the repo for reproducible builds.
type LockFile struct {
	Features map[string]LockedFeature `json:"features"`
}

// LockFilePath returns the standard lockfile path: <workspace>/.devcontainer/devcontainer-lock.json.
func LockFilePath(workspace string) string {
	return filepath.Join(workspace, ".devcontainer", "devcontainer-lock.json")
}

// LoadLockFile reads the lockfile. An absent file is not an error — it returns
// an empty (but non-nil) LockFile.
func LoadLockFile(workspace string) (LockFile, error) {
	l := LockFile{Features: map[string]LockedFeature{}}
	data, err := os.ReadFile(LockFilePath(workspace))
	if err != nil {
		if os.IsNotExist(err) {
			return l, nil
		}
		return l, err
	}
	if err := json.Unmarshal(data, &l); err != nil {
		return LockFile{Features: map[string]LockedFeature{}}, err
	}
	if l.Features == nil {
		l.Features = map[string]LockedFeature{}
	}
	return l, nil
}

// Save writes the lockfile to the standard path, creating .devcontainer/ if needed.
func (l LockFile) Save(workspace string) error {
	p := LockFilePath(workspace)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(p, data, 0o644)
}

// lockedResolvedRef converts a feature reference (with an optional tag) and a
// digest into the spec's `resolved` form: base ref (tag stripped) + @digest.
// It preserves a registry:port host (only the final :tag path segment is stripped).
func lockedResolvedRef(featureRef, digest string) string {
	base := featureRef
	if slash := strings.LastIndex(featureRef, "/"); slash != -1 {
		if colon := strings.LastIndex(featureRef[slash:], ":"); colon != -1 {
			base = featureRef[:slash+colon]
		}
	}
	return base + "@" + digest
}

// buildLockFile assembles a LockFile from feature-ref → digest and
// feature-ref → version maps.
func buildLockFile(refToDigest, refToVersion map[string]string) LockFile {
	l := LockFile{Features: make(map[string]LockedFeature, len(refToDigest))}
	for ref, digest := range refToDigest {
		l.Features[ref] = LockedFeature{
			Version:   refToVersion[ref],
			Resolved:  lockedResolvedRef(ref, digest),
			Integrity: digest,
		}
	}
	return l
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/container/ -run 'TestLockedResolvedRef|TestLockFile_SaveLoadRoundTrip' -v` then `go build ./...`
Expected: PASS; build clean.

- [ ] **Step 5: Commit**

```bash
git add internal/container/lockfile.go internal/container/lockfile_test.go
git commit -m "feat(container): devcontainer-lock.json types + I/O (spec-standard feature lockfile)"
```

---

## Task 2: Resolve features by locked digest; write the lock

**Files:**
- Modify: `internal/container/features.go` (`ResolveFeatures` signature + digest capture + lock read/write; `featureMetadata` gains `Version`)
- Modify: `internal/container/build.go` (`BuildImageOpts.Workspace`; pass to `ResolveFeatures`)
- Modify: `cmd/run.go` (pass `r.workspace` into `BuildImageOpts`)
- Modify: `internal/container/features_test.go`

**Interfaces:**
- Changes: `ResolveFeatures(features map[string]map[string]interface{}, workspace string, noCache bool) ([]ResolvedFeature, func(), error)`. `ResolvedFeature` gains `Digest string` and `Version string`. `featureMetadata` gains `Version string json:"version"`. `downloadAndExtract` returns the resolved digest string.
- Behavior: when `!noCache` and the lock has an entry for a feature's map-key ref, pull that feature by the locked `resolved` ref (reproducible). Otherwise pull by the tag ref and capture the fresh digest. After all features resolve, write `buildLockFile(...)` to the workspace.

- [ ] **Step 1: Write the failing test**

The network-dependent crane pull is not unit-tested; the testable core is the ref/lock plumbing. Add to `internal/container/features_test.go` a test of `featureMetadata.Version` parsing and the resolve-ref decision helper. First the plan introduces a small pure helper `resolvePullRef(mapKeyRef string, lock LockFile, noCache bool) string` (returns the locked resolved ref when usable, else the map-key ref). Test it:

```go
func TestResolvePullRef(t *testing.T) {
	lock := LockFile{Features: map[string]LockedFeature{
		"ghcr.io/devcontainers/features/node:1": {
			Resolved: "ghcr.io/devcontainers/features/node@sha256:abc",
		},
	}}

	t.Run("uses locked digest when present and not noCache", func(t *testing.T) {
		got := resolvePullRef("ghcr.io/devcontainers/features/node:1", lock, false)
		if got != "ghcr.io/devcontainers/features/node@sha256:abc" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("noCache ignores the lock (fresh by tag)", func(t *testing.T) {
		got := resolvePullRef("ghcr.io/devcontainers/features/node:1", lock, true)
		if got != "ghcr.io/devcontainers/features/node:1" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("unlocked feature pulls by tag", func(t *testing.T) {
		got := resolvePullRef("ghcr.io/devcontainers/features/go:1", lock, false)
		if got != "ghcr.io/devcontainers/features/go:1" {
			t.Errorf("got %q", got)
		}
	})
}
```

Also extend `TestReadFeatureMetadata_ParsesOptions` (or add a sibling) to assert `meta.Version` parses from `{"id":"node","version":"1.0.4",...}`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/container/ -run 'TestResolvePullRef' -v`
Expected: FAIL — `resolvePullRef` undefined.

- [ ] **Step 3: Implement the resolve helper + metadata version + digest capture**

In `internal/container/features.go`:

Add the pure helper:
```go
// resolvePullRef returns the OCI ref to pull for a feature: the lock's resolved
// (digest-pinned) ref when the lock has a usable entry and this is not a rebuild;
// otherwise the map-key ref (tag), so it resolves fresh.
func resolvePullRef(mapKeyRef string, lock LockFile, noCache bool) string {
	if noCache {
		return mapKeyRef
	}
	if f, ok := lock.Features[mapKeyRef]; ok && f.Resolved != "" {
		return f.Resolved
	}
	return mapKeyRef
}
```

Add `Version string json:"version"` to `featureMetadata`. Add `Digest string` and `Version string` to `ResolvedFeature`.

Change `downloadAndExtract(ref, destDir string)` to also return the resolved digest:
```go
func downloadAndExtract(ref, destDir string) (string, error) {
	img, err := crane.Pull(ref)
	if err != nil {
		return "", fmt.Errorf("pulling %s: %w", ref, err)
	}
	digest := ""
	if d, err := img.Digest(); err == nil {
		digest = d.String()
		log.Infof("  %s → %s", ref, digest)
	}
	if err := extractImage(img, destDir); err != nil {
		return "", err
	}
	return digest, nil
}
```

- [ ] **Step 4: Thread workspace/noCache + write the lock in `ResolveFeatures`**

Change the signature to `ResolveFeatures(features map[string]map[string]interface{}, workspace string, noCache bool)`. Load the lock once up front (`lock, _ := LoadLockFile(workspace)`); inside each goroutine, resolve the ref via `config.ResolveFeatureName(name)` then compute `pullRef := resolvePullRef(<the resolved config ref>, lock, noCache)` and pass `pullRef` to `downloadAndExtract`, capturing the returned digest onto `resolved[i].Digest` and `resolved[i].Version = meta.Version`. After `g.Wait()` and `sortFeatures`, build and save the updated lock from the resolved features (map-key ref → digest, → version) via `buildLockFile(...).Save(workspace)` — best-effort: log a warning on save failure, do not fail the build. (Use the ORIGINAL config map key as the lock key, so the lock key matches what the user wrote; keep a name→resolvedRef association as you iterate.)

Update `BuildImageOpts` (build.go) with `Workspace string`, and the call `ResolveFeatures(opts.ProjectCfg.Features, opts.Workspace, opts.NoCache)`. Update `cmd/run.go`'s `BuildImageOpts{...}` literal to pass `Workspace: r.workspace`.

(Full function edits are mechanical; keep the concurrency, cleanup, and `sortFeatures` exactly as they are — only the pull-ref, digest capture, and post-resolve lock write are added.)

- [ ] **Step 5: Run tests + build**

Run: `go test ./internal/container/ -run 'TestResolvePullRef|TestReadFeatureMetadata|TestSortFeatures|TestMergeOptionDefaults' -v` then `go build ./...` then `go test ./...`
Expected: helper tests pass; build clean (all `ResolveFeatures` call sites updated); full suite green.

- [ ] **Step 6: Commit**

```bash
git add internal/container/features.go internal/container/build.go cmd/run.go internal/container/features_test.go
git commit -m "feat(container): pin features by digest via devcontainer-lock.json; --rebuild re-resolves"
```

---

## Task 3: Fold locked digests into the image fingerprint

**Files:**
- Modify: `internal/config/fingerprint.go` (`BuildInput.FeatureDigests`; hash them in `imageFingerprint`)
- Modify: `internal/config/fingerprint_test.go`
- Modify: `cmd/run.go` (`resolveContainer` populates `FeatureDigests` from the lock before `CompareFingerprints`)

**Interfaces:**
- Changes: `BuildInput` gains `FeatureDigests map[string]string` (feature-ref → `sha256:...`). `imageFingerprint` hashes them (deterministic, sorted). Reading the committed lock at fingerprint time is offline-safe (no network).

**Context:** Today `imageFingerprint` hashes the feature config (name + options) but not the resolved digest, so an upstream feature change under the same tag doesn't invalidate the image cache. Since the digest now lives in the committed lock, the fingerprint can hash it directly — no network needed.

- [ ] **Step 1: Write the failing test**

Add to `internal/config/fingerprint_test.go`:

```go
func TestImageFingerprint_DigestChangeInvalidates(t *testing.T) {
	base := BuildInput{
		Version:    "1.0.0",
		Dockerfile: "FROM x",
		ProjectCfg: ProjectConfig{
			Features: map[string]map[string]interface{}{
				"ghcr.io/devcontainers/features/node:1": {"version": "20"},
			},
		},
		FeatureDigests: map[string]string{"ghcr.io/devcontainers/features/node:1": "sha256:aaa"},
	}
	changed := base
	changed.FeatureDigests = map[string]string{"ghcr.io/devcontainers/features/node:1": "sha256:bbb"}

	if imageFingerprint(base) == imageFingerprint(changed) {
		t.Error("a feature digest change must change the image fingerprint")
	}
	// Same digests → same fingerprint (determinism).
	if imageFingerprint(base) != imageFingerprint(base) {
		t.Error("fingerprint must be deterministic")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run TestImageFingerprint_DigestChangeInvalidates -v`
Expected: FAIL — `BuildInput` has no `FeatureDigests` field.

- [ ] **Step 3: Implement**

In `internal/config/fingerprint.go`, add the field to `BuildInput`:
```go
type BuildInput struct {
	Version        string
	Dockerfile     string
	Scripts        map[string][]byte
	ProjectCfg     ProjectConfig
	FeatureDigests map[string]string // feature ref → resolved sha256 digest (from the committed lock)
}
```

In `imageFingerprint`, after the existing features-config block (around line 70), add:
```go
	// Hash resolved feature digests from the committed lockfile so an upstream
	// feature change under the same tag invalidates the image cache.
	if len(b.FeatureDigests) > 0 {
		h.Write([]byte("featuredigests:"))
		refs := make([]string, 0, len(b.FeatureDigests))
		for ref := range b.FeatureDigests {
			refs = append(refs, ref)
		}
		sort.Strings(refs)
		for _, ref := range refs {
			h.Write([]byte(ref + "@" + b.FeatureDigests[ref] + ","))
		}
	}
```

- [ ] **Step 4: Populate `FeatureDigests` in `resolveContainer` (run.go)**

Where `r.buildInput` is constructed (run.go ~line 354), read the lock and populate the digests:
```go
	featureDigests := map[string]string{}
	if lock, err := container.LoadLockFile(r.workspace); err == nil {
		for ref, f := range lock.Features {
			featureDigests[ref] = f.Integrity
		}
	}
	r.buildInput = config.BuildInput{
		Version:        Version,
		Dockerfile:     r.cachedDockerfile,
		Scripts:        scriptMap,
		ProjectCfg:     r.projectCfg,
		FeatureDigests: featureDigests,
	}
```

- [ ] **Step 5: Run tests + build**

Run: `go test ./internal/config/ -run TestImageFingerprint -v` then `go build ./...` then `go test ./...`
Expected: PASS; build clean; full suite green.

- [ ] **Step 6: Commit**

```bash
git add internal/config/fingerprint.go internal/config/fingerprint_test.go cmd/run.go
git commit -m "feat(config): fold locked feature digests into the image fingerprint

An upstream feature change (new digest in devcontainer-lock.json) now
invalidates the image cache and triggers a rebuild, closing the supply-chain
gap where mutable-tag changes were silently absorbed."
```

---

## Self-review notes (coverage vs spec §6.8)

- Standard lockfile filename/location/format → Task 1 (`.devcontainer/devcontainer-lock.json`, spec schema).
- Reproducible builds (pull by digest when locked) + `--rebuild` re-resolve → Task 2.
- Offline-safe fingerprint that detects upstream feature changes → Task 3 (hashes the committed digest string, no network).

## Sequencing note

The lockfile lives at `.devcontainer/devcontainer-lock.json` — the standard location. When the later `internal/devcontainer/` work generates `.devcontainer/devcontainer.json`, the lock is already in the correct place and format alongside it; no migration needed. Bunker writes the lock; the user commits it (as with any lockfile). A follow-up doc task (Phase 4) should note the lock in README/`.gitignore` guidance (it should NOT be gitignored).
