package container

import (
	"strings"
	"testing"
)

func TestInitFirewallStartsProxyAndRedirects(t *testing.T) {
	s := string(initFirewallScript)
	for _, want := range []string{
		ProxyBinaryPath,
		"--dport 443 -m owner --uid-owner 1001 -j RETURN",
		"--dport 443 -j REDIRECT --to-ports 15443",
		"runuser -u bunker-proxy",
		"--resolve",   // the domain-fronting self-test
		"HTTPS_PROXY", // interop guard: stand down the SNI layer when an upstream proxy is set
	} {
		if !strings.Contains(s, want) {
			t.Errorf("init-firewall.sh missing %q", want)
		}
	}
}

// TestInitFirewallInstallsCAAtSystemPath guards against the hardcoded system
// CA path in init-firewall.sh drifting from the Go-side ProxyCASystemPath
// constant that createAuthWrapper points NODE_EXTRA_CA_CERTS at. If these two
// ever disagree, the agent's NODE_EXTRA_CA_CERTS would point at a path the
// firewall script never installs the CA to, breaking every terminated
// api.anthropic.com request with an unknown-CA error.
func TestInitFirewallInstallsCAAtSystemPath(t *testing.T) {
	s := string(initFirewallScript)
	if !strings.Contains(s, ProxyCASystemPath) {
		t.Errorf("init-firewall.sh does not install the CA at ProxyCASystemPath (%q); it and createAuthWrapper's NODE_EXTRA_CA_CERTS export have drifted", ProxyCASystemPath)
	}
}
