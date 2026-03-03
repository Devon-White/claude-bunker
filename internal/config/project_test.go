package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadProjectConfig_Missing(t *testing.T) {
	cfg, err := LoadProjectConfig(t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Workspace != "" || len(cfg.Exclude) != 0 || len(cfg.AllowDomains) != 0 {
		t.Errorf("expected zero-value config, got %+v", cfg)
	}
}

func TestLoadProjectConfig_Valid(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".claude", ".claude-bunker")
	os.MkdirAll(cfgDir, 0755)
	data := `{
		"workspace": "src",
		"exclude": ["secrets/", ".env.production"],
		"allowDomains": ["private-registry.company.com"],
		"features": {
			"python": {"version": "3.12"},
			"node": {"version": "20"}
		},
		"env": {"PYTHONDONTWRITEBYTECODE": "1"},
		"postStartCommand": "pip install -r requirements.txt"
	}`
	os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(data), 0644)

	cfg, err := LoadProjectConfig(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Workspace != "src" {
		t.Errorf("workspace = %q, want %q", cfg.Workspace, "src")
	}
	if len(cfg.Exclude) != 2 {
		t.Errorf("exclude len = %d, want 2", len(cfg.Exclude))
	}
	if len(cfg.AllowDomains) != 1 || cfg.AllowDomains[0] != "private-registry.company.com" {
		t.Errorf("allowDomains = %v", cfg.AllowDomains)
	}
	if len(cfg.Features) != 2 {
		t.Errorf("features len = %d, want 2", len(cfg.Features))
	}
	if cfg.Env["PYTHONDONTWRITEBYTECODE"] != "1" {
		t.Errorf("env PYTHONDONTWRITEBYTECODE = %q", cfg.Env["PYTHONDONTWRITEBYTECODE"])
	}
	if cfg.PostStartCommand != "pip install -r requirements.txt" {
		t.Errorf("PostStartCommand = %q, want %q", cfg.PostStartCommand, "pip install -r requirements.txt")
	}
}

func TestLoadProjectConfig_Invalid(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".claude", ".claude-bunker")
	os.MkdirAll(cfgDir, 0755)
	os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte("not json"), 0644)

	_, err := LoadProjectConfig(dir)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestValidateDomains(t *testing.T) {
	valid := [][]string{
		{"example.com"},
		{"private-registry.company.com"},
		{"*.example.com"},
		{"api.internal.corp.net"},
		{"*.registry.io"},
	}
	for _, domains := range valid {
		if err := validateDomains(domains); err != nil {
			t.Errorf("validateDomains(%v) unexpected error: %v", domains, err)
		}
	}

	invalid := []struct {
		domains []string
		desc    string
	}{
		{[]string{""}, "empty string"},
		{[]string{"*.com"}, "too broad wildcard"},
		{[]string{"*.org"}, "too broad wildcard TLD"},
		{[]string{"*"}, "bare wildcard"},
		{[]string{"com"}, "single segment"},
		{[]string{"localhost"}, "single segment localhost"},
		{[]string{"foo..bar"}, "empty segment"},
		{[]string{"good.example.com", ""}, "second entry empty"},
		{[]string{"*github.com"}, "malformed wildcard missing dot"},
		{[]string{"*example.org"}, "malformed wildcard missing dot"},
	}
	for _, tt := range invalid {
		if err := validateDomains(tt.domains); err == nil {
			t.Errorf("validateDomains(%v) should have failed (%s)", tt.domains, tt.desc)
		}
	}
}
