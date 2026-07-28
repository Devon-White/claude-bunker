package container

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/docker/docker/client"
)

// maskRuleJSON is the on-disk shape the egress proxy reads (mirrors
// egressproxy.MaskRule; kept as a local type to avoid importing package main).
type maskRuleJSON struct {
	Sentinel string   `json:"sentinel"`
	Secret   string   `json:"secret"`
	Hosts    []string `json:"hosts"`
	Headers  []string `json:"headers"`
}

// randToken returns n hex chars of CSPRNG output.
func randToken(n int) string {
	b := make([]byte, n/2)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand: " + err.Error())
	}
	const hexd = "0123456789abcdef"
	out := make([]byte, n)
	for i, x := range b {
		out[i*2] = hexd[x>>4]
		out[i*2+1] = hexd[x&0x0f]
	}
	return string(out)
}

// BuildMaskRules produces the proxy masking rules (holding the REAL secrets)
// and the sentinel-substituted AuthTokens to hand the agent. Sentinels are
// format-preserving so clients don't reject them on shape.
func BuildMaskRules(auth AuthTokens) ([]maskRuleJSON, AuthTokens) {
	if !auth.HasSecrets() {
		return nil, AuthTokens{}
	}
	var rules []maskRuleJSON
	sent := AuthTokens{}
	if auth.ApiKey != "" {
		s := "sk-ant-" + randToken(32)
		sent.ApiKey = s
		rules = append(rules, maskRuleJSON{Sentinel: s, Secret: auth.ApiKey,
			Hosts: []string{"api.anthropic.com"}, Headers: []string{"x-api-key", "authorization"}})
	}
	if auth.OAuthToken != "" {
		s := "sk-ant-oat-" + randToken(32)
		sent.OAuthToken = s
		rules = append(rules, maskRuleJSON{Sentinel: s, Secret: auth.OAuthToken,
			Hosts: []string{"api.anthropic.com"}, Headers: []string{"authorization"}})
	}
	if auth.GhToken != "" {
		s := "ghp_" + randToken(36)
		sent.GhToken = s
		rules = append(rules, maskRuleJSON{Sentinel: s, Secret: auth.GhToken,
			Hosts: []string{"github.com", "api.github.com"}, Headers: []string{"authorization"}})
	}
	return rules, sent
}

// PrepareMasking writes the proxy masking config (real secrets) into a
// bunker-proxy-owned dir before the firewall/proxy start. No-op if no secrets.
func PrepareMasking(ctx context.Context, cli *client.Client, containerID string, auth AuthTokens) (AuthTokens, error) {
	rules, sentinels := BuildMaskRules(auth)
	if len(rules) == 0 {
		return auth, nil // nothing to mask
	}
	blob, err := json.Marshal(rules)
	if err != nil {
		return auth, err
	}
	// Create the config + CA dirs BEFORE writing the config file: neither
	// directory is created by the image build (only ManagedSettingsDir is),
	// and CopyContentToContainer writes via a shell redirection
	// (`> path`) which errors if the parent directory doesn't exist yet —
	// unlike `cp`/COPY, it does not create missing parents.
	if _, err := ExecNonInteractive(ctx, cli, containerID, RootUser,
		[]string{"mkdir", "-p", ProxyConfigDir, ProxyCADir}); err != nil {
		return auth, fmt.Errorf("creating proxy config dir: %w", err)
	}
	if err := CopyContentToContainer(ctx, cli, containerID, blob, MaskingConfigPath); err != nil {
		return auth, fmt.Errorf("writing masking config: %w", err)
	}
	// Lock the config + CA dir to the proxy user (agent uid 1000 cannot read).
	var script strings.Builder
	script.WriteString("set -e\n")
	fmt.Fprintf(&script, "chown -R %d:%d %s\n", ProxyUID, ProxyGID, ProxyConfigDir)
	fmt.Fprintf(&script, "chmod 0400 %s\n", MaskingConfigPath)
	fmt.Fprintf(&script, "chmod 0700 %s %s\n", ProxyConfigDir, ProxyCADir)
	if _, err := ExecNonInteractive(ctx, cli, containerID, RootUser, []string{"sh", "-c", script.String()}); err != nil {
		return auth, fmt.Errorf("locking masking config: %w", err)
	}
	return sentinels, nil
}
