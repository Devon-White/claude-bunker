package devcontainer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Devon-White/claude-bunker/internal/config"
)

// bunkerManagedFeaturePrefixes are OCI feature refs bunker provides itself
// (native install / its own engine) and therefore strips from the engine's
// ProjectConfig on read. They remain in the file only for the portable
// (VS Code / Codespaces) build.
var bunkerManagedFeaturePrefixes = []string{
	"ghcr.io/anthropics/devcontainer-features/claude-code",
	"ghcr.io/Devon-White/claude-bunker/firewall",
	"ghcr.io/Devon-White/claude-bunker/hardening",
}

// DevContainerPath returns the standard devcontainer.json path for a workspace.
func DevContainerPath(workspace string) string {
	return filepath.Join(workspace, ".devcontainer", "devcontainer.json")
}

// stripBunkerFeatures removes bunker-managed features from a features map,
// returning a new map. User features are preserved.
func stripBunkerFeatures(features map[string]map[string]interface{}) map[string]map[string]interface{} {
	out := make(map[string]map[string]interface{}, len(features))
	for ref, opts := range features {
		managed := false
		for _, p := range bunkerManagedFeaturePrefixes {
			if strings.HasPrefix(ref, p) {
				managed = true
				break
			}
		}
		if !managed {
			out[ref] = opts
		}
	}
	return out
}

// LoadProjectConfig reads <workspace>/.devcontainer/devcontainer.json and maps
// it to the engine's ProjectConfig. Returns (zeroConfig, false, nil) when the
// file is absent. For a user-authored file (no GENERATED marker) it still forces
// bunker's security fields; either way it strips bunker-managed features (which
// bunker provides natively) from the engine config.
func LoadProjectConfig(workspace string) (config.ProjectConfig, bool, error) {
	data, err := os.ReadFile(DevContainerPath(workspace))
	if err != nil {
		if os.IsNotExist(err) {
			return config.ProjectConfig{}, false, nil
		}
		return config.ProjectConfig{}, false, err
	}

	dc, err := Parse(data, func(name string) (string, bool) { return os.LookupEnv(name) })
	if err != nil {
		return config.ProjectConfig{}, true, fmt.Errorf("reading %s: %w", DevContainerPath(workspace), err)
	}
	if !IsBunkerGenerated(data) {
		dc = Merge(dc) // user-authored: force security fields into the in-memory spec
	}

	cfg := ToProjectConfig(dc)
	cfg.Features = stripBunkerFeatures(cfg.Features)
	return cfg, true, nil
}
