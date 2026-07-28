package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
)

// MaskRule maps a per-session sentinel token to its real value and the hosts
// where the real value may be injected. Present only in the bunker-CLI (Tier 2)
// path; absent => the proxy runs Tier 1 (splice) only.
type MaskRule struct {
	Sentinel string   `json:"sentinel"`
	Secret   string   `json:"secret"`
	Hosts    []string `json:"hosts"`
	Headers  []string `json:"headers"`
}

// Config is the proxy's runtime configuration, populated from flags in main.go.
type Config struct {
	ListenAddr    string
	AllowlistPath string
	MaskingPath   string
	CADir         string
}

// LoadMasking reads the masking rules JSON. A missing file is not an error —
// it means Tier 1 (no termination). Returns nil rules in that case.
func LoadMasking(path string) ([]MaskRule, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rules []MaskRule
	if err := json.Unmarshal(data, &rules); err != nil {
		return nil, err
	}
	return rules, nil
}
