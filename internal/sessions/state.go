package sessions

import (
	"encoding/json"
	"time"
)

// ContainerState represents the state of a single claude-bunker container.
// Each container maps to one workspace/project directory.
type ContainerState struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`         // label value: "project-a1b2c3d4"
	DisplayName string        `json:"display_name"`  // human portion: "project-a"
	Status      string        `json:"status"`        // "running", "exited", "created", etc.
	StartedAt   time.Time     `json:"started_at"`
	Sessions    []SessionInfo `json:"sessions"`
}

// SessionInfo represents a single exec session (claude or bash process)
// inside a container.
type SessionInfo struct {
	PID       string         `json:"pid"`
	Command   string         `json:"command"`    // "claude" or "bash"
	SessionID string         `json:"session_id"` // Claude Code session UUID (resolved from PID)
	Title     string         `json:"title"`      // custom session title (from title registry)
	Subagents []SubagentInfo `json:"subagents"`  // child processes of this session
}

// SubagentInfo represents a subagent or child process spawned by a claude session.
// Detected via process tree inspection (PPID matching).
type SubagentInfo struct {
	PID  string `json:"pid"`
	Name string `json:"name"` // derived from process command/args
}

// Snapshot is an immutable point-in-time view of all claude-bunker sessions.
// Designed for future swarm integration where multiple Docker hosts
// contribute snapshots to a unified view.
type Snapshot struct {
	Containers []ContainerState `json:"containers"`
	UpdatedAt  time.Time        `json:"updated_at"`
}

// MarshalJSON implements json.Marshaler for Snapshot, ensuring containers
// is always an array (never null) in JSON output.
func (s Snapshot) MarshalJSON() ([]byte, error) {
	type alias Snapshot
	a := alias(s)
	if a.Containers == nil {
		a.Containers = []ContainerState{}
	}
	return json.Marshal(a)
}
