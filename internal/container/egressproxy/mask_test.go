package main

import (
	"encoding/base64"
	"net/http"
	"testing"
)

func TestApplyMaskBearer(t *testing.T) {
	h := http.Header{}
	h.Set("Authorization", "Bearer sk-ant-SENTINEL")
	applyMask(h, &MaskRule{Sentinel: "sk-ant-SENTINEL", Secret: "sk-ant-REAL", Headers: []string{"authorization"}})
	if got := h.Get("Authorization"); got != "Bearer sk-ant-REAL" {
		t.Fatalf("bearer swap: %q", got)
	}
}

func TestApplyMaskAPIKey(t *testing.T) {
	h := http.Header{}
	h.Set("x-api-key", "SENTINEL")
	applyMask(h, &MaskRule{Sentinel: "SENTINEL", Secret: "REAL", Headers: []string{"x-api-key"}})
	if got := h.Get("x-api-key"); got != "REAL" {
		t.Fatalf("api-key swap: %q", got)
	}
}

func TestApplyMaskBasicPassword(t *testing.T) {
	h := http.Header{}
	h.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("x-access-token:SENTINEL")))
	applyMask(h, &MaskRule{Sentinel: "SENTINEL", Secret: "ghp_REAL", Headers: []string{"authorization"}})
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:ghp_REAL"))
	if got := h.Get("Authorization"); got != want {
		t.Fatalf("basic swap: %q want %q", got, want)
	}
}

func TestApplyMaskLeavesNonMatchingUntouched(t *testing.T) {
	h := http.Header{}
	h.Set("Authorization", "Bearer something-else")
	applyMask(h, &MaskRule{Sentinel: "SENTINEL", Secret: "REAL", Headers: []string{"authorization"}})
	if got := h.Get("Authorization"); got != "Bearer something-else" {
		t.Fatalf("must not touch non-matching value: %q", got)
	}
}

// TestMatchRulesReturnsAllRulesForHost guards against the single-rule
// regression (matchRule returning only the FIRST match): a host that has
// multiple credentials configured — e.g. api.anthropic.com with both an
// api-key rule and an OAuth rule — must get every one of its rules back, not
// just the first, or the second credential's sentinel is never swapped.
func TestMatchRulesReturnsAllRulesForHost(t *testing.T) {
	rules := []MaskRule{
		{Sentinel: "APIKEY-SENTINEL", Secret: "APIKEY-REAL", Hosts: []string{"api.anthropic.com"}, Headers: []string{"x-api-key", "authorization"}},
		{Sentinel: "OAUTH-SENTINEL", Secret: "OAUTH-REAL", Hosts: []string{"api.anthropic.com"}, Headers: []string{"authorization"}},
		{Sentinel: "GH-SENTINEL", Secret: "GH-REAL", Hosts: []string{"github.com", "api.github.com"}, Headers: []string{"authorization"}},
	}

	got := matchRules(rules, "API.ANTHROPIC.COM") // case-insensitive match
	if len(got) != 2 {
		t.Fatalf("matchRules(api.anthropic.com) returned %d rules, want 2: %+v", len(got), got)
	}
	if got[0].Sentinel != "APIKEY-SENTINEL" || got[1].Sentinel != "OAUTH-SENTINEL" {
		t.Errorf("matchRules returned wrong rules: %+v", got)
	}

	if got := matchRules(rules, "github.com"); len(got) != 1 || got[0].Sentinel != "GH-SENTINEL" {
		t.Errorf("matchRules(github.com) = %+v, want just the GH rule", got)
	}

	if got := matchRules(rules, "example.com"); len(got) != 0 {
		t.Errorf("matchRules(example.com) = %+v, want none", got)
	}
}
