package container

import (
	"strings"
	"testing"
)

func TestGenerateBaseDockerfile_SudoInstalled(t *testing.T) {
	got := GenerateBaseDockerfile()
	if !strings.Contains(got, "sudo") {
		t.Error("expected apt-get install list to include sudo")
	}
}

func TestGenerateBaseDockerfile_ScopedSudoersGrant(t *testing.T) {
	got := GenerateBaseDockerfile()

	wantGrant := ContainerUser + " ALL=(root) NOPASSWD: " + FirewallScriptPath + " " + AllowedDomainsPath + ", " + RefreshFirewallScriptPath + " " + AllowedDomainsPath
	if !strings.Contains(got, wantGrant) {
		t.Errorf("expected arg-pinned sudoers grant %q in generated Dockerfile, got:\n%s", wantGrant, got)
	}

	if !strings.Contains(got, "init-firewall.sh /etc/claude-bunker/allowed-domains.txt") {
		t.Error("expected sudoers grant to arg-pin init-firewall.sh to /etc/claude-bunker/allowed-domains.txt")
	}

	// The grant must be arg-pinned, not the bare arg-unrestricted form: the
	// allowlist path must appear after the script path in the sudoers line,
	// so sudo only permits this exact invocation and denies any other
	// domains-file argument (or a bare invocation with no argument).
	if scriptIdx, pathIdx := strings.Index(got, FirewallScriptPath), strings.Index(got, AllowedDomainsPath); scriptIdx == -1 || pathIdx == -1 || pathIdx < scriptIdx {
		t.Error("expected sudoers grant to be arg-pinned: allowlist path must appear after the script path")
	}
	if strings.Contains(got, FirewallScriptPath+",") {
		t.Error("sudoers grant must not contain the bare, arg-unrestricted form of init-firewall.sh")
	}

	if !strings.Contains(got, "/etc/sudoers.d/claude-bunker-firewall") {
		t.Error("expected sudoers grant to be written to /etc/sudoers.d/claude-bunker-firewall")
	}

	if !strings.Contains(got, "chmod 0440 /etc/sudoers.d/claude-bunker-firewall") {
		t.Error("expected /etc/sudoers.d/claude-bunker-firewall to be chmod 0440")
	}

	// Must not grant blanket sudo access.
	if strings.Contains(got, "ALL=(ALL) NOPASSWD: ALL") || strings.Contains(got, "ALL=(ALL:ALL) NOPASSWD: ALL") {
		t.Error("sudoers grant must be scoped to firewall scripts, not blanket sudo")
	}
}
