package sandbox

import (
	"testing"
)

func TestDetectProxyEnv_Empty(t *testing.T) {
	// Clear any proxy env vars that might be set
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "NO_PROXY", "no_proxy",
		"NODE_EXTRA_CA_CERTS", "CLAUDE_CODE_CLIENT_CERT", "CLAUDE_CODE_CLIENT_KEY", "CLAUDE_CODE_CLIENT_KEY_PASSPHRASE"} {
		t.Setenv(k, "")
	}

	cfg := DetectProxyEnv()
	if cfg.HasProxy() {
		t.Error("expected no proxy when env vars are empty")
	}
	if cfg.HasCerts() {
		t.Error("expected no certs when env vars are empty")
	}
}

func TestDetectProxyEnv_WithProxy(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://proxy.example.com:8080")

	cfg := DetectProxyEnv()
	if !cfg.HasProxy() {
		t.Error("expected proxy to be detected")
	}
	if cfg.HTTPSProxy != "http://proxy.example.com:8080" {
		t.Errorf("HTTPSProxy = %q, want %q", cfg.HTTPSProxy, "http://proxy.example.com:8080")
	}
	if cfg.ProxyHost != "proxy.example.com" {
		t.Errorf("ProxyHost = %q, want %q", cfg.ProxyHost, "proxy.example.com")
	}
}

func TestDetectProxyEnv_LowercaseFallback(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("https_proxy", "http://lower.example.com:3128")

	cfg := DetectProxyEnv()
	if cfg.HTTPSProxy != "http://lower.example.com:3128" {
		t.Errorf("HTTPSProxy = %q, want lowercase fallback", cfg.HTTPSProxy)
	}
}

func TestExtractProxyDomain(t *testing.T) {
	cfg := ProxyConfig{ProxyHost: "proxy.example.com"}
	domains := ExtractProxyDomain(cfg)
	if len(domains) != 1 || domains[0] != "proxy.example.com" {
		t.Errorf("ExtractProxyDomain() = %v, want [proxy.example.com]", domains)
	}

	empty := ExtractProxyDomain(ProxyConfig{})
	if len(empty) != 0 {
		t.Errorf("ExtractProxyDomain(empty) = %v, want []", empty)
	}
}

func TestProxyContainerEnv(t *testing.T) {
	cfg := ProxyConfig{
		HTTPSProxy: "http://proxy.example.com:8080",
		NoProxy:    "localhost,127.0.0.1",
	}
	env := ProxyContainerEnv(cfg)

	if env["HTTPS_PROXY"] != "http://proxy.example.com:8080" {
		t.Errorf("HTTPS_PROXY = %q", env["HTTPS_PROXY"])
	}
	if env["https_proxy"] != "http://proxy.example.com:8080" {
		t.Errorf("https_proxy = %q", env["https_proxy"])
	}
	if env["NO_PROXY"] != "localhost,127.0.0.1" {
		t.Errorf("NO_PROXY = %q", env["NO_PROXY"])
	}
	if _, ok := env["HTTP_PROXY"]; ok {
		t.Error("HTTP_PROXY should not be set when HTTPProxy is empty")
	}
}

func TestExtractHost(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"http://proxy.example.com:8080", "proxy.example.com"},
		{"https://proxy.example.com", "proxy.example.com"},
		{"proxy.example.com:8080", "proxy.example.com"},
		{"192.168.1.1:3128", "192.168.1.1"},
		{"http://192.168.1.1:3128", "192.168.1.1"},
		{"", ""},
	}
	for _, tt := range tests {
		got := extractHost(tt.input)
		if got != tt.want {
			t.Errorf("extractHost(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
