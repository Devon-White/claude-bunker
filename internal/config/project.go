package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Devon-White/claude-bunker/internal/log"
)

// ProjectConfig represents the unified claude-bunker configuration.
// Location: .claude/.claude-bunker/config.json
type ProjectConfig struct {
	Workspace        string                    `json:"workspace"`
	Exclude          []string                  `json:"exclude"`
	AllowDomains     []string                  `json:"allowDomains"`
	Features         map[string]map[string]any `json:"features"`
	Env              map[string]string         `json:"env"`
	OnCreateCommand  string                    `json:"onCreateCommand"`
	PostStartCommand string                    `json:"postStartCommand"`
	GhToken          string                    `json:"ghToken,omitempty"`
	SeedHistory      *bool                     `json:"seedHistory,omitempty"`
	Plugins          string                    `json:"plugins,omitempty"`
}

// Plugin level constants.
const (
	PluginLevelProject = "project"
	PluginLevelUser    = "user"
	PluginLevelAll     = "all"
)

// pluginLevelOrder maps plugin levels to their numeric rank for AtLeast comparisons.
var pluginLevelOrder = map[string]int{
	PluginLevelProject: 1,
	PluginLevelUser:    2,
	PluginLevelAll:     3,
}

// PluginLevel returns the validated plugin level string.
// Returns "" if plugins are disabled (false, omitted, or invalid value).
func (c ProjectConfig) PluginLevel() string {
	if _, ok := pluginLevelOrder[c.Plugins]; ok {
		return c.Plugins
	}
	return ""
}

// PluginLevelAtLeast returns true if level is at least threshold.
// Returns false if level is empty (disabled).
func PluginLevelAtLeast(level, threshold string) bool {
	return pluginLevelOrder[level] >= pluginLevelOrder[threshold]
}

// ShouldSeedHistory returns whether session history should be seeded.
// Defaults to true when not explicitly set.
func (c ProjectConfig) ShouldSeedHistory() bool {
	if c.SeedHistory == nil {
		return true
	}
	return *c.SeedHistory
}

// HasGeneratedLayers returns true if the config requires generating
// additional Dockerfile layers (features or env vars).
func (c ProjectConfig) HasGeneratedLayers() bool {
	return len(c.Features) > 0 || len(c.Env) > 0 || c.OnCreateCommand != ""
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
	expandProjectConfig(&cfg)

	// Warn if ghToken looks like a literal token value rather than an env var reference.
	// This catches accidental credential commits — ghToken should normally be "${GH_TOKEN}" or similar.
	if cfg.GhToken != "" && !strings.Contains(cfg.GhToken, "${") && !strings.HasPrefix(cfg.GhToken, "$") {
		log.Warn("ghToken in config.json appears to be a literal value, not an env var reference (e.g. \"${GH_TOKEN}\"). Avoid committing credentials to version control.")
	}

	// Normalize domain whitespace before validation
	for i, d := range cfg.AllowDomains {
		cfg.AllowDomains[i] = strings.TrimSpace(d)
	}
	if err := validateDomains(cfg.AllowDomains); err != nil {
		return ProjectConfig{}, err
	}
	return cfg, nil
}

// validDomainChars matches only characters that are safe in domain patterns.
// Rejects shell metacharacters (;|$`&<>(){}!#) that could be exploited if
// a domain string flows into shell commands (e.g. firewall scripts).
var validDomainChars = regexp.MustCompile(`^[a-zA-Z0-9.*\-]+$`)

// IsValidDomain checks whether a single domain pattern is valid for firewall
// allowlisting. It rejects empty strings, malformed wildcards, patterns with
// fewer than 2 segments, empty segments, and shell metacharacters.
func IsValidDomain(d string) bool {
	if d == "" {
		return false
	}
	if !validDomainChars.MatchString(d) {
		return false
	}
	if strings.HasPrefix(d, "*") && !strings.HasPrefix(d, "*.") {
		return false
	}
	check := d
	if strings.HasPrefix(check, "*.") {
		check = check[2:]
	}
	segments := strings.Split(check, ".")
	if len(segments) < 2 {
		return false
	}
	for _, seg := range segments {
		if seg == "" {
			return false
		}
	}
	return true
}

// validateDomains checks that domain patterns in allowDomains are reasonable.
func validateDomains(domains []string) error {
	for _, d := range domains {
		if !IsValidDomain(d) {
			if d == "" {
				return fmt.Errorf("allowDomains: empty domain pattern")
			}
			if strings.HasPrefix(d, "*") && !strings.HasPrefix(d, "*.") {
				return fmt.Errorf("allowDomains: pattern %q has invalid wildcard (use %q instead)", d, "*."+d[1:])
			}
			return fmt.Errorf("allowDomains: pattern %q is too broad or contains empty segments", d)
		}
	}
	return nil
}

// NormalizeDomains trims whitespace from each allowDomains entry and validates
// the patterns, returning an error on the first invalid one. Used by the runtime
// read path after ${VAR} expansion.
func NormalizeDomains(cfg *ProjectConfig) error {
	for i, d := range cfg.AllowDomains {
		cfg.AllowDomains[i] = strings.TrimSpace(d)
	}
	return validateDomains(cfg.AllowDomains)
}

// ConfigPath returns the path to .claude/.claude-bunker/config.json in the given workspace.
func ConfigPath(workspace string) string {
	return filepath.Join(workspace, ".claude", ".claude-bunker", "config.json")
}
