package config

import (
	"os"
	"testing"
)

func TestImageFingerprint_Deterministic(t *testing.T) {
	dockerfile := "FROM debian:bookworm-slim\nRUN echo hello"
	scripts := map[string][]byte{
		"init-firewall.sh": []byte("#!/bin/bash\necho firewall"),
		"tmux.conf":        []byte("set -g mouse on"),
	}
	cfg := ProjectConfig{}

	fp1 := ImageFingerprint(dockerfile, scripts, cfg, "")
	fp2 := ImageFingerprint(dockerfile, scripts, cfg, "")
	if fp1 != fp2 {
		t.Errorf("fingerprints differ: %s vs %s", fp1, fp2)
	}
}

func TestImageFingerprint_ChangesOnDockerfileChange(t *testing.T) {
	scripts := map[string][]byte{
		"init-firewall.sh": []byte("#!/bin/bash"),
	}
	cfg := ProjectConfig{}

	fp1 := ImageFingerprint("FROM debian:bookworm-slim", scripts, cfg, "")
	fp2 := ImageFingerprint("FROM debian:trixie-slim", scripts, cfg, "")

	if fp1 == fp2 {
		t.Error("fingerprint should change when Dockerfile changes")
	}
}

func TestImageFingerprint_ChangesOnScriptChange(t *testing.T) {
	dockerfile := "FROM debian:bookworm-slim"
	cfg := ProjectConfig{}

	scripts1 := map[string][]byte{"init-firewall.sh": []byte("v1")}
	scripts2 := map[string][]byte{"init-firewall.sh": []byte("v2")}

	fp1 := ImageFingerprint(dockerfile, scripts1, cfg, "")
	fp2 := ImageFingerprint(dockerfile, scripts2, cfg, "")

	if fp1 == fp2 {
		t.Error("fingerprint should change when script content changes")
	}
}

func TestImageFingerprint_ChangesOnAptPackages(t *testing.T) {
	dockerfile := "FROM debian:bookworm-slim"
	scripts := map[string][]byte{}

	cfg1 := ProjectConfig{Apt: []string{"vim"}}
	cfg2 := ProjectConfig{Apt: []string{"vim", "curl"}}

	fp1 := ImageFingerprint(dockerfile, scripts, cfg1, "")
	fp2 := ImageFingerprint(dockerfile, scripts, cfg2, "")

	if fp1 == fp2 {
		t.Error("fingerprint should change when apt packages change")
	}
}

func TestContainerFingerprint_ChangesOnDomains(t *testing.T) {
	cfg1 := ProjectConfig{AllowDomains: []string{"example.com"}}
	cfg2 := ProjectConfig{AllowDomains: []string{"example.com", "other.com"}}

	fp1 := ContainerFingerprint(cfg1)
	fp2 := ContainerFingerprint(cfg2)

	if fp1 == fp2 {
		t.Error("container fingerprint should change when domains change")
	}
}

func TestContainerFingerprint_DoesNotAffectImage(t *testing.T) {
	dockerfile := "FROM debian:bookworm-slim"
	scripts := map[string][]byte{"init-firewall.sh": []byte("#!/bin/bash")}

	cfg1 := ProjectConfig{AllowDomains: []string{"example.com"}}
	cfg2 := ProjectConfig{AllowDomains: []string{"other.com"}}

	imgFP1 := ImageFingerprint(dockerfile, scripts, cfg1, "")
	imgFP2 := ImageFingerprint(dockerfile, scripts, cfg2, "")

	if imgFP1 != imgFP2 {
		t.Error("image fingerprint should NOT change when only domains change")
	}
}

func TestCombinedFingerprint_Format(t *testing.T) {
	dockerfile := "FROM debian:bookworm-slim"
	scripts := map[string][]byte{}
	cfg := ProjectConfig{}

	fp := CombinedFingerprint(dockerfile, scripts, cfg, "")

	// Should contain a colon separating image and container fingerprints
	if len(fp) < 65 { // At minimum: 64 hex chars + ":" + some hex chars
		t.Errorf("combined fingerprint too short: %s", fp)
	}
	parts := splitFingerprint(fp)
	if len(parts) != 2 {
		t.Errorf("combined fingerprint should have 2 parts separated by ':', got: %s", fp)
	}
}

