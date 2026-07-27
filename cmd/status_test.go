package cmd

import (
	"encoding/json"
	"testing"
)

func TestStatusInfoJSON(t *testing.T) {
	s := statusInfo{
		Workspace: "/w", Container: "proj-abc", Image: "img:tag",
		State: "running", ID: "abcdef123456", Uptime: "5m 0s",
		Sessions: []string{"claude"},
	}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"workspace", "container", "image", "state"} {
		if _, ok := back[k]; !ok {
			t.Errorf("status JSON missing key %q: %s", k, data)
		}
	}
	if back["state"] != "running" {
		t.Errorf("state = %v", back["state"])
	}
}

func TestStatusShowsConfig(t *testing.T) {
	if statusShowsConfig("not created") {
		t.Error("config section must NOT show for the not-created state")
	}
	if !statusShowsConfig("running") {
		t.Error("config section must show for a running container")
	}
}
