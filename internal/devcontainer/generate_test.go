package devcontainer

import (
	"bytes"
	"encoding/json"
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
		Features:     map[string]map[string]any{"ghcr.io/devcontainers/features/node:1": {"version": "20"}},
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

// TestGenerate_FirewallHardeningFeaturesAndRunArgs locks in VS Code
// portability: when FirewallFeature/HardeningFeature are set, both refs land
// in the features map (firewall carrying the allowDomains option so VS Code
// gets the same allowlist), and runArgs carries the seccomp profile flag so
// VS Code applies the same custom seccomp profile bunker uses natively.
func TestGenerate_FirewallHardeningFeaturesAndRunArgs(t *testing.T) {
	cfg := config.ProjectConfig{
		AllowDomains: []string{"registry.npmjs.org", "github.com"},
	}
	data, err := Generate(cfg, GenerateOpts{
		FirewallFeature:  "ghcr.io/Devon-White/claude-bunker/firewall:0",
		HardeningFeature: "ghcr.io/Devon-White/claude-bunker/hardening:0",
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	// Strip the leading GENERATED marker line before decoding as plain JSON.
	body := data
	if nl := bytes.IndexByte(body, '\n'); nl >= 0 {
		body = body[nl+1:]
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("generated file is not valid JSON: %v", err)
	}

	features, ok := raw["features"].(map[string]any)
	if !ok {
		t.Fatalf("features missing or wrong type: %+v", raw["features"])
	}
	fw, ok := features["ghcr.io/Devon-White/claude-bunker/firewall:0"].(map[string]any)
	if !ok {
		t.Fatalf("firewall feature ref missing: %+v", features)
	}
	if fw["allowDomains"] != "registry.npmjs.org,github.com" {
		t.Errorf("firewall allowDomains = %v, want joined allowlist", fw["allowDomains"])
	}
	if _, ok := features["ghcr.io/Devon-White/claude-bunker/hardening:0"]; !ok {
		t.Errorf("hardening feature ref missing: %+v", features)
	}

	runArgs, ok := raw["runArgs"].([]any)
	if !ok {
		t.Fatalf("runArgs missing or wrong type: %+v", raw["runArgs"])
	}
	want := []any{"--security-opt", "seccomp=${localWorkspaceFolder}/.devcontainer/seccomp.json"}
	if len(runArgs) != len(want) || runArgs[0] != want[0] || runArgs[1] != want[1] {
		t.Errorf("runArgs = %+v, want %+v", runArgs, want)
	}
}

// TestGenerate_CommonUtilsFeature locks in the VS Code user-resolution fix:
// when CommonUtilsFeature is set, it lands in the features map configured to
// create the claude-bunker user (uid/gid 1000, matching bunker's native base
// image user) so remoteUser: claude-bunker actually resolves on the VS
// Code / Codespaces path, and the created user gets passwordless sudo (which
// the firewall Feature's postStart needs).
func TestGenerate_CommonUtilsFeature(t *testing.T) {
	data, err := Generate(config.ProjectConfig{}, GenerateOpts{
		CommonUtilsFeature: "ghcr.io/devcontainers/features/common-utils:2",
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	body := data
	if nl := bytes.IndexByte(body, '\n'); nl >= 0 {
		body = body[nl+1:]
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("generated file is not valid JSON: %v", err)
	}

	features, ok := raw["features"].(map[string]any)
	if !ok {
		t.Fatalf("features missing or wrong type: %+v", raw["features"])
	}
	cu, ok := features["ghcr.io/devcontainers/features/common-utils:2"].(map[string]any)
	if !ok {
		t.Fatalf("common-utils feature ref missing: %+v", features)
	}
	if cu["username"] != "claude-bunker" {
		t.Errorf("common-utils username = %v, want claude-bunker", cu["username"])
	}
	if cu["userUid"] != "1000" {
		t.Errorf("common-utils userUid = %v, want 1000", cu["userUid"])
	}
	if cu["userGid"] != "1000" {
		t.Errorf("common-utils userGid = %v, want 1000", cu["userGid"])
	}
}

// TestGenerate_FirewallFeatureNoAllowDomains confirms the firewall feature's
// options object is empty (not carrying an empty-string allowDomains) when no
// allowlist is configured, matching the "only include the option if
// AllowDomains is non-empty" rule.
func TestGenerate_FirewallFeatureNoAllowDomains(t *testing.T) {
	data, err := Generate(config.ProjectConfig{}, GenerateOpts{
		FirewallFeature: "ghcr.io/Devon-White/claude-bunker/firewall:0",
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	dc, err := Parse(data, nil)
	if err != nil {
		t.Fatalf("generated file does not re-parse: %v", err)
	}
	opts, ok := dc.Features["ghcr.io/Devon-White/claude-bunker/firewall:0"]
	if !ok {
		t.Fatalf("firewall feature ref missing: %+v", dc.Features)
	}
	m, ok := opts.(map[string]any)
	if !ok {
		t.Fatalf("firewall feature options wrong type: %+v", opts)
	}
	if _, present := m["allowDomains"]; present {
		t.Errorf("allowDomains should be omitted when AllowDomains is empty, got %+v", m)
	}
}
