package devcontainer

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Devon-White/claude-bunker/internal/config"
)

// bunkerUser is the forced non-root container user.
const bunkerUser = "claude-bunker"

// bunkerCustomizationsKey is the customizations namespace holding bunker extras.
const bunkerCustomizationsKey = "claude-bunker"

// forcedCaps are the network capabilities bunker's firewall requires; forced
// into capAdd whether bunker generates the file or augments a user-authored one.
var forcedCaps = []string{"NET_ADMIN", "NET_RAW"}

// DevContainer is the subset of devcontainer.json that claude-bunker reads.
type DevContainer struct {
	Name             string                     `json:"name,omitempty"`
	Image            string                     `json:"image,omitempty"`
	Features         map[string]any             `json:"features,omitempty"` // ref → options object (or shorthand)
	CapAdd           []string                   `json:"capAdd,omitempty"`
	SecurityOpt      []string                   `json:"securityOpt,omitempty"`
	RemoteUser       string                     `json:"remoteUser,omitempty"`
	ContainerEnv     map[string]string          `json:"containerEnv,omitempty"`
	OnCreateCommand  json.RawMessage            `json:"onCreateCommand,omitempty"`
	PostStartCommand json.RawMessage            `json:"postStartCommand,omitempty"`
	Customizations   map[string]json.RawMessage `json:"customizations,omitempty"`
}

// bunkerCustomizations is the shape of customizations["claude-bunker"].
type bunkerCustomizations struct {
	Exclude      []string `json:"exclude,omitempty"`
	AllowDomains []string `json:"allowDomains,omitempty"`
	Apt          []string `json:"apt,omitempty"`
	Plugins      string   `json:"plugins,omitempty"`
	GhToken      string   `json:"ghToken,omitempty"`
	SeedHistory  *bool    `json:"seedHistory,omitempty"`
	Workspace    string   `json:"workspace,omitempty"`
}

// Parse preprocesses JSONC + localEnv, then unmarshals into a DevContainer.
func Parse(data []byte, localEnv func(string) (string, bool)) (DevContainer, error) {
	clean := preprocess(data, localEnv)
	var dc DevContainer
	if err := json.Unmarshal(clean, &dc); err != nil {
		return DevContainer{}, fmt.Errorf("parsing devcontainer.json: %w", err)
	}
	return dc, nil
}

// bunkerExtras extracts customizations["claude-bunker"], if present.
func (dc DevContainer) bunkerExtras() bunkerCustomizations {
	var bc bunkerCustomizations
	if raw, ok := dc.Customizations[bunkerCustomizationsKey]; ok {
		_ = json.Unmarshal(raw, &bc)
	}
	return bc
}

// ToProjectConfig maps a DevContainer onto the engine's ProjectConfig.
func ToProjectConfig(dc DevContainer) config.ProjectConfig {
	bc := dc.bunkerExtras()

	features := map[string]map[string]any{}
	for ref, opts := range dc.Features {
		if b, ok := opts.(bool); ok && !b {
			continue // false shorthand: feature explicitly disabled, skip it
		}
		if m, ok := opts.(map[string]any); ok {
			features[ref] = m
		} else {
			features[ref] = map[string]any{}
		}
	}

	return config.ProjectConfig{
		Workspace:        bc.Workspace,
		Exclude:          bc.Exclude,
		AllowDomains:     bc.AllowDomains,
		Features:         features,
		Apt:              bc.Apt,
		Env:              dc.ContainerEnv,
		OnCreateCommand:  commandToString(dc.OnCreateCommand),
		PostStartCommand: commandToString(dc.PostStartCommand),
		GhToken:          bc.GhToken,
		SeedHistory:      bc.SeedHistory,
		Plugins:          bc.Plugins,
	}
}

// commandToString reduces a devcontainer command (string | []string | object)
// to a single shell command. String → itself; []string → joined with " && ";
// object → "" (named-command form has no single-string meaning here).
func commandToString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var arr []string
	if json.Unmarshal(raw, &arr) == nil {
		return strings.Join(arr, " && ")
	}
	return ""
}
