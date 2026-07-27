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
		{"already digest-pinned ref", "ghcr.io/devcontainers/features/node@sha256:1111", "sha256:2222", "ghcr.io/devcontainers/features/node@sha256:2222"},
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

func TestLoadLockFile_CorruptAndNull(t *testing.T) {
	// Corrupt JSON: returns an error but a usable, non-nil empty Features map.
	ws := t.TempDir()
	dir := filepath.Join(ws, ".devcontainer")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "devcontainer-lock.json"), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	l, err := LoadLockFile(ws)
	if err == nil {
		t.Error("corrupt JSON should return an error")
	}
	if l.Features == nil {
		t.Error("Features must be non-nil even on error")
	}

	// "features": null normalizes to a non-nil empty map.
	ws2 := t.TempDir()
	dir2 := filepath.Join(ws2, ".devcontainer")
	if err := os.MkdirAll(dir2, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir2, "devcontainer-lock.json"), []byte(`{"features":null}`), 0o644); err != nil {
		t.Fatal(err)
	}
	l2, err := LoadLockFile(ws2)
	if err != nil {
		t.Fatalf("valid JSON: %v", err)
	}
	if l2.Features == nil {
		t.Error("null features must normalize to a non-nil map")
	}
}

func TestBuildLockFile_SkipsEmptyDigest(t *testing.T) {
	l := buildLockFile(
		map[string]string{"ghcr.io/f/good:1": "sha256:abc", "ghcr.io/f/bad:1": ""},
		map[string]string{"ghcr.io/f/good:1": "1.0"},
	)
	if _, ok := l.Features["ghcr.io/f/bad:1"]; ok {
		t.Error("empty-digest feature must be omitted from the lock")
	}
	if _, ok := l.Features["ghcr.io/f/good:1"]; !ok {
		t.Error("good feature must be present")
	}
}

// TestResolveFeatures_LockPreservesStrippedEntries proves the merge-aware
// writer preserves lock entries for features present in the committed
// devcontainer.json but not resolved this run — e.g. bunker-managed features
// (claude-code, and future firewall/hardening) that are stripped before
// bunker's own feature resolution. VS Code/Codespaces still need those pinned
// digests, so a wholesale overwrite (the old bug) must not drop them.
func TestResolveFeatures_LockPreservesStrippedEntries(t *testing.T) {
	ws := t.TempDir()

	// Seed an existing lock with a bunker-managed feature entry VS Code needs.
	seed := LockFile{Features: map[string]LockedFeature{
		"ghcr.io/anthropics/devcontainer-features/claude-code:1": {
			Version:   "1.0.0",
			Resolved:  "ghcr.io/anthropics/devcontainer-features/claude-code@sha256:abc",
			Integrity: "sha256:abc",
		},
	}}
	if err := seed.Save(ws); err != nil {
		t.Fatal(err)
	}

	// Simulate the lock write for a run that resolved only a user feature
	// (claude-code was stripped before resolution, so it's absent here).
	refToDigest := map[string]string{"ghcr.io/rocker-org/devcontainer-features/apt-packages:1": "sha256:def"}
	refToVersion := map[string]string{"ghcr.io/rocker-org/devcontainer-features/apt-packages:1": "1.2.0"}
	if err := writeMergedLock(ws, refToDigest, refToVersion); err != nil {
		t.Fatal(err)
	}

	got, err := LoadLockFile(ws)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Features["ghcr.io/anthropics/devcontainer-features/claude-code:1"]; !ok {
		t.Error("claude-code lock entry must be PRESERVED (VS Code needs it), not dropped")
	}
	if _, ok := got.Features["ghcr.io/rocker-org/devcontainer-features/apt-packages:1"]; !ok {
		t.Error("newly-resolved apt-packages entry must be present")
	}
	if len(got.Features) != 2 {
		t.Errorf("expected exactly 2 entries after merge, got %d: %+v", len(got.Features), got.Features)
	}
}

// TestWriteMergedLock_FreshWinsOnConflict proves that when a feature is
// re-resolved (e.g. via --rebuild), the freshly-resolved digest overwrites
// the stale locked entry rather than the old one winning — otherwise
// --rebuild would never actually update pinned digests.
func TestWriteMergedLock_FreshWinsOnConflict(t *testing.T) {
	ws := t.TempDir()

	ref := "ghcr.io/devcontainers/features/node:1"
	seed := LockFile{Features: map[string]LockedFeature{
		ref: {Version: "1.0.0", Resolved: ref + "@sha256:old", Integrity: "sha256:old"},
	}}
	if err := seed.Save(ws); err != nil {
		t.Fatal(err)
	}

	refToDigest := map[string]string{ref: "sha256:new"}
	refToVersion := map[string]string{ref: "2.0.0"}
	if err := writeMergedLock(ws, refToDigest, refToVersion); err != nil {
		t.Fatal(err)
	}

	got, err := LoadLockFile(ws)
	if err != nil {
		t.Fatal(err)
	}
	f, ok := got.Features[ref]
	if !ok {
		t.Fatal("re-resolved feature must still be present")
	}
	if f.Integrity != "sha256:new" {
		t.Errorf("integrity = %q, want fresh digest sha256:new (rebuild must update it)", f.Integrity)
	}
	if f.Version != "2.0.0" {
		t.Errorf("version = %q, want fresh version 2.0.0", f.Version)
	}
}
