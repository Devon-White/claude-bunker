package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAllowlist(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "domains.txt", "!api.anthropic.com\ngithub.com\n*.githubusercontent.com\n\n# comment\n")
	al, err := LoadAllowlist(p)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		host string
		want bool
	}{
		{"api.anthropic.com", true}, // critical marker stripped
		{"API.Anthropic.com", true}, // case-insensitive
		{"github.com", true},
		{"raw.githubusercontent.com", true}, // wildcard suffix
		{"githubusercontent.com", false},    // "*." requires a label
		{"evil.com", false},
		{"notgithub.com", false},
		{"", false},
	}
	for _, c := range cases {
		if got := al.Allowed(c.host); got != c.want {
			t.Errorf("Allowed(%q)=%v want %v", c.host, got, c.want)
		}
	}
}
