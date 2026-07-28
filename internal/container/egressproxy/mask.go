package main

import (
	"encoding/base64"
	"net/http"
	"strings"
)

// matchRules returns ALL rules whose Hosts include host (case-insensitive).
// A single host can carry multiple credentials (e.g. api.anthropic.com gets
// separate rules for the API-key sentinel and the OAuth-token sentinel), and
// a given request only carries one of them — so every rule for the host must
// be tried, not just the first match, or the non-first credential's sentinel
// is never swapped back to the real secret.
func matchRules(rules []MaskRule, host string) []MaskRule {
	var matched []MaskRule
	for i := range rules {
		for _, h := range rules[i].Hosts {
			if strings.EqualFold(h, host) {
				matched = append(matched, rules[i])
				break
			}
		}
	}
	return matched
}

// applyMask replaces the rule's sentinel with the real secret in the configured
// headers. Handles bare values (x-api-key), "Bearer <sentinel>", and HTTP Basic
// (decode, swap the password, re-encode). Non-matching values are left intact.
func applyMask(h http.Header, rule *MaskRule) {
	for _, name := range rule.Headers {
		v := h.Get(name)
		if v == "" {
			continue
		}
		switch {
		case strings.HasPrefix(v, "Basic "):
			h.Set(name, swapBasic(v, rule.Sentinel, rule.Secret))
		default:
			// covers "Bearer <sentinel>", "<sentinel>", "Token <sentinel>", etc.
			h.Set(name, strings.ReplaceAll(v, rule.Sentinel, rule.Secret))
		}
	}
}

func swapBasic(v, sentinel, secret string) string {
	enc := strings.TrimPrefix(v, "Basic ")
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return v
	}
	user, pass, ok := strings.Cut(string(raw), ":")
	if !ok || pass != sentinel {
		return v
	}
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+secret))
}
