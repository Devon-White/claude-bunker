package sessions

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/client"

	ctr "github.com/Devon-White/claude-bunker/internal/container"
)

// mockClient implements DockerClient for testing.
type mockClient struct {
	containers []container.Summary
	inspect    map[string]container.InspectResponse
	top        map[string]container.TopResponse
	stopErr    error
	removeErr  error
}

func (m *mockClient) ContainerList(_ context.Context, _ container.ListOptions) ([]container.Summary, error) {
	return m.containers, nil
}

func (m *mockClient) ContainerInspect(_ context.Context, id string) (container.InspectResponse, error) {
	if resp, ok := m.inspect[id]; ok {
		return resp, nil
	}
	return container.InspectResponse{}, nil
}

func (m *mockClient) ContainerTop(_ context.Context, id string, _ []string) (container.TopResponse, error) {
	if resp, ok := m.top[id]; ok {
		return resp, nil
	}
	return container.TopResponse{}, nil
}

func (m *mockClient) ContainerStop(_ context.Context, _ string, _ container.StopOptions) error {
	return m.stopErr
}

func (m *mockClient) ContainerRemove(_ context.Context, _ string, _ container.RemoveOptions) error {
	return m.removeErr
}

func (m *mockClient) ContainerStart(_ context.Context, _ string, _ container.StartOptions) error {
	return nil
}

func (m *mockClient) ContainerRename(_ context.Context, _, _ string) error {
	return nil
}

func (m *mockClient) Events(_ context.Context, _ events.ListOptions) (<-chan events.Message, <-chan error) {
	return make(chan events.Message), make(chan error)
}