func splitFingerprint(fp string) []string {
	for i, c := range fp {
		if c == ':' {
			return []string{fp[:i], fp[i+1:]}
		}
	}
	return []string{fp}
}

func TestCompareFingerprints_FullMatch(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	dockerfile := "FROM debian:bookworm-slim"
	scripts := map[string][]byte{"init-firewall.sh": []byte("#!/bin/bash")}
	cfg := ProjectConfig{}

	// Save fingerprint
	err := SaveCombinedFingerprint(dockerfile, scripts, cfg, "test-container", "")
	if err != nil {
		t.Fatal(err)
	}

	// Compare — should match both
	result := CompareFingerprints(dockerfile, scripts, cfg, "test-container", "")
	if !result.ImageMatch {
		t.Error("image fingerprint should match")
	}
	if !result.ContainerMatch {
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
	err := SaveCombinedFingerprint(dockerfile, scripts, cfg1, "test-container", "")
	if err != nil {
		t.Fatal(err)
	}

	// Compare with changed domains (container-only change)
	cfg2 := ProjectConfig{AllowDomains: []string{"other.com"}}
	result := CompareFingerprints(dockerfile, scripts, cfg2, "test-container", "")
	if !result.ImageMatch {
		t.Error("image fingerprint should still match (only domains changed)")
	}
	if result.ContainerMatch {
		t.Error("container fingerprint should NOT match (domains changed)")
	}
}

func TestImageFingerprint_BaseImageDigest(t *testing.T) {
	dockerfile := "FROM debian:bookworm-slim"
	scripts := map[string][]byte{"init-firewall.sh": []byte("#!/bin/bash")}
	cfg := ProjectConfig{}

	// With no digest, should use dockerfile hash
	fpLocal := ImageFingerprint(dockerfile, scripts, cfg, "")

	// With a digest, should produce a different fingerprint
	fpPulled := ImageFingerprint(dockerfile, scripts, cfg, "sha256:abc123")

	if fpLocal == fpPulled {
		t.Error("fingerprint with base image digest should differ from local build fingerprint")
	}

	// Same digest should produce same fingerprint
	fpPulled2 := ImageFingerprint(dockerfile, scripts, cfg, "sha256:abc123")
	if fpPulled != fpPulled2 {
		t.Error("same base image digest should produce same fingerprint")
	}

	// Different digest should produce different fingerprint
	fpPulled3 := ImageFingerprint(dockerfile, scripts, cfg, "sha256:def456")
	if fpPulled == fpPulled3 {
		t.Error("different base image digest should produce different fingerprint")
	}
}

func TestSaveAndLoadBaseImageDigest(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	// Initially no digest cached
	digest := LoadBaseImageDigest("test-container")
	if digest != "" {
		t.Errorf("expected empty digest, got %q", digest)
	}

	// Save and load
	err := SaveBaseImageDigest("test-container", "sha256:abc123")
	if err != nil {
		t.Fatal(err)
	}

	digest = LoadBaseImageDigest("test-container")
	if digest != "sha256:abc123" {
		t.Errorf("loaded digest = %q, want %q", digest, "sha256:abc123")
	}

	// Clear
	err = SaveBaseImageDigest("test-container", "")
	if err != nil {
		t.Fatal(err)
	}

	digest = LoadBaseImageDigest("test-container")
	if digest != "" {
		t.Errorf("expected empty digest after clear, got %q", digest)
	}
}

func TestSaveAndLoadFingerprint(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	err := SaveFingerprint("test-container", "abc123:def456")
	if err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadFingerprint("test-container")
	if err != nil {
		t.Fatal(err)
	}
	if loaded != "abc123:def456" {
		t.Errorf("loaded = %q, want %q", loaded, "abc123:def456")
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

// Ensure HOME env var is set for test (needed for CacheDir)
func init() {
	if os.Getenv("HOME") == "" {
		os.Setenv("HOME", os.TempDir())
	}
}
