package config

import (
	"strings"
	"testing"
)

func TestContainerName_Format(t *testing.T) {
	name := ContainerName("/home/user/my-project")

	if !strings.HasPrefix(name, "claude-bunker-my-project-") {
		t.Errorf("expected prefix 'claude-bunker-my-project-', got %q", name)
	}
	// 8-char hex hash suffix
	parts := strings.Split(name, "-")
	hash := parts[len(parts)-1]
	if len(hash) != 8 {
		t.Errorf("expected 8-char hash suffix, got %q (%d chars)", hash, len(hash))
	}
}

func TestContainerName_Deterministic(t *testing.T) {
	a := ContainerName("/home/user/my-project")
	b := ContainerName("/home/user/my-project")
	if a != b {
		t.Errorf("same path gave different names: %q vs %q", a, b)
	}
}

func TestContainerName_DifferentPaths(t *testing.T) {
	a := ContainerName("/home/user/projects/my-app")
	b := ContainerName("/home/user/worktrees/my-app")
	if a == b {
		t.Errorf("different paths should produce different names, both got %q", a)
	}
	// Both should share the same basename prefix
	if !strings.Contains(a, "my-app") || !strings.Contains(b, "my-app") {
		t.Errorf("both should contain 'my-app': %q, %q", a, b)
	}
}

func TestContainerName_Sanitization(t *testing.T) {
	tests := []struct {
		workspace  string
		wantPrefix string
	}{
		{"/home/user/MyProject", "claude-bunker-myproject-"},
		{"/home/user/my project", "claude-bunker-my-project-"},
		{"/home/user/my___project!!!", "claude-bunker-my-project-"},
		{"/home/user/UPPER-case-MIX", "claude-bunker-upper-case-mix-"},
		{"/home/user/---leading-trailing---", "claude-bunker-leading-trailing-"},
	}

	for _, tt := range tests {
		t.Run(tt.workspace, func(t *testing.T) {
			got := ContainerName(tt.workspace)
			if !strings.HasPrefix(got, tt.wantPrefix) {
				t.Errorf("ContainerName(%q) = %q, want prefix %q", tt.workspace, got, tt.wantPrefix)
			}
		})
	}
}

func TestBashHistoryVolume(t *testing.T) {
	got := BashHistoryVolume("claude-bunker-myproject-abcd1234")
	want := "claude-code-bashhistory-claude-bunker-myproject-abcd1234"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestClaudeConfigVolume(t *testing.T) {
	got := ClaudeConfigVolume("claude-bunker-myproject-abcd1234")
	want := "claude-code-config-claude-bunker-myproject-abcd1234"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestImageTag(t *testing.T) {
	got := ImageTag("claude-bunker-myproject-abcd1234")
	want := "claude-bunker:claude-bunker-myproject-abcd1234"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
