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

	wantGrant := ContainerUser + " ALL=(root) NOPASSWD: " + FirewallScriptPath + ", " + RefreshFirewallScriptPath
	if !strings.Contains(got, wantGrant) {
		t.Errorf("expected scoped sudoers grant %q in generated Dockerfile, got:\n%s", wantGrant, got)
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
