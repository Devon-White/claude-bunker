package config

import (
	"testing"
)

func TestExpandEnvVars(t *testing.T) {
	// Set up test env vars
	t.Setenv("CB_TEST_USER", "alice")
	t.Setenv("CB_TEST_HOST", "example.com")
	t.Setenv("CB_TEST_EMPTY", "")

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"plain text", "hello world", "hello world"},
		{"empty string", "", ""},
		{"simple $VAR", "user=$CB_TEST_USER", "user=alice"},
		{"braced ${VAR}", "user=${CB_TEST_USER}", "user=alice"},
		{"multiple vars", "$CB_TEST_USER@$CB_TEST_HOST", "alice@example.com"},
		{"braced multiple", "${CB_TEST_USER}@${CB_TEST_HOST}", "alice@example.com"},
		{"unset var empty", "val=$CB_TEST_UNSET_XYZ", "val="},
		{"unset braced empty", "val=${CB_TEST_UNSET_XYZ}", "val="},
		{"default on unset", "${CB_TEST_UNSET_XYZ:-fallback}", "fallback"},
		{"default on empty", "${CB_TEST_EMPTY:-fallback}", "fallback"},
		{"default not used when set", "${CB_TEST_USER:-fallback}", "alice"},
		{"default with colons", "${CB_TEST_UNSET_XYZ:-host:port}", "host:port"},
		{"empty default", "${CB_TEST_UNSET_XYZ:-}", ""},
		{"dollar escape", "price=$$100", "price=$100"},
		{"dollar at end", "trail$", "trail$"},
		{"unterminated brace", "val=${CB_TEST_USER", "val=${CB_TEST_USER"},
		{"dollar digit literal", "arg$1", "arg$1"},
		{"dollar space literal", "$ sign", "$ sign"},
		{"underscore var", "$CB_TEST_USER ok", "alice ok"},
		{"adjacent braces", "${CB_TEST_USER}${CB_TEST_HOST}", "aliceexample.com"},
		{"no vars", "just plain text", "just plain text"},
		{"var at start", "$CB_TEST_USER end", "alice end"},
		{"var at end", "start $CB_TEST_USER", "start alice"},
		{"only var", "$CB_TEST_USER", "alice"},
		{"only braced var", "${CB_TEST_USER}", "alice"},
		{"double dollar mid", "a$$b", "a$b"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := expandEnvVars(tt.input)
			if got != tt.want {
				t.Errorf("expandEnvVars(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestExpandProjectConfig(t *testing.T) {
	t.Setenv("CB_TOKEN", "ghp_secret123")
	t.Setenv("CB_DOMAIN", "registry.example.com")
	t.Setenv("CB_PY_VER", "3.12")

	cfg := ProjectConfig{
		Workspace:        "${CB_WORKSPACE:-src}",
		PostStartCommand: "echo $CB_TOKEN",
		GhToken:          "${CB_TOKEN}",
		Exclude:          []string{"${CB_EXCL:-secrets/}"},
		AllowDomains:     []string{"${CB_DOMAIN}"},
		Env:              map[string]string{"API_KEY": "${CB_TOKEN}"},
		Features: map[string]map[string]any{
			"python": {"version": "${CB_PY_VER}", "count": 3.0},
		},
	}

	expandProjectConfig(&cfg)

	if cfg.Workspace != "src" {
		t.Errorf("Workspace = %q, want %q", cfg.Workspace, "src")
	}
	if cfg.PostStartCommand != "echo ghp_secret123" {
		t.Errorf("PostStartCommand = %q, want %q", cfg.PostStartCommand, "echo ghp_secret123")
	}
	if cfg.GhToken != "ghp_secret123" {
		t.Errorf("GhToken = %q, want %q", cfg.GhToken, "ghp_secret123")
	}
	if len(cfg.Exclude) != 1 || cfg.Exclude[0] != "secrets/" {
		t.Errorf("Exclude = %v, want [secrets/]", cfg.Exclude)
	}
	if len(cfg.AllowDomains) != 1 || cfg.AllowDomains[0] != "registry.example.com" {
		t.Errorf("AllowDomains = %v, want [registry.example.com]", cfg.AllowDomains)
	}
	if cfg.Env["API_KEY"] != "ghp_secret123" {
		t.Errorf("Env[API_KEY] = %q, want %q", cfg.Env["API_KEY"], "ghp_secret123")
	}
	if v, ok := cfg.Features["python"]["version"].(string); !ok || v != "3.12" {
		t.Errorf("Features[python][version] = %v, want %q", cfg.Features["python"]["version"], "3.12")
	}
	// Non-string feature values are untouched
	if v, ok := cfg.Features["python"]["count"].(float64); !ok || v != 3.0 {
		t.Errorf("Features[python][count] = %v, want 3.0", cfg.Features["python"]["count"])
	}
}
