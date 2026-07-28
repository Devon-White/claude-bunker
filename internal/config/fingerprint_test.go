package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testVersion = "v1.0.0-test"

func TestImageFingerprint_Deterministic(t *testing.T) {
	b := BuildInput{
		Version:    testVersion,
		Dockerfile: "FROM debian:bookworm-slim\nRUN echo hello",
		Scripts: map[string][]byte{
			"init-firewall.sh": []byte("#!/bin/bash\necho firewall"),
			"tmux.conf":        []byte("set -g mouse on"),
		},
		ProjectCfg: ProjectConfig{},
	}

	fp1 := imageFingerprint(b)
	fp2 := imageFingerprint(b)
	if fp1 != fp2 {
		t.Errorf("fingerprints differ: %s vs %s", fp1, fp2)
	}
}

func TestImageFingerprint_ChangesOnDockerfileChange(t *testing.T) {
	scripts := map[string][]byte{
		"init-firewall.sh": []byte("#!/bin/bash"),
	}
	cfg := ProjectConfig{}

	fp1 := imageFingerprint(BuildInput{Version: testVersion, Dockerfile: "FROM debian:bookworm-slim", Scripts: scripts, ProjectCfg: cfg})
	fp2 := imageFingerprint(BuildInput{Version: testVersion, Dockerfile: "FROM debian:trixie-slim", Scripts: scripts, ProjectCfg: cfg})

	if fp1 == fp2 {
		t.Error("fingerprint should change when Dockerfile changes")
	}
}

func TestImageFingerprint_ChangesOnScriptChange(t *testing.T) {
	dockerfile := "FROM debian:bookworm-slim"
	cfg := ProjectConfig{}

	scripts1 := map[string][]byte{"init-firewall.sh": []byte("v1")}
	scripts2 := map[string][]byte{"init-firewall.sh": []byte("v2")}

	fp1 := imageFingerprint(BuildInput{Version: testVersion, Dockerfile: dockerfile, Scripts: scripts1, ProjectCfg: cfg})
	fp2 := imageFingerprint(BuildInput{Version: testVersion, Dockerfile: dockerfile, Scripts: scripts2, ProjectCfg: cfg})

	if fp1 == fp2 {
		t.Error("fingerprint should change when script content changes")
	}
}

func TestImageFingerprint_ChangesOnProxySourceChange(t *testing.T) {
	dockerfile := "FROM debian:bookworm-slim"
	cfg := ProjectConfig{}

	proxy1 := map[string][]byte{"egressproxy/main.go": []byte("package main // v1")}
	proxy2 := map[string][]byte{"egressproxy/main.go": []byte("package main // v2")}

	fp1 := imageFingerprint(BuildInput{Version: testVersion, Dockerfile: dockerfile, ProxySources: proxy1, ProjectCfg: cfg})
	fp2 := imageFingerprint(BuildInput{Version: testVersion, Dockerfile: dockerfile, ProxySources: proxy2, ProjectCfg: cfg})

	if fp1 == fp2 {
		t.Error("image fingerprint should change when egress-proxy source content changes")
	}
}

// TestImageFingerprintCoversProxySource proves the image fingerprint is fed by
// the egress-proxy sources: it must be deterministic across calls, non-empty,
// and (per TestImageFingerprint_ChangesOnProxySourceChange above) sensitive to
// a single byte of proxy-source content.
func TestImageFingerprintCoversProxySource(t *testing.T) {
	in := BuildInput{
		Version:    testVersion,
		Dockerfile: "FROM debian:bookworm-slim",
		Scripts:    map[string][]byte{"init-firewall.sh": []byte("#!/bin/bash")},
		ProxySources: map[string][]byte{
			"egressproxy/main.go": []byte("package main"),
			"egressproxy/go.mod":  []byte("module egressproxyd\n\ngo 1.23\n"),
		},
		ProjectCfg: ProjectConfig{},
	}
	base := imageFingerprint(in)

	again := imageFingerprint(in)
	if base != again {
		t.Fatal("image fingerprint must be deterministic")
	}
	if base == "" {
		t.Fatal("image fingerprint empty")
	}
}

