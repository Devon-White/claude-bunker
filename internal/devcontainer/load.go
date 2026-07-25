package devcontainer

import (
	"errors"
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
			if ref == p || strings.HasPrefix(ref, p+":") || strings.HasPrefix(ref, p+"@") {
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
// file is absent. The returned config maps the file's fields and strips bunker-managed
// features (which bunker provides natively). Bunker's capAdd/remoteUser enforcement
// happens at container creation time in internal/container, not here.
func LoadProjectConfig(workspace string) (config.ProjectConfig, bool, error) {
	path := DevContainerPath(workspace)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return config.ProjectConfig{}, false, nil
		}
		return config.ProjectConfig{}, false, err
	}

	dc, err := Parse(data, os.LookupEnv)
	if err != nil {
		return config.ProjectConfig{}, true, fmt.Errorf("reading %s: %w", path, err)
	}

	cfg := ToProjectConfig(dc)
	cfg.Features = stripBunkerFeatures(cfg.Features)
	return cfg, true, nil
}
