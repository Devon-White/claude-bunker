package devcontainer

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/Devon-White/claude-bunker/internal/config"
)

func TestGenerate(t *testing.T) {
	seed := false
	cfg := config.ProjectConfig{
		Exclude:      []string{"secrets/"},
		AllowDomains: []string{"registry.npmjs.org"},
		Features:     map[string]map[string]interface{}{"ghcr.io/devcontainers/features/node:1": {"version": "20"}},
		Env:          map[string]string{"NODE_ENV": "development"},
		SeedHistory:  &seed,
	}
	data, err := Generate(cfg, GenerateOpts{Name: "demo (bunkered)", Image: "base:debian", ClaudeCodeFeature: "ghcr.io/anthropics/devcontainer-features/claude-code:1"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	// First non-blank line is the marker.
	first := strings.SplitN(strings.TrimLeft(string(data), "\n"), "\n", 2)[0]
	if first != GeneratedMarker {
		t.Errorf("first line = %q, want the marker", first)
	}

	// Re-parse the generated file and assert the forced + mapped fields.
	dc, err := Parse(data, nil)
	if err != nil {
		t.Fatalf("generated file does not re-parse: %v", err)
	}
	if !slices.Contains(dc.CapAdd, "NET_ADMIN") || !slices.Contains(dc.CapAdd, "NET_RAW") {
		t.Errorf("capAdd must force NET_ADMIN/NET_RAW: %+v", dc.CapAdd)
	}
	if dc.RemoteUser != "claude-bunker" {
		t.Errorf("remoteUser = %q, want claude-bunker", dc.RemoteUser)
	}
	if _, ok := dc.Features["ghcr.io/devcontainers/features/node:1"]; !ok {
		t.Errorf("user feature dropped: %+v", dc.Features)
	}
	if _, ok := dc.Features["ghcr.io/anthropics/devcontainer-features/claude-code:1"]; !ok {
		t.Errorf("claude-code feature not added: %+v", dc.Features)
	}
	// Round-trip: the bunker extras survive back into ProjectConfig.
	got := ToProjectConfig(dc)
	if len(got.Exclude) != 1 || got.Exclude[0] != "secrets/" {
		t.Errorf("exclude round-trip failed: %+v", got.Exclude)
	}
	if len(got.AllowDomains) != 1 || got.AllowDomains[0] != "registry.npmjs.org" {
		t.Errorf("allowDomains round-trip failed: %+v", got.AllowDomains)
	}
	if got.SeedHistory == nil || *got.SeedHistory != false {
		t.Errorf("seedHistory round-trip failed: %v", got.SeedHistory)
	}
	if !bytes.Contains(data, []byte("claude-bunker")) {
		t.Error("customizations namespace missing")
	}
}

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
