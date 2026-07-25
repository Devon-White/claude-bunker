package devcontainer

import (
	"encoding/json"
	"fmt"

	"github.com/Devon-White/claude-bunker/internal/config"
)

// DevContainer is the subset of devcontainer.json that claude-bunker reads.
type DevContainer struct {
	Name             string                     `json:"name,omitempty"`
	Image            string                     `json:"image,omitempty"`
	Features         map[string]interface{}     `json:"features,omitempty"` // ref → options object (or shorthand)
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
	if raw, ok := dc.Customizations["claude-bunker"]; ok {
		_ = json.Unmarshal(raw, &bc)
	}
	return bc
}

// ToProjectConfig maps a DevContainer onto the engine's ProjectConfig.
func ToProjectConfig(dc DevContainer) config.ProjectConfig {
	bc := dc.bunkerExtras()

	features := map[string]map[string]interface{}{}
	for ref, opts := range dc.Features {
		if m, ok := opts.(map[string]interface{}); ok {
			features[ref] = m
		} else {
			features[ref] = map[string]interface{}{}
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
		return joinCommands(arr)
	}
	return ""
}

func joinCommands(cmds []string) string {
	out := ""
	for i, c := range cmds {
		if i > 0 {
			out += " && "
		}
		out += c
	}
	return out
}

// contains reports whether a string slice contains a value.
func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
