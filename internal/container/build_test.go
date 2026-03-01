package container

import (
	"archive/tar"
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestGenerateBaseContent_NoTrailingUser(t *testing.T) {
	content := generateBaseContent()
	lines := strings.Split(strings.TrimSpace(content), "\n")
	lastNonEmpty := ""
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			lastNonEmpty = strings.TrimSpace(lines[i])
			break
		}
	}
	if strings.HasPrefix(strings.ToUpper(lastNonEmpty), "USER ") {
		t.Errorf("generateBaseContent() should not end with USER, got %q", lastNonEmpty)
	}
}

func TestGenerateBaseDockerfile_ContainsKeyElements(t *testing.T) {
	df := GenerateBaseDockerfile()

	mustContain := []struct {
		name    string
		content string
	}{
		{"base image", "FROM debian:bookworm-slim"},
		{"user creation", "useradd"},
		{"apt-get install", "apt-get install"},
		{"curl present", "curl"},
		{"bubblewrap", "bubblewrap"},
		{"iptables", "iptables"},
		{"firewall COPY", "COPY init-firewall.sh"},
		{"tmux COPY", "COPY tmux.conf"},
		{"zshrc COPY", "COPY zshrc"},
		{"managed-settings dir", "/etc/claude-code"},
		{"Claude Code install", "claude.ai/install.sh"},
		{"USER claude-bunker", "USER claude-bunker"},
		{"DEVCONTAINER env", "DEVCONTAINER=true"},
		{"zsh shell", "SHELL=/bin/zsh"},
	}

	for _, c := range mustContain {
		if !strings.Contains(df, c.content) {
			t.Errorf("Dockerfile missing %s: expected to find %q", c.name, c.content)
		}
	}

	// Verify wget is NOT in apt-get install
	if strings.Contains(df, "wget") {
		t.Error("Dockerfile should not contain wget (optimization: only curl is needed)")
	}

	// Verify Oh My Zsh / zsh-in-docker is not used
	if strings.Contains(df, "zsh-in-docker") {
		t.Error("Dockerfile should not use zsh-in-docker (replaced with minimal .zshrc)")
	}
}

func TestGenerateBaseDockerfile_TZAfterAptGet(t *testing.T) {
	df := GenerateBaseDockerfile()

	aptIdx := strings.Index(df, "apt-get install")
	tzIdx := strings.Index(df, "ARG TZ=")

	if aptIdx < 0 || tzIdx < 0 {
		t.Fatal("Dockerfile missing apt-get install or ARG TZ")
	}

	if tzIdx < aptIdx {
		t.Error("ARG TZ should appear AFTER apt-get install to prevent cache busting")
	}
}

func TestGenerateBaseDockerfile_SingleUserSwitch(t *testing.T) {
	df := GenerateBaseDockerfile()

	// Count USER claude-bunker occurrences — should be exactly 2:
	// one before zsh/Claude installs, one at the end after COPY layers
	count := strings.Count(df, "USER claude-bunker")
	if count != 2 {
		t.Errorf("expected exactly 2 'USER claude-bunker' lines, got %d", count)
	}
}

func TestBuildContextTar_ContainsExpectedFiles(t *testing.T) {
	df := "FROM debian:bookworm-slim\n"
	buf, err := buildContextTar(df, nil)
	if err != nil {
		t.Fatal(err)
	}

	entries := tarEntryNames(t, buf)
	expected := map[string]bool{
		"Dockerfile":       false,
		"init-firewall.sh": false,
		"tmux.conf":        false,
		"zshrc":            false,
	}

	for _, name := range entries {
		if _, ok := expected[name]; ok {
			expected[name] = true
		}
	}

	for name, found := range expected {
		if !found {
			t.Errorf("tar archive missing expected file: %s", name)
		}
	}
}

func TestBaseImageRef_DevReturnsEmpty(t *testing.T) {
	tests := []struct {
		version string
		want    string
	}{
		{"", ""},
		{"dev", ""},
		{"0.3.0", BaseImageRegistry + ":v0.3.0"},
		{"1.0.0", BaseImageRegistry + ":v1.0.0"},
	}

	for _, tt := range tests {
		got := BaseImageRef(tt.version)
		if got != tt.want {
			t.Errorf("BaseImageRef(%q) = %q, want %q", tt.version, got, tt.want)
		}
	}
}

func TestBaseImageRef_Registry(t *testing.T) {
	ref := BaseImageRef("0.3.0")
	if !strings.HasPrefix(ref, "ghcr.io/") {
		t.Errorf("BaseImageRef should use GHCR registry, got %q", ref)
	}
}

// tarEntryNames reads a tar archive from a buffer and returns all entry names.
func tarEntryNames(t *testing.T, buf *bytes.Buffer) []string {
	t.Helper()
	tr := tar.NewReader(bytes.NewReader(buf.Bytes()))
	var names []string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("reading tar entry: %v", err)
		}
		names = append(names, hdr.Name)
	}
	return names
}
