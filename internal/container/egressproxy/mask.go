package main

import (
	"encoding/base64"
	"net/http"
	"strings"
)

// matchRule returns the first rule whose Hosts include host, or nil.
func matchRule(rules []MaskRule, host string) *MaskRule {
	for i := range rules {
		for _, h := range rules[i].Hosts {
			if strings.EqualFold(h, host) {
				return &rules[i]
			}
		}
	}
	return nil
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
