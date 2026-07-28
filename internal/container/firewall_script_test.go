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
