// Package features_test drift-tests the firewall OCI Dev Container Feature
// package against its canonical sources in internal/container. The Feature's
// scripts and builtin domain list are generated (see cmd/genfeatures) rather
// than hand-copied, so this test fails CI if the packaged copy ever diverges
// from the source of truth — the portable VS Code/Codespaces path must never
// silently become weaker than bunker's native firewall.
package features_test

import (
	"os"
	"strings"
	"testing"

	"github.com/Devon-White/claude-bunker/internal/container"
)

func TestFirewallFeatureScriptsMatchCanonical(t *testing.T) {
	for _, name := range []string{"init-firewall.sh", "refresh-firewall.sh", "firewall-common.sh"} {
		canonical, err := os.ReadFile("../internal/container/scripts/" + name)
		if err != nil {
			t.Fatal(err)
		}
		packaged, err := os.ReadFile("src/firewall/" + name)
		if err != nil {
			t.Fatalf("feature script missing (run `go run ./cmd/genfeatures`): %v", err)
		}
		if string(canonical) != string(packaged) {
			t.Errorf("%s drift: features/src/firewall/%s != internal/container/scripts/%s — regenerate with `go run ./cmd/genfeatures`", name, name, name)
		}
	}
}

func TestFirewallFeatureBuiltinDomainsMatchCanonical(t *testing.T) {
	packaged, err := os.ReadFile("src/firewall/builtin-domains.txt")
	if err != nil {
		t.Fatalf("builtin-domains.txt missing (run `go run ./cmd/genfeatures`): %v", err)
	}
	want := strings.Join(container.BuiltinDomains(), "\n") + "\n"
	if string(packaged) != want {
		t.Errorf("builtin-domains.txt drift vs container.BuiltinDomains() — regenerate with `go run ./cmd/genfeatures`")
	}
}