// TestImageFingerprint_ChangesOnFeaturesConfig covers plain Features-map
// changes (independent of FeatureDigests, covered separately). Extra apt
// packages are now expressed as a Features entry (the standard apt-packages
// feature), so this is also the regression test for that path invalidating
// the image cache.
func TestImageFingerprint_ChangesOnFeaturesConfig(t *testing.T) {
	dockerfile := "FROM debian:bookworm-slim"
	scripts := map[string][]byte{}

	cfg1 := ProjectConfig{Features: map[string]map[string]any{
		"ghcr.io/rocker-org/devcontainer-features/apt-packages:1": {"packages": "vim"},
	}}
	cfg2 := ProjectConfig{Features: map[string]map[string]any{
		"ghcr.io/rocker-org/devcontainer-features/apt-packages:1": {"packages": "vim,curl"},
	}}

	fp1 := imageFingerprint(BuildInput{Version: testVersion, Dockerfile: dockerfile, Scripts: scripts, ProjectCfg: cfg1})
	fp2 := imageFingerprint(BuildInput{Version: testVersion, Dockerfile: dockerfile, Scripts: scripts, ProjectCfg: cfg2})

	if fp1 == fp2 {
		t.Error("fingerprint should change when a feature's options change")
	}
}

func TestImageFingerprint_ChangesOnVersionChange(t *testing.T) {
	dockerfile := "FROM debian:bookworm-slim"
	scripts := map[string][]byte{"init-firewall.sh": []byte("#!/bin/bash")}
	cfg := ProjectConfig{}

	fp1 := imageFingerprint(BuildInput{Version: testVersion, Dockerfile: dockerfile, Scripts: scripts, ProjectCfg: cfg})
	fp2 := imageFingerprint(BuildInput{Version: "v1.1.0", Dockerfile: dockerfile, Scripts: scripts, ProjectCfg: cfg})

	if fp1 == fp2 {
		t.Error("fingerprint should change when version changes")
	}
}

func TestCompareFingerprints_VersionChange(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	dockerfile := "FROM debian:bookworm-slim"
	scripts := map[string][]byte{"init-firewall.sh": []byte("#!/bin/bash")}
	cfg := ProjectConfig{}

	b := BuildInput{Version: testVersion, Dockerfile: dockerfile, Scripts: scripts, ProjectCfg: cfg}

	// Save fingerprint with v1.0.0
	result := CompareFingerprints(b, "test-container")
	if err := result.Save("test-container"); err != nil {
		t.Fatal(err)
	}

	// Compare with v1.1.0 — image should NOT match (version changed)
	b2 := BuildInput{Version: "v1.1.0", Dockerfile: dockerfile, Scripts: scripts, ProjectCfg: cfg}
	result2 := CompareFingerprints(b2, "test-container")
	if result2.ImageMatch {
		t.Error("image fingerprint should NOT match after version upgrade")
	}
	if !result2.ContainerMatch {
		t.Error("container fingerprint should still match (version doesn't affect container fp)")
	}
}

func TestContainerFingerprint_ChangesOnDomains(t *testing.T) {
	cfg1 := ProjectConfig{AllowDomains: []string{"example.com"}}
	cfg2 := ProjectConfig{AllowDomains: []string{"example.com", "other.com"}}

	fp1 := containerFingerprint(cfg1, false)
	fp2 := containerFingerprint(cfg2, false)

	if fp1 == fp2 {
		t.Error("container fingerprint should change when domains change")
	}
}

func TestContainerFingerprint_DoesNotAffectImage(t *testing.T) {
	dockerfile := "FROM debian:bookworm-slim"
	scripts := map[string][]byte{"init-firewall.sh": []byte("#!/bin/bash")}

	cfg1 := ProjectConfig{AllowDomains: []string{"example.com"}}
	cfg2 := ProjectConfig{AllowDomains: []string{"other.com"}}

	imgFP1 := imageFingerprint(BuildInput{Version: testVersion, Dockerfile: dockerfile, Scripts: scripts, ProjectCfg: cfg1})
	imgFP2 := imageFingerprint(BuildInput{Version: testVersion, Dockerfile: dockerfile, Scripts: scripts, ProjectCfg: cfg2})

	if imgFP1 != imgFP2 {
		t.Error("image fingerprint should NOT change when only domains change")
	}
}

func TestCombinedFingerprint_Format(t *testing.T) {
	b := BuildInput{
		Version:    testVersion,
		Dockerfile: "FROM debian:bookworm-slim",
		Scripts:    map[string][]byte{},
		ProjectCfg: ProjectConfig{},
	}

	// Use CompareFingerprints with a non-existent cache to get the combined hash
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	result := CompareFingerprints(b, "test-container")

	// Save and load to verify format
	if err := result.Save("test-container"); err != nil {
		t.Fatal(err)
	}
	fp, err := loadFingerprint("test-container")
	if err != nil {
		t.Fatal(err)
	}

	// Should contain a colon separating image and container fingerprints
	if len(fp) < 65 { // At minimum: 64 hex chars + ":" + some hex chars
		t.Errorf("combined fingerprint too short: %s", fp)
	}
	parts := strings.SplitN(fp, ":", 2)
	if len(parts) != 2 {
		t.Errorf("combined fingerprint should have 2 parts separated by ':', got: %s", fp)
	}
}

