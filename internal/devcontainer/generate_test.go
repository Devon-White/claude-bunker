package devcontainer

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/Devon-White/claude-bunker/internal/config"
	"github.com/Devon-White/claude-bunker/internal/container"
)

// parseRawGenerated strips the GENERATED marker line and unmarshals the rest
// as a plain map, for asserting on top-level JSON keys that DevContainer
// doesn't model (e.g. build, runArgs) — Parse would silently drop them.
func parseRawGenerated(t *testing.T, data []byte) map[string]any {
	t.Helper()
	body := data
	if nl := bytes.IndexByte(body, '\n'); nl >= 0 {
		body = body[nl+1:]
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("generated file is not valid JSON: %v", err)
	}
	return raw
}

func TestGenerate(t *testing.T) {
	seed := false
	cfg := config.ProjectConfig{
		Exclude:      []string{"secrets/"},
		AllowDomains: []string{"registry.npmjs.org"},
		Features:     map[string]map[string]any{"ghcr.io/devcontainers/features/node:1": {"version": "20"}},
		Env:          map[string]string{"NODE_ENV": "development"},
		SeedHistory:  &seed,
	}
	data, err := Generate(cfg, GenerateOpts{Name: "demo (bunkered)"})
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

// TestGenerate_BuildDockerfileNoImageNoHardeningFeatures locks in Phase 2c:
// Generate emits build.dockerfile (VS Code builds the committed
// .devcontainer/Dockerfile, which bakes in the firewall/hardening and
// installs Claude Code natively) instead of image + OCI hardening Feature
// refs. runArgs still carries the seccomp flag, and any user features (e.g.
// apt-packages) are preserved.
func TestGenerate_BuildDockerfileNoImageNoHardeningFeatures(t *testing.T) {
	cfg := config.ProjectConfig{
		AllowDomains: []string{"registry.npmjs.org", "github.com"},
		Features: map[string]map[string]any{
			"ghcr.io/rocker-org/devcontainer-features/apt-packages:1": {"packages": "jq"},
		},
	}
	data, err := Generate(cfg, GenerateOpts{})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	raw := parseRawGenerated(t, data)

	build, ok := raw["build"].(map[string]any)
	if !ok {
		t.Fatalf("build missing or wrong type: %+v", raw["build"])
	}
	if build["dockerfile"] != "Dockerfile" {
		t.Errorf(`build.dockerfile = %v, want "Dockerfile"`, build["dockerfile"])
	}
	if _, ok := raw["image"]; ok {
		t.Errorf("image must NOT be emitted when build.dockerfile is used: %+v", raw["image"])
	}

	features, _ := raw["features"].(map[string]any)
	for _, ref := range []string{
		"ghcr.io/anthropics/devcontainer-features/claude-code:1",
		"ghcr.io/Devon-White/claude-bunker/firewall:0",
		"ghcr.io/Devon-White/claude-bunker/hardening:0",
		"ghcr.io/devcontainers/features/common-utils:2",
	} {
		if _, ok := features[ref]; ok {
			t.Errorf("hardening feature ref %q must not be emitted; the Dockerfile does this natively", ref)
		}
	}
	if _, ok := features["ghcr.io/rocker-org/devcontainer-features/apt-packages:1"]; !ok {
		t.Errorf("user apt-packages feature dropped: %+v", features)
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

// TestGenerate_PostStartRunsFirewallAgainstBakedAllowlist locks in the
// SECURITY-critical path: the generated postStartCommand must run the
// firewall via sudo against the BAKED root-owned allowlist path
// (container.AllowedDomainsPath), NOT a workspace-writable copy — this must
// match the arg-pinned sudoers grant exactly (same script path + same
// domains path) or sudo denies it, and using the workspace copy would let a
// sandboxed agent widen its own firewall allowlist.
func TestGenerate_PostStartRunsFirewallAgainstBakedAllowlist(t *testing.T) {
	data, err := Generate(config.ProjectConfig{}, GenerateOpts{})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	raw := parseRawGenerated(t, data)
	cmd, ok := raw["postStartCommand"].(string)
	if !ok {
		t.Fatalf("postStartCommand missing or wrong type: %+v", raw["postStartCommand"])
	}
	if !strings.HasPrefix(cmd, "sudo "+container.FirewallScriptPath+" "+container.AllowedDomainsPath) {
		t.Errorf("postStartCommand must sudo-run the firewall against the baked allowlist first, got %q", cmd)
	}
	if !strings.Contains(cmd, "sudo "+container.RefreshFirewallScriptPath+" "+container.AllowedDomainsPath) {
		t.Errorf("postStartCommand must sudo-run the refresh daemon against the baked allowlist, got %q", cmd)
	}
	if strings.Contains(cmd, "containerWorkspaceFolder") || strings.Contains(cmd, ".devcontainer/allowed-domains.txt") {
		t.Errorf("postStartCommand must NOT reference the agent-writable workspace copy of the allowlist, got %q", cmd)
	}
}

// TestGenerate_PostStartAppendsUserCommandAfterFirewall verifies a project's
// own postStartCommand still runs, but only AFTER the firewall is up.
func TestGenerate_PostStartAppendsUserCommandAfterFirewall(t *testing.T) {
	cfg := config.ProjectConfig{PostStartCommand: "npm install"}
	data, err := Generate(cfg, GenerateOpts{})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	raw := parseRawGenerated(t, data)
	cmd, _ := raw["postStartCommand"].(string)
	fwIdx := strings.Index(cmd, container.FirewallScriptPath)
	userIdx := strings.Index(cmd, "npm install")
	if fwIdx == -1 || userIdx == -1 || userIdx < fwIdx {
		t.Errorf("user postStartCommand must run AFTER the firewall bootstrap, got %q", cmd)
	}
}
