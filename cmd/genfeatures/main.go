// Command genfeatures derives the portable artifacts for the firewall OCI
// Dev Container Feature (features/src/firewall/) from claude-bunker's
// canonical sources: the embedded firewall scripts (internal/container) and
// container.BuiltinDomains(). It never hand-copies anything, so the Feature
// package cannot silently drift from — or fall behind — bunker's native
// firewall. features/firewall_drift_test.go enforces this invariant in CI.
//
// Run via `go generate ./...` (see generate.go) or directly:
//
//	go run ./cmd/genfeatures
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Devon-White/claude-bunker/internal/container"
)

// firewallDir is where the derived Feature artifacts are written.
const firewallDir = "features/src/firewall"

// firewallScriptNames are the canonical scripts copied verbatim into the
// Feature package. Keep in sync with features/firewall_drift_test.go.
var firewallScriptNames = map[string]bool{
	"firewall-common.sh":  true,
	"init-firewall.sh":    true,
	"refresh-firewall.sh": true,
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Feature artifacts generated in", firewallDir)
}

func run() error {
	if err := os.MkdirAll(firewallDir, 0755); err != nil {
		return fmt.Errorf("creating output dir: %w", err)
	}

	// Copy the canonical firewall scripts verbatim (same embedded bytes
	// genbuild uses for the Docker image build context), byte-for-byte.
	for _, f := range container.BuildContextScripts() {
		if !firewallScriptNames[f.Name] {
			continue
		}
		if err := os.WriteFile(filepath.Join(firewallDir, f.Name), f.Content, f.Mode); err != nil {
			return fmt.Errorf("writing %s: %w", f.Name, err)
		}
	}

	// Write the builtin domain allowlist from the same Go source of truth
	// that seeds /tmp/.bunker-domains in the native (Docker) path.
	domains := strings.Join(container.BuiltinDomains(), "\n") + "\n"
	if err := os.WriteFile(filepath.Join(firewallDir, "builtin-domains.txt"), []byte(domains), 0644); err != nil {
		return fmt.Errorf("writing builtin-domains.txt: %w", err)
	}

	return nil
}