func TestCompareFingerprints_FullMatch(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	b := BuildInput{
		Version:    testVersion,
		Dockerfile: "FROM debian:bookworm-slim",
		Scripts:    map[string][]byte{"init-firewall.sh": []byte("#!/bin/bash")},
		ProjectCfg: ProjectConfig{},
	}

	// Save fingerprint via CompareFingerprints + Save
	result := CompareFingerprints(b, "test-container")
	if err := result.Save("test-container"); err != nil {
		t.Fatal(err)
	}

	// Compare — should match both
	result2 := CompareFingerprints(b, "test-container")
	if !result2.ImageMatch {
		t.Error("image fingerprint should match")
	}
	if !result2.ContainerMatch {
		t.Error("container fingerprint should match")
	}
}

func TestCompareFingerprints_ContainerOnlyChange(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	dockerfile := "FROM debian:bookworm-slim"
	scripts := map[string][]byte{"init-firewall.sh": []byte("#!/bin/bash")}
	cfg1 := ProjectConfig{AllowDomains: []string{"example.com"}}

	// Save with original config
	b1 := BuildInput{Version: testVersion, Dockerfile: dockerfile, Scripts: scripts, ProjectCfg: cfg1}
	result := CompareFingerprints(b1, "test-container")
	if err := result.Save("test-container"); err != nil {
		t.Fatal(err)
	}

	// Compare with changed domains (container-only change)
	cfg2 := ProjectConfig{AllowDomains: []string{"other.com"}}
	result2 := CompareFingerprints(BuildInput{Version: testVersion, Dockerfile: dockerfile, Scripts: scripts, ProjectCfg: cfg2}, "test-container")
	if !result2.ImageMatch {
		t.Error("image fingerprint should still match (only domains changed)")
	}
	if result2.ContainerMatch {
		t.Error("container fingerprint should NOT match (domains changed)")
	}
}

func TestSaveAndLoadFingerprint(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	err := saveFingerprint("test-container", "abc123:def456")
	if err != nil {
		t.Fatal(err)
	}

	loaded, err := loadFingerprint("test-container")
	if err != nil {
		t.Fatal(err)
	}
	if loaded != "abc123:def456" {
		t.Errorf("loaded = %q, want %q", loaded, "abc123:def456")
	}
}

func TestClearFingerprint(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	// Save a fingerprint
	err := saveFingerprint("test-container", "abc123:def456")
	if err != nil {
		t.Fatal(err)
	}

	// Clear it
	err = ClearFingerprint("test-container")
	if err != nil {
		t.Fatal(err)
	}

	// Should be gone
	_, err = loadFingerprint("test-container")
	if err == nil {
		t.Error("expected error loading cleared fingerprint")
	}
}

func TestClearFingerprint_NonExistent(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	// Should not error when clearing non-existent fingerprint
	err := ClearFingerprint("nonexistent-container")
	if err != nil {
		t.Errorf("ClearFingerprint should not error on non-existent: %v", err)
	}
}

func TestEffectiveWorkdir(t *testing.T) {
	tests := []struct {
		cfg  ProjectConfig
		want string
	}{
		{ProjectConfig{}, "/workspace"},
		{ProjectConfig{Workspace: "."}, "/workspace"},
		{ProjectConfig{Workspace: "./packages/backend"}, "/workspace/packages/backend"},
		{ProjectConfig{Workspace: "src"}, "/workspace/src"},
	}

	for _, tt := range tests {
		got, err := EffectiveWorkdir(tt.cfg)
		if err != nil {
			t.Errorf("EffectiveWorkdir(%+v) unexpected error: %v", tt.cfg, err)
			continue
		}
		if got != tt.want {
			t.Errorf("EffectiveWorkdir(%+v) = %q, want %q", tt.cfg, got, tt.want)
		}
	}
}

func TestEffectiveWorkdir_Traversal(t *testing.T) {
	traversalPaths := []string{
		"../../etc",
		"../../../",
		"foo/../../..",
		"../secret",
	}

	for _, ws := range traversalPaths {
		_, err := EffectiveWorkdir(ProjectConfig{Workspace: ws})
		if err == nil {
			t.Errorf("EffectiveWorkdir(workspace=%q) should have returned an error for path traversal", ws)
		}
	}
}

func TestContainerFingerprint_ChangesOnPlugins(t *testing.T) {
	cfg1 := ProjectConfig{}
	cfg2 := ProjectConfig{Plugins: "user"}

	fp1 := containerFingerprint(cfg1, false)
	fp2 := containerFingerprint(cfg2, false)

	if fp1 == fp2 {
		t.Error("container fingerprint should change when plugins field changes")
	}

	// Different plugin levels should produce different fingerprints
	cfg3 := ProjectConfig{Plugins: "all"}
	fp3 := containerFingerprint(cfg3, false)
	if fp2 == fp3 {
		t.Error("container fingerprint should differ between plugin levels")
	}
}

