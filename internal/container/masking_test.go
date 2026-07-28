package container

import (
	"encoding/json"
	"strings"
	"testing"
)

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
