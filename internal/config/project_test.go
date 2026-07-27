package config

import (
	"testing"
)

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

func TestNormalizeDomains(t *testing.T) {
	// Trims whitespace before validating.
	cfg := ProjectConfig{AllowDomains: []string{"  example.com  ", "*.registry.io"}}
	if err := NormalizeDomains(&cfg); err != nil {
		t.Fatalf("NormalizeDomains unexpected error: %v", err)
	}
	if cfg.AllowDomains[0] != "example.com" {
		t.Errorf("AllowDomains[0] = %q, want trimmed %q", cfg.AllowDomains[0], "example.com")
	}

	// Rejects invalid patterns (fail-closed on the runtime read path).
	for _, bad := range [][]string{{"*"}, {"a b.com"}} {
		cfg := ProjectConfig{AllowDomains: bad}
		if err := NormalizeDomains(&cfg); err == nil {
			t.Errorf("NormalizeDomains(%v) should have failed", bad)
		}
	}
}
