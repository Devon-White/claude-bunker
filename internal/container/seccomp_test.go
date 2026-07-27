package container

import (
	"encoding/json"
	"testing"
)

func TestSeccompProfileJSON(t *testing.T) {
	s := SeccompProfileJSON()
	var p struct {
		DefaultAction string `json:"defaultAction"`
		Syscalls      []struct {
			Names  []string `json:"names"`
			Action string   `json:"action"`
		} `json:"syscalls"`
	}
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		t.Fatalf("profile is not valid JSON: %v", err)
	}
	if p.DefaultAction != "SCMP_ACT_ALLOW" {
		t.Errorf("defaultAction = %q, want SCMP_ACT_ALLOW", p.DefaultAction)
	}
	// The denylist must block the dangerous syscalls.
	blocked := map[string]bool{}
	for _, sc := range p.Syscalls {
		if sc.Action == "SCMP_ACT_ERRNO" {
			for _, n := range sc.Names {
				blocked[n] = true
			}
		}
	}
	for _, must := range []string{"bpf", "kexec_load", "ptrace", "init_module", "reboot"} {
		if !blocked[must] {
			t.Errorf("%q must be in the seccomp denylist", must)
		}
	}
	// Deterministic: two calls return identical bytes (needed for drift stability).
	if SeccompProfileJSON() != s {
		t.Error("SeccompProfileJSON must be deterministic")
	}
}
