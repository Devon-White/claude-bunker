package devcontainer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Devon-White/claude-bunker/internal/config"
)

func TestStripBunkerFeatures(t *testing.T) {
	in := map[string]map[string]any{
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

func TestStripBunkerFeatures_PrefixBoundary(t *testing.T) {
	in := map[string]map[string]any{
		"ghcr.io/anthropics/devcontainer-features/claude-code:1":     {}, // bunker-managed → stripped
		"ghcr.io/anthropics/devcontainer-features/claude-code-cli:1": {}, // DISTINCT sibling → kept
	}
	got := stripBunkerFeatures(in)
	if _, ok := got["ghcr.io/anthropics/devcontainer-features/claude-code:1"]; ok {
		t.Error("exact bunker feature must be stripped")
	}
	if _, ok := got["ghcr.io/anthropics/devcontainer-features/claude-code-cli:1"]; !ok {
		t.Error("a distinct sibling feature sharing the prefix must NOT be stripped")
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

// TestLoadProjectConfig_StripsFirewallHardeningAndIgnoresRunArgs proves there
// is no double-apply between bunker's native firewall/seccomp/apparmor and
// the portable devcontainer.json Generate now emits: a file that references
// the firewall, hardening, and common-utils features AND carries a runArgs
// seccomp flag must (a) parse without error — DevContainer has no runArgs
// field, so the key is silently ignored — and (b) have all three features
// stripped from the engine's ProjectConfig, since bunker applies the
// firewall/hardening natively via internal/container and creates its
// claude-bunker user natively too (common-utils is VS Code-only), rather than
// resolving the OCI features itself.
func TestLoadProjectConfig_StripsFirewallHardeningAndIgnoresRunArgs(t *testing.T) {
	ws := t.TempDir()
	dir := filepath.Join(ws, ".devcontainer")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{
  "features": {
    "ghcr.io/Devon-White/claude-bunker/firewall:0": {"allowDomains": "github.com"},
    "ghcr.io/Devon-White/claude-bunker/hardening:0": {},
    "ghcr.io/devcontainers/features/common-utils:2": {"username": "claude-bunker", "userUid": "1000", "userGid": "1000"},
    "ghcr.io/devcontainers/features/node:1": {"version": "20"}
  },
  "runArgs": ["--security-opt", "seccomp=${localWorkspaceFolder}/.devcontainer/seccomp.json"]
}`
	if err := os.WriteFile(filepath.Join(dir, "devcontainer.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, found, err := LoadProjectConfig(ws)
	if err != nil || !found {
		t.Fatalf("present devcontainer.json with runArgs: found=%v err=%v", found, err)
	}
	if _, ok := cfg.Features["ghcr.io/Devon-White/claude-bunker/firewall:0"]; ok {
		t.Error("firewall feature must be stripped from the engine config (bunker applies it natively)")
	}
	if _, ok := cfg.Features["ghcr.io/Devon-White/claude-bunker/hardening:0"]; ok {
		t.Error("hardening feature must be stripped from the engine config (bunker applies it natively)")
	}
	if _, ok := cfg.Features["ghcr.io/devcontainers/features/common-utils:2"]; ok {
		t.Error("common-utils feature must be stripped from the engine config (bunker creates the user natively)")
	}
	if _, ok := cfg.Features["ghcr.io/devcontainers/features/node:1"]; !ok {
		t.Error("user feature must survive")
	}
}

// TestStripBunkerPostStart proves the firewall bootstrap Generate emits into
// postStartCommand strips back out cleanly, leaving only a user's real
// command (if any). This is required for correctness, not just cosmetic:
// bunker's own native RunPostStart already execs the firewall directly as
// root (internal/container/lifecycle.go), and the BAKED allowlist path this
// string targets (container.AllowedDomainsPath) isn't even COPY'd into
// bunker's native image — only the portable Dockerfile written by
// writeDevContainer has it. If this string reached RunPostStart's
// postStartCommand step unstripped, it would sudo-exec the firewall against
// a nonexistent file and fail the whole postStart.
func TestStripBunkerPostStart(t *testing.T) {
	fw := firewallPostStartCommand()
	cases := map[string]string{
		fw:                     "",
		fw + " && npm install": "npm install",
		"npm install":          "npm install",
		"":                     "",
	}
	for in, want := range cases {
		if got := stripBunkerPostStart(in); got != want {
			t.Errorf("stripBunkerPostStart(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestLoadProjectConfig_StripsFirewallPostStart proves the strip is wired
// into LoadProjectConfig itself: loading a Generate-produced file back
// yields only the user's real postStartCommand, not the firewall bootstrap.
func TestLoadProjectConfig_StripsFirewallPostStart(t *testing.T) {
	ws := t.TempDir()
	dir := filepath.Join(ws, ".devcontainer")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := Generate(config.ProjectConfig{PostStartCommand: "npm install"}, GenerateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "devcontainer.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	got, found, err := LoadProjectConfig(ws)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if got.PostStartCommand != "npm install" {
		t.Errorf("PostStartCommand = %q, want the firewall bootstrap stripped, leaving just the user command", got.PostStartCommand)
	}
}

// TestLoadProjectConfig_StripsFirewallPostStartWithNoUserCommand covers the
// common case (no user postStartCommand at all): the generated file still
// always carries the firewall bootstrap, and loading it back must yield an
// empty PostStartCommand so bunker's native RunPostStart doesn't try to
// re-run the firewall against a path that doesn't exist in its own image.
func TestLoadProjectConfig_StripsFirewallPostStartWithNoUserCommand(t *testing.T) {
	ws := t.TempDir()
	dir := filepath.Join(ws, ".devcontainer")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := Generate(config.ProjectConfig{}, GenerateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "devcontainer.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	got, found, err := LoadProjectConfig(ws)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if got.PostStartCommand != "" {
		t.Errorf("PostStartCommand = %q, want empty (firewall bootstrap fully stripped)", got.PostStartCommand)
	}
}
