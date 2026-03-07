package sandbox

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Devon-White/claude-bunker/internal/config"
)

func TestExtractPluginDomains_Empty(t *testing.T) {
	domains := ExtractPluginDomains("/nonexistent", "")
	if len(domains) != 0 {
		t.Errorf("expected no domains when plugins disabled, got %v", domains)
	}
}

func TestExtractPluginDomains_Project(t *testing.T) {
	dir := t.TempDir()

	mcpJSON := `{
		"mcpServers": {
			"my-http-server": {
				"url": "https://mcp.example.com/sse"
			},
			"my-stdio-server": {
				"command": "node",
				"args": ["server.js"]
			},
			"another-http": {
				"url": "http://api.other.com:8080/path"
			}
		}
	}`
	if err := os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte(mcpJSON), 0644); err != nil {
		t.Fatal(err)
	}

	domains := ExtractPluginDomains(dir, "project")
	expected := map[string]bool{
		"mcp.example.com": true,
		"api.other.com":   true,
	}
	if len(domains) != len(expected) {
		t.Fatalf("expected %d domains, got %d: %v", len(expected), len(domains), domains)
	}
	for _, d := range domains {
		if !expected[d] {
			t.Errorf("unexpected domain %q", d)
		}
	}
}

func TestExtractPluginDomains_User(t *testing.T) {
	// Create workspace with .mcp.json
	workspace := t.TempDir()
	mcpJSON := `{"mcpServers": {"ws": {"url": "https://ws.example.com/sse"}}}`
	if err := os.WriteFile(filepath.Join(workspace, ".mcp.json"), []byte(mcpJSON), 0644); err != nil {
		t.Fatal(err)
	}

	// Create fake home with .claude.json
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // Windows
	claudeJSON := `{"mcpServers": {"home": {"url": "https://home.example.com/api"}}}`
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(claudeJSON), 0644); err != nil {
		t.Fatal(err)
	}

	domains := ExtractPluginDomains(workspace, "user")
	expected := map[string]bool{
		"ws.example.com":   true,
		"home.example.com": true,
	}
	if len(domains) != len(expected) {
		t.Fatalf("expected %d domains, got %d: %v", len(expected), len(domains), domains)
	}
	for _, d := range domains {
		if !expected[d] {
			t.Errorf("unexpected domain %q", d)
		}
	}
}

func TestExtractPluginDomains_Deduplicates(t *testing.T) {
	workspace := t.TempDir()
	// Same domain in both configs
	mcpJSON := `{"mcpServers": {"s1": {"url": "https://shared.example.com/sse"}}}`
	if err := os.WriteFile(filepath.Join(workspace, ".mcp.json"), []byte(mcpJSON), 0644); err != nil {
		t.Fatal(err)
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(mcpJSON), 0644); err != nil {
		t.Fatal(err)
	}

	domains := ExtractPluginDomains(workspace, "user")
	if len(domains) != 1 {
		t.Errorf("expected 1 deduplicated domain, got %d: %v", len(domains), domains)
	}
}

func TestExtractPluginDomains_InvalidDomainsFiltered(t *testing.T) {
	workspace := t.TempDir()
	// "localhost" is a single-segment domain — should be filtered out
	mcpJSON := `{"mcpServers": {"local": {"url": "http://localhost:3000/sse"}}}`
	if err := os.WriteFile(filepath.Join(workspace, ".mcp.json"), []byte(mcpJSON), 0644); err != nil {
		t.Fatal(err)
	}

	domains := ExtractPluginDomains(workspace, "project")
	if len(domains) != 0 {
		t.Errorf("expected single-segment domain to be filtered, got %v", domains)
	}
}

func TestIsValidDomain(t *testing.T) {
	tests := []struct {
		domain string
		valid  bool
	}{
		{"example.com", true},
		{"api.example.com", true},
		{"*.example.com", true},
		{"localhost", false},
		{"", false},
		{"*.com", false},
		{"*example.com", false},
		{"foo..bar.com", false},
	}
	for _, tt := range tests {
		got := config.IsValidDomain(tt.domain)
		if got != tt.valid {
			t.Errorf("IsValidDomain(%q) = %v, want %v", tt.domain, got, tt.valid)
		}
	}
}

