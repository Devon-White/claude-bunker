package container

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEnvContainsProxy(t *testing.T) {
	cases := []struct {
		name string
		env  []string
		want bool
	}{
		{"none", []string{"PATH=/usr/bin", "DEVCONTAINER=true"}, false},
		{"https upper set", []string{"HTTPS_PROXY=http://proxy:3128"}, true},
		{"https lower set", []string{"https_proxy=http://proxy:3128"}, true},
		{"https set empty", []string{"HTTPS_PROXY="}, false},
		{"only http proxy (not https)", []string{"HTTP_PROXY=http://proxy:3128"}, false},
		{"nil env", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := envContainsProxy(c.env); got != c.want {
				t.Errorf("envContainsProxy(%v)=%v want %v", c.env, got, c.want)
			}
		})
	}
}

func TestBuildMaskRulesSplitsSecrets(t *testing.T) {
	auth := AuthTokens{ApiKey: "sk-ant-REAL", OAuthToken: "oauth-REAL", GhToken: "ghp_REAL"}
	rules, sentinels := BuildMaskRules(auth)

	// Sentinels must differ from the real secrets and be format-preserving.
	if sentinels.ApiKey == auth.ApiKey || !strings.HasPrefix(sentinels.ApiKey, "sk-ant-") {
		t.Errorf("api key sentinel bad: %q", sentinels.ApiKey)
	}
	if sentinels.GhToken == auth.GhToken || !strings.HasPrefix(sentinels.GhToken, "ghp_") {
		t.Errorf("gh sentinel bad: %q", sentinels.GhToken)
	}
	if sentinels.OAuthToken == auth.OAuthToken || sentinels.OAuthToken == "" {
		t.Errorf("oauth sentinel bad: %q", sentinels.OAuthToken)
	}

	// Rules must carry the REAL secrets and target the right hosts/headers.
	blob, _ := json.Marshal(rules)
	s := string(blob)
	for _, want := range []string{"sk-ant-REAL", "oauth-REAL", "ghp_REAL", "api.anthropic.com", "github.com", "x-api-key", "authorization"} {
		if !strings.Contains(s, want) {
			t.Errorf("rules missing %q: %s", want, s)
		}
	}
	// The sentinel in each rule must match what the agent gets.
	for _, r := range rules {
		if r.Secret == "sk-ant-REAL" && r.Sentinel != sentinels.ApiKey {
			t.Error("api-key rule sentinel mismatch")
		}
	}
}

func TestBuildMaskRulesEmptyWhenNoSecrets(t *testing.T) {
	rules, _ := BuildMaskRules(AuthTokens{})
	if len(rules) != 0 {
		t.Errorf("no secrets => no rules, got %d", len(rules))
	}
}

func TestShouldMask(t *testing.T) {
	withSecrets := AuthTokens{ApiKey: "sk-ant-REAL"}
	noSecrets := AuthTokens{}

	tests := []struct {
		name             string
		auth             AuthTokens
		hasUpstreamProxy bool
		want             bool
	}{
		{"secrets and no upstream proxy => mask", withSecrets, false, true},
		{"secrets but upstream proxy configured => no mask", withSecrets, true, false},
		{"no secrets => no mask regardless of proxy", noSecrets, false, false},
		{"no secrets and upstream proxy => no mask", noSecrets, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ShouldMask(tt.auth, tt.hasUpstreamProxy); got != tt.want {
				t.Errorf("ShouldMask(%+v, %v) = %v, want %v", tt.auth, tt.hasUpstreamProxy, got, tt.want)
			}
		})
	}
}