func TestContainerFingerprint_ChangesOnSeedHistory(t *testing.T) {
	// Default (nil) vs explicit true should differ from nil
	cfgDefault := ProjectConfig{}
	boolTrue := true
	cfgTrue := ProjectConfig{SeedHistory: &boolTrue}
	boolFalse := false
	cfgFalse := ProjectConfig{SeedHistory: &boolFalse}

	fpDefault := containerFingerprint(cfgDefault, false)
	fpTrue := containerFingerprint(cfgTrue, false)
	fpFalse := containerFingerprint(cfgFalse, false)

	if fpDefault == fpTrue {
		t.Error("container fingerprint should change when seedHistory is explicitly set to true vs unset")
	}
	if fpDefault == fpFalse {
		t.Error("container fingerprint should change when seedHistory is set to false vs unset")
	}
	if fpTrue == fpFalse {
		t.Error("container fingerprint should differ between seedHistory true and false")
	}
}

// TestContainerFingerprint_ChangesOnMasking verifies that toggling credential
// masking (container.ShouldMask — auth secrets present and no upstream proxy)
// forces a container recreate rather than silently reusing a container created
// under a different masking state. Only the boolean mask state is hashed;
// no raw token/secret material ever enters the fingerprint.
func TestContainerFingerprint_ChangesOnMasking(t *testing.T) {
	cfg := ProjectConfig{}

	fpOff := containerFingerprint(cfg, false)
	fpOn := containerFingerprint(cfg, true)

	if fpOff == fpOn {
		t.Error("container fingerprint should change when masking is toggled")
	}

	// Deterministic for a fixed mask state.
	if containerFingerprint(cfg, true) != fpOn {
		t.Error("container fingerprint must be deterministic for a fixed masking state")
	}
}

func TestPluginLevel(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", ""},
		{"false", ""},
		{"invalid", ""},
		{"project", "project"},
		{"user", "user"},
		{"all", "all"},
	}
	for _, tt := range tests {
		cfg := ProjectConfig{Plugins: tt.input}
		got := cfg.PluginLevel()
		if got != tt.want {
			t.Errorf("PluginLevel(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestImageFingerprint_DigestChangeInvalidates(t *testing.T) {
	base := BuildInput{
		Version:    "1.0.0",
		Dockerfile: "FROM x",
		ProjectCfg: ProjectConfig{
			Features: map[string]map[string]any{
				"ghcr.io/devcontainers/features/node:1": {"version": "20"},
			},
		},
		// Two entries so the deterministic-sort path is actually exercised
		// (a single-entry map has only one possible iteration order).
		FeatureDigests: map[string]string{
			"ghcr.io/devcontainers/features/node:1": "sha256:aaa",
			"ghcr.io/devcontainers/features/go:1":   "sha256:ccc",
		},
	}
	changed := base
	changed.FeatureDigests = map[string]string{
		"ghcr.io/devcontainers/features/node:1": "sha256:bbb", // node digest changed
		"ghcr.io/devcontainers/features/go:1":   "sha256:ccc",
	}

	if imageFingerprint(base) == imageFingerprint(changed) {
		t.Error("a feature digest change must change the image fingerprint")
	}
	// Same digests → same fingerprint (determinism across independent calls).
	fp1 := imageFingerprint(base)
	fp2 := imageFingerprint(base)
	if fp1 != fp2 {
		t.Error("fingerprint must be deterministic")
	}
}

func TestCacheDir(t *testing.T) {
	t.Run("honors CLAUDE_BUNKER_CACHE_DIR", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("CLAUDE_BUNKER_CACHE_DIR", dir)
		got, err := CacheDir()
		if err != nil {
			t.Fatalf("CacheDir() error: %v", err)
		}
		if got != dir {
			t.Errorf("CacheDir() = %q, want %q", got, dir)
		}
	})
	t.Run("falls back to ~/.cache/claude-bunker", func(t *testing.T) {
		t.Setenv("CLAUDE_BUNKER_CACHE_DIR", "")
		home := t.TempDir()
		t.Setenv("HOME", home)
		got, err := CacheDir()
		if err != nil {
			t.Fatalf("CacheDir() error: %v", err)
		}
		want := filepath.Join(home, ".cache", "claude-bunker")
		if got != want {
			t.Errorf("CacheDir() = %q, want %q", got, want)
		}
	})
}

// Ensure HOME env var is set for test (needed for CacheDir)
func init() {
	if os.Getenv("HOME") == "" {
		os.Setenv("HOME", os.TempDir())
	}
}