func TestExtractMCPDomains(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")

	// Valid config (nested mcpServers format)
	data := `{"mcpServers": {"s1": {"url": "https://api.example.com/sse"}, "s2": {"command": "node"}}}`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}
	domains := extractMCPDomains(path)
	if len(domains) != 1 || domains[0] != "api.example.com" {
		t.Errorf("expected [api.example.com], got %v", domains)
	}

	// Non-existent file
	domains = extractMCPDomains("/nonexistent/file.json")
	if len(domains) != 0 {
		t.Errorf("expected empty for non-existent file, got %v", domains)
	}

	// Invalid JSON
	if err := os.WriteFile(path, []byte("not json"), 0644); err != nil {
		t.Fatal(err)
	}
	domains = extractMCPDomains(path)
	if len(domains) != 0 {
		t.Errorf("expected empty for invalid JSON, got %v", domains)
	}
}

func TestExtractMCPDomains_FlatFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".mcp.json")

	// Flat format used by marketplace plugin .mcp.json files
	data := `{"github": {"type": "http", "url": "https://api.githubcopilot.com/mcp/"}}`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}
	domains := extractMCPDomains(path)
	if len(domains) != 1 || domains[0] != "api.githubcopilot.com" {
		t.Errorf("expected [api.githubcopilot.com], got %v", domains)
	}
}

func TestExtractPluginCacheDomains(t *testing.T) {
	// Build a fake plugin cache structure:
	// cache/marketplace/plugin/version/.mcp.json
	cacheDir := t.TempDir()
	pluginDir := filepath.Join(cacheDir, "my-marketplace", "my-plugin", "v1")
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		t.Fatal(err)
	}
	mcpJSON := `{"my-server": {"type": "http", "url": "https://api.plugin.com/mcp"}}`
	if err := os.WriteFile(filepath.Join(pluginDir, ".mcp.json"), []byte(mcpJSON), 0644); err != nil {
		t.Fatal(err)
	}

	// Add a stdio-only plugin (no domains expected)
	stdioDir := filepath.Join(cacheDir, "my-marketplace", "stdio-plugin", "v1")
	if err := os.MkdirAll(stdioDir, 0755); err != nil {
		t.Fatal(err)
	}
	stdioJSON := `{"tool": {"command": "node", "args": ["server.js"]}}`
	if err := os.WriteFile(filepath.Join(stdioDir, ".mcp.json"), []byte(stdioJSON), 0644); err != nil {
		t.Fatal(err)
	}

	domains := extractPluginCacheDomains(cacheDir)
	if len(domains) != 1 || domains[0] != "api.plugin.com" {
		t.Errorf("expected [api.plugin.com], got %v", domains)
	}
}

func TestExtractMCPDomainsFromData_BothFormats(t *testing.T) {
	// Nested format (settings.json style)
	nested := []byte(`{"mcpServers": {"s1": {"url": "https://nested.example.com/sse"}}}`)
	domains := extractMCPDomainsFromData(nested)
	if len(domains) != 1 || domains[0] != "nested.example.com" {
		t.Errorf("nested format: expected [nested.example.com], got %v", domains)
	}

	// Flat format (plugin .mcp.json style)
	flat := []byte(`{"s1": {"url": "https://flat.example.com/mcp"}}`)
	domains = extractMCPDomainsFromData(flat)
	if len(domains) != 1 || domains[0] != "flat.example.com" {
		t.Errorf("flat format: expected [flat.example.com], got %v", domains)
	}

	// Stdio-only (no domains)
	stdio := []byte(`{"tool": {"command": "uvx", "args": ["serena"]}}`)
	domains = extractMCPDomainsFromData(stdio)
	if len(domains) != 0 {
		t.Errorf("stdio-only: expected no domains, got %v", domains)
	}
}