func TestFetchSnapshot_Empty(t *testing.T) {
	mgr := NewManager(&mockClient{})
	snap, err := mgr.FetchSnapshot(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(snap.Containers) != 0 {
		t.Fatalf("expected 0 containers, got %d", len(snap.Containers))
	}
}

func TestFetchSnapshot_MultipleContainers(t *testing.T) {
	// Claude sessions now come from `claude agents --json`, not the process
	// tree, so stub the exec call that would otherwise hit a nil Docker client.
	orig := execAgentsJSON
	defer func() { execAgentsJSON = orig }()
	execAgentsJSON = func(_ context.Context, _ *client.Client, _ string) (string, error) {
		return `[{"pid":42,"cwd":"/workspace","kind":"interactive","sessionId":"sid-alpha","status":"idle"}]`, nil
	}

	now := time.Now().Format(time.RFC3339Nano)
	cli := &mockClient{
		containers: []container.Summary{
			{
				ID:     "abc123",
				State:  "running",
				Labels: map[string]string{ctr.LabelKey: "project-alpha-a1b2c3d4"},
			},
			{
				ID:     "def456",
				State:  "exited",
				Labels: map[string]string{ctr.LabelKey: "project-beta-e5f6g7h8"},
			},
		},
		inspect: map[string]container.InspectResponse{
			"abc123": {
				ContainerJSONBase: &container.ContainerJSONBase{
					State: &container.State{StartedAt: now},
				},
			},
		},
		top: map[string]container.TopResponse{
			"abc123": {
				Titles:    []string{"PID", "PPID", "COMMAND", "ARGS"},
				Processes: [][]string{{"1", "0", "sleep", "sleep infinity"}, {"42", "1", "claude", "claude"}},
			},
		},
	}

	mgr := NewManager(cli)
	snap, err := mgr.FetchSnapshot(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(snap.Containers) != 2 {
		t.Fatalf("expected 2 containers, got %d", len(snap.Containers))
	}

	// Running container should have sessions populated.
	alpha := snap.Containers[0]
	if alpha.Name != "project-alpha-a1b2c3d4" {
		t.Errorf("expected name 'project-alpha-a1b2c3d4', got %q", alpha.Name)
	}
	if alpha.DisplayName != "project-alpha" {
		t.Errorf("expected display name 'project-alpha', got %q", alpha.DisplayName)
	}
	if alpha.Status != "running" {
		t.Errorf("expected status 'running', got %q", alpha.Status)
	}
	if len(alpha.Sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(alpha.Sessions))
	}
	if alpha.Sessions[0].Command != "claude" {
		t.Errorf("expected session command 'claude', got %q", alpha.Sessions[0].Command)
	}

	// Exited container should have no sessions.
	beta := snap.Containers[1]
	if beta.Status != "exited" {
		t.Errorf("expected status 'exited', got %q", beta.Status)
	}
	if len(beta.Sessions) != 0 {
		t.Errorf("expected 0 sessions for exited container, got %d", len(beta.Sessions))
	}
}

func TestResolveContainer_ExactMatch(t *testing.T) {
	orig := execAgentsJSON
	defer func() { execAgentsJSON = orig }()
	execAgentsJSON = func(_ context.Context, _ *client.Client, _ string) (string, error) { return "[]", nil }

	cli := &mockClient{
		containers: []container.Summary{
			{ID: "abc", State: "running", Labels: map[string]string{ctr.LabelKey: "project-a1b2c3d4"}},
		},
		inspect: map[string]container.InspectResponse{
			"abc": {ContainerJSONBase: &container.ContainerJSONBase{State: &container.State{}}},
		},
		top: map[string]container.TopResponse{
			"abc": {Titles: []string{"PID", "COMMAND"}, Processes: [][]string{}},
		},
	}

	mgr := NewManager(cli)
	c, err := mgr.ResolveContainer(context.Background(), "project-a1b2c3d4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.ID != "abc" {
		t.Errorf("expected ID 'abc', got %q", c.ID)
	}
}

func TestResolveContainer_PrefixMatch(t *testing.T) {
	cli := &mockClient{
		containers: []container.Summary{
			{ID: "abc", State: "exited", Labels: map[string]string{ctr.LabelKey: "myproject-a1b2c3d4"}},
		},
	}

	mgr := NewManager(cli)
	c, err := mgr.ResolveContainer(context.Background(), "myproject")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.ID != "abc" {
		t.Errorf("expected ID 'abc', got %q", c.ID)
	}
}

func TestResolveContainer_NoMatch(t *testing.T) {
	orig := execAgentsJSON
	defer func() { execAgentsJSON = orig }()
	execAgentsJSON = func(_ context.Context, _ *client.Client, _ string) (string, error) { return "[]", nil }

	cli := &mockClient{
		containers: []container.Summary{
			{ID: "abc", State: "running", Labels: map[string]string{ctr.LabelKey: "project-a1b2c3d4"}},
		},
		inspect: map[string]container.InspectResponse{
			"abc": {ContainerJSONBase: &container.ContainerJSONBase{State: &container.State{}}},
		},
		top: map[string]container.TopResponse{
			"abc": {Titles: []string{"PID", "COMMAND"}, Processes: [][]string{}},
		},
	}

	mgr := NewManager(cli)
	_, err := mgr.ResolveContainer(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for no match")
	}
}

func TestResolveContainer_AmbiguousMatch(t *testing.T) {
	cli := &mockClient{
		containers: []container.Summary{
			{ID: "abc", State: "exited", Labels: map[string]string{ctr.LabelKey: "proj-alpha-a1b2c3d4"}},
			{ID: "def", State: "exited", Labels: map[string]string{ctr.LabelKey: "proj-beta-e5f6g7h8"}},
		},
	}

	mgr := NewManager(cli)
	_, err := mgr.ResolveContainer(context.Background(), "proj")
	if err == nil {
		t.Fatal("expected error for ambiguous match")
	}
}

func TestDisplayName(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"project-a1b2c3d4", "project"},
		{"my-project-a1b2c3d4", "my-project"},
		{"nohash", "nohash"},
		{"short-ab", "short-ab"},
		{"has-longer-suffix-toolong99", "has-longer-suffix-toolong99"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DisplayName(tt.name)
			if got != tt.want {
				t.Errorf("DisplayName(%q) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

func TestGetProcessTree_WithSubagents(t *testing.T) {
	cli := &mockClient{
		top: map[string]container.TopResponse{
			"abc": {
				Titles: []string{"PID", "PPID", "COMMAND", "ARGS"},
				Processes: [][]string{
					{"1", "0", "sleep", "sleep infinity"},
					{"10", "1", "claude", "claude"},
					{"20", "10", "node", "node claude subagent"},
					{"30", "1", "bash", "bash"},
				},
			},
		},
	}

	mgr := NewManager(cli)
	sessions, err := mgr.GetProcessTree(context.Background(), "abc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should find claude (with subagent) and bash.
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(sessions))
	}

	// Claude session should have a subagent.
	claude := sessions[0]
	if claude.Command != "claude" {
		t.Errorf("expected first session to be claude, got %q", claude.Command)
	}
	if len(claude.Subagents) != 1 {
		t.Fatalf("expected 1 subagent, got %d", len(claude.Subagents))
	}
	if claude.Subagents[0].PID != "20" {
		t.Errorf("expected subagent PID '20', got %q", claude.Subagents[0].PID)
	}

	// Bash session should have no subagents.
	bash := sessions[1]
	if bash.Command != "bash" {
		t.Errorf("expected second session to be bash, got %q", bash.Command)
	}
	if len(bash.Subagents) != 0 {
		t.Errorf("expected 0 subagents for bash, got %d", len(bash.Subagents))
	}
}

func TestGetProcessTree_BashChildOfClaude(t *testing.T) {
	// Bash processes that are children of claude should not be listed as sessions.
	cli := &mockClient{
		top: map[string]container.TopResponse{
			"abc": {
				Titles: []string{"PID", "PPID", "COMMAND", "ARGS"},
				Processes: [][]string{
					{"1", "0", "sleep", "sleep infinity"},
					{"10", "1", "claude", "claude"},
					{"20", "10", "bash", "bash -c some-command"},
				},
			},
		},
	}

	mgr := NewManager(cli)
	sessions, err := mgr.GetProcessTree(context.Background(), "abc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Only claude should appear; bash child of claude is filtered out.
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if sessions[0].Command != "claude" {
		t.Errorf("expected claude, got %q", sessions[0].Command)
	}
}

func TestClaudeSessionsFromAgents(t *testing.T) {
	orig := execAgentsJSON
	defer func() { execAgentsJSON = orig }()
	execAgentsJSON = func(_ context.Context, _ *client.Client, _ string) (string, error) {
		return `[
		  {"pid":10,"cwd":"/workspace","kind":"interactive","sessionId":"sid-1","name":"fix-bug","status":"idle"},
		  {"pid":20,"cwd":"/workspace","kind":"background","sessionId":"sid-2","name":"run tests","status":"idle","state":"blocked"}
		]`, nil
	}
	mgr := NewManager(&mockClient{})
	got := mgr.claudeSessions(context.Background(), "cid")
	if len(got) != 1 {
		t.Fatalf("want 1 interactive claude session, got %d: %+v", len(got), got)
	}
	s := got[0]
	if s.Command != "claude" || s.SessionID != "sid-1" || s.Title != "fix-bug" || s.PID != "10" {
		t.Errorf("session mismatch: %+v", s)
	}
	if len(s.Subagents) != 1 || s.Subagents[0].Name != "run tests" || s.Subagents[0].PID != "20" {
		t.Errorf("subagent mismatch: %+v", s.Subagents)
	}
}

func TestClaudeSessions_TitleFallback(t *testing.T) {
	orig := execAgentsJSON
	defer func() { execAgentsJSON = orig }()
	execAgentsJSON = func(_ context.Context, _ *client.Client, _ string) (string, error) {
		return `[
		  {"pid":10,"cwd":"/workspace","kind":"interactive","sessionId":"sid-empty","name":"","status":"idle"},
		  {"pid":20,"cwd":"/workspace","kind":"interactive","sessionId":"sid-named","name":"live-name","status":"idle"}
		]`, nil
	}

	// Seed titles in the store
	const containerID = "cid"
	if err := SetSessionTitle(containerID, "sid-empty", "stored-title"); err != nil {
		t.Fatalf("SetSessionTitle failed: %v", err)
	}
	if err := SetSessionTitle(containerID, "sid-named", "stored-other"); err != nil {
		t.Fatalf("SetSessionTitle failed: %v", err)
	}

	mgr := NewManager(&mockClient{})
	got := mgr.claudeSessions(context.Background(), containerID)

	if len(got) != 2 {
		t.Fatalf("want 2 interactive claude sessions, got %d: %+v", len(got), got)
	}

	// Find the sessions by SessionID
	var emptySession, namedSession *SessionInfo
	for i := range got {
		switch got[i].SessionID {
		case "sid-empty":
			emptySession = &got[i]
		case "sid-named":
			namedSession = &got[i]
		}
	}

	if emptySession == nil {
		t.Fatal("session with sid-empty not found")
	}
	if namedSession == nil {
		t.Fatal("session with sid-named not found")
	}

	// Test 1: Fallback case - empty name should use stored title
	if emptySession.Title != "stored-title" {
		t.Errorf("empty name session: want Title='stored-title', got %q", emptySession.Title)
	}

	// Test 2: Claude name wins - non-empty name should override stored title
	if namedSession.Title != "live-name" {
		t.Errorf("named session: want Title='live-name', got %q", namedSession.Title)
	}
}

func TestFormatUptime(t *testing.T) {
	tests := []struct {
		name    string
		started time.Time
		want    string
	}{
		{"zero", time.Time{}, ""},
		{"seconds", time.Now().Add(-30 * time.Second), "30s"},
		{"minutes", time.Now().Add(-5*time.Minute - 10*time.Second), "5m 10s"},
		{"hours", time.Now().Add(-2*time.Hour - 15*time.Minute), "2h 15m"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatUptime(tt.started)
			if got != tt.want {
				t.Errorf("FormatUptime() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSnapshotJSON_EmptyContainers(t *testing.T) {
	snap := Snapshot{UpdatedAt: time.Now()}
	data, err := snap.MarshalJSON()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should contain "containers":[] not "containers":null
	if !strings.Contains(string(data), `"containers":[]`) {
		t.Errorf("expected empty array in JSON, got: %s", string(data))
	}
}
