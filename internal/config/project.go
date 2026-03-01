package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ProjectConfig represents the unified claude-bunker configuration.
// Location: .claude/.claude-bunker/config.json
type ProjectConfig struct {
	Workspace         string                            `json:"workspace"`
	Exclude           []string                          `json:"exclude"`
	AllowDomains      []string                          `json:"allowDomains"`
	Features          map[string]map[string]interface{} `json:"features"`
	Apt               []string                          `json:"apt"`
	Env               map[string]string                 `json:"env"`
	PostStartCommand  string                            `json:"postStartCommand"`
	PostCreateCommand string                            `json:"postCreateCommand,omitempty"` // deprecated: use postStartCommand
	GhToken           string                            `json:"ghToken,omitempty"`
	SeedHistory       *bool                             `json:"seedHistory,omitempty"`
}

// ShouldSeedHistory returns whether session history should be seeded.
// Defaults to true for backward compatibility when not explicitly set.
func (c ProjectConfig) ShouldSeedHistory() bool {
	if c.SeedHistory == nil {
		return true
	}
	return *c.SeedHistory
}

// HasGeneratedLayers returns true if the config requires generating
// additional Dockerfile layers (features, apt packages, or env vars).
func (c ProjectConfig) HasGeneratedLayers() bool {
	return len(c.Features) > 0 || len(c.Apt) > 0 || len(c.Env) > 0
}

// LoadProjectConfig reads .claude/.claude-bunker/config.json from the workspace.
// Returns a zero-value config if the file doesn't exist.
func LoadProjectConfig(workspace string) (ProjectConfig, error) {
	p := ConfigPath(workspace)
	data, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ProjectConfig{}, nil
		}
		return ProjectConfig{}, err
	}
	var cfg ProjectConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return ProjectConfig{}, err
	}
	// Normalize domain whitespace before validation
	for i, d := range cfg.AllowDomains {
		cfg.AllowDomains[i] = strings.TrimSpace(d)
	}
	if err := validateDomains(cfg.AllowDomains); err != nil {
		return ProjectConfig{}, err
	}
	// Backward compat: postCreateCommand -> postStartCommand
	if cfg.PostStartCommand == "" && cfg.PostCreateCommand != "" {
		cfg.PostStartCommand = cfg.PostCreateCommand
		cfg.PostCreateCommand = ""
	}
	return cfg, nil
}

// validateDomains checks that domain patterns in allowDomains are reasonable.
// It rejects empty strings, patterns with fewer than 2 domain segments
// (which would be overly broad, e.g. "*.com", "*.org", "*"), and malformed
// wildcard prefixes. Single-segment domains like "localhost" are intentionally
// rejected — the firewall resolves domains to IPs, and single-segment names
// are too broad for a security allowlist.
func validateDomains(domains []string) error {
	for _, d := range domains {
		if d == "" {
			return fmt.Errorf("allowDomains: empty domain pattern")
		}

		// Wildcards must use the "*.domain" form (e.g. "*.github.com").
		// Reject malformed wildcards like "*github.com" (missing dot).
		if strings.HasPrefix(d, "*") && !strings.HasPrefix(d, "*.") {
			return fmt.Errorf("allowDomains: pattern %q has invalid wildcard (use %q instead)", d, "*."+d[1:])
		}

		// Strip leading wildcard prefix for segment counting
		check := d
		if strings.HasPrefix(check, "*.") {
			check = check[2:]
		}

		// After stripping "*.", we need at least 2 segments (e.g. "example.com")
		// to prevent overly broad patterns like "*.com"
		segments := strings.Split(check, ".")
		if len(segments) < 2 {
			return fmt.Errorf("allowDomains: pattern %q is too broad (need at least 2 domain segments)", d)
		}

		// Reject segments that are empty (e.g. "foo..bar")
		for _, seg := range segments {
			if seg == "" {
				return fmt.Errorf("allowDomains: pattern %q contains empty segment", d)
			}
		}
	}
	return nil
}

// ConfigPath returns the path to .claude/.claude-bunker/config.json in the given workspace.
func ConfigPath(workspace string) string {
	return filepath.Join(workspace, ".claude", ".claude-bunker", "config.json")
}
