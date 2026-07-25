package sessions

import (
	"context"
	"testing"

	"github.com/docker/docker/client"
)

const agentsFixture = `[
  {"pid":123,"cwd":"/workspace","kind":"interactive","startedAt":1782936561306,"sessionId":"895a6ba5-1e40-4d94-96f7-9a3c0b1a080a","name":"fix-auth-bug","status":"idle"},
  {"pid":456,"cwd":"/workspace","kind":"interactive","startedAt":1782936799205,"sessionId":"06a6ebf8-5944-49fe-86f3-75930e0925a3","status":"waiting","waitingFor":"permission prompt"},
  {"pid":789,"id":"e5cac754","cwd":"/other","kind":"background","startedAt":1783605036338,"sessionId":"7d5a5dd7-0e07-4051-b6f7-818a1bf10e89","name":"run the test suite","status":"idle","state":"blocked"}
]`

func TestParseAgents(t *testing.T) {
	got, err := parseAgents([]byte(agentsFixture))
	if err != nil {
		t.Fatalf("parseAgents: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 sessions, got %d", len(got))
	}
	if got[0].SessionID != "895a6ba5-1e40-4d94-96f7-9a3c0b1a080a" || got[0].Name != "fix-auth-bug" || got[0].PID != 123 || got[0].Kind != "interactive" || got[0].Status != "idle" {
		t.Errorf("session 0 mismatch: %+v", got[0])
	}
	// Unnamed session: Name must be empty (field omitted in JSON).
	if got[1].Name != "" || got[1].Status != "waiting" {
		t.Errorf("session 1 (unnamed/waiting) mismatch: %+v", got[1])
	}
	// Background session with state.
	if got[2].Kind != "background" || got[2].State != "blocked" || got[2].Name != "run the test suite" {
		t.Errorf("session 2 mismatch: %+v", got[2])
	}
}

func TestParseAgents_Empty(t *testing.T) {
	got, err := parseAgents([]byte("[]\n"))
	if err != nil {
		t.Fatalf("parseAgents([]): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want 0 sessions, got %d", len(got))
	}
}

func TestParseAgents_Malformed(t *testing.T) {
	if _, err := parseAgents([]byte("not json")); err == nil {
		t.Error("expected error on malformed JSON")
	}
}

func TestFetchAgents_FiltersToWorkspace(t *testing.T) {
	orig := execAgentsJSON
	defer func() { execAgentsJSON = orig }()
	// Stub returns the fixture (one /workspace interactive, one /workspace waiting, one /other background).
	execAgentsJSON = func(_ context.Context, _ *client.Client, _ string) (string, error) {
		return agentsFixture, nil
	}

	got, err := FetchAgents(context.Background(), nil, "cid")
	if err != nil {
		t.Fatalf("FetchAgents: %v", err)
	}
	// Only the two /workspace sessions should remain (the /other one is filtered out).
	if len(got) != 2 {
		t.Fatalf("want 2 workspace sessions, got %d: %+v", len(got), got)
	}
	for _, s := range got {
		if s.CWD != "/workspace" {
			t.Errorf("non-workspace session leaked: %+v", s)
		}
	}
}
