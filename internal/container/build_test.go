package container

import (
	"archive/tar"
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestStripTrailingUser_RemovesLastUser(t *testing.T) {
	input := "FROM debian:bookworm-slim\nRUN echo hello\nUSER claude-bunker\n"
	got := stripTrailingUser(input)
	want := "FROM debian:bookworm-slim\nRUN echo hello\n"
	if got != want {
		t.Errorf("stripTrailingUser() = %q, want %q", got, want)
	}
}

func TestStripTrailingUser_NoUser(t *testing.T) {
	input := "FROM debian:bookworm-slim\nRUN echo hello\n"
	got := stripTrailingUser(input)
	if got != input {
		t.Errorf("stripTrailingUser() modified input without USER line: %q", got)
	}
}

func TestStripTrailingUser_UserInMiddle(t *testing.T) {
	// USER in middle should NOT be removed (only trailing USER matters)
	input := "FROM debian:bookworm-slim\nUSER root\nRUN echo hello"
	got := stripTrailingUser(input)
	if got != input {
		t.Errorf("stripTrailingUser() should not remove non-trailing USER: %q", got)
	}
}

func TestStripTrailingUser_TrailingWhitespace(t *testing.T) {
	input := "FROM debian:bookworm-slim\nRUN echo hello\nUSER claude-bunker\n\n"
	got := stripTrailingUser(input)
	want := "FROM debian:bookworm-slim\nRUN echo hello\n\n"
	if got != want {
		t.Errorf("stripTrailingUser() = %q, want %q", got, want)
	}
}

func TestStripTrailingUser_CaseInsensitive(t *testing.T) {
	input := "FROM debian:bookworm-slim\nuser root\n"
	got := stripTrailingUser(input)
	want := "FROM debian:bookworm-slim\n"
	if got != want {
		t.Errorf("stripTrailingUser() should handle lowercase USER: got %q, want %q", got, want)
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
