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
