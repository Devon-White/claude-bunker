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
// ProjectConfig on read. Generate no longer emits any of these into the
// devcontainer.json it writes (the committed Dockerfile bakes in the
// firewall/hardening and installs Claude Code + creates the user natively —
// see internal/devcontainer/generate.go), so in practice this list is now a
// defensive no-op: it only matters if a user hand-adds one of these refs to
// their own devcontainer.json, in which case it's still stripped rather than
// double-applied.
var bunkerManagedFeaturePrefixes = []string{
	"ghcr.io/anthropics/devcontainer-features/claude-code",
	"ghcr.io/Devon-White/claude-bunker/firewall",
	"ghcr.io/Devon-White/claude-bunker/hardening",
	"ghcr.io/devcontainers/features/common-utils",
}

// DevContainerPath returns the standard devcontainer.json path for a workspace.
func DevContainerPath(workspace string) string {
	return filepath.Join(workspace, ".devcontainer", "devcontainer.json")
}

// stripBunkerFeatures removes bunker-managed features from a features map,
// returning a new map. User features are preserved.
func stripBunkerFeatures(features map[string]map[string]any) map[string]map[string]any {
	out := make(map[string]map[string]any, len(features))
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
	cfg.PostStartCommand = stripBunkerPostStart(cfg.PostStartCommand)
	return cfg, true, nil
}

// stripBunkerPostStart removes the firewall bootstrap Generate always emits
// into postStartCommand (see firewallPostStartCommand in generate.go),
// leaving only a user's own postStartCommand (if any). This is required for
// correctness: bunker's own native RunPostStart already execs the firewall
// directly as root (internal/container/lifecycle.go), and the BAKED
// allowlist path the bootstrap targets isn't even present in bunker's native
// image (only the portable Dockerfile COPYs it in). Without this strip,
// bunker's own postStart step would try to re-run the firewall against a
// nonexistent file and fail outright.
func stripBunkerPostStart(cmd string) string {
	fw := firewallPostStartCommand()
	if cmd == fw {
		return ""
	}
	if rest, ok := strings.CutPrefix(cmd, fw+" && "); ok {
		return rest
	}
	return cmd
}
