package devcontainer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStripBunkerFeatures(t *testing.T) {
	in := map[string]map[string]interface{}{
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
	in := map[string]map[string]interface{}{
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
