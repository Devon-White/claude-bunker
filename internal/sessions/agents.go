package sessions

import (
	"encoding/json"
	"fmt"
)

// AgentSession is one entry from `claude agents --json` — Claude Code's
// authoritative view of an active session (interactive or background).
type AgentSession struct {
	SessionID string
	Name      string // user-set/AI display name; empty when unnamed
	CWD       string
	Kind      string // "interactive" | "background"
	Status    string // "idle" | "waiting" | ...
	State     string // e.g. "blocked" | "failed" (background); may be empty
	PID       int    // container-namespace PID when the command ran in-container
}

// parseAgents decodes the JSON array printed by `claude agents --json`.
func parseAgents(data []byte) ([]AgentSession, error) {
	var raw []struct {
		SessionID string `json:"sessionId"`
		Name      string `json:"name"`
		CWD       string `json:"cwd"`
		Kind      string `json:"kind"`
		Status    string `json:"status"`
		State     string `json:"state"`
		PID       int    `json:"pid"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parsing claude agents --json: %w", err)
	}
	out := make([]AgentSession, 0, len(raw))
	for _, r := range raw {
		out = append(out, AgentSession{
			SessionID: r.SessionID,
			Name:      r.Name,
			CWD:       r.CWD,
			Kind:      r.Kind,
			Status:    r.Status,
			State:     r.State,
			PID:       r.PID,
		})
	}
	return out, nil
}
