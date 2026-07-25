package sessions

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	ctr "github.com/Devon-White/claude-bunker/internal/container"
	"github.com/docker/docker/client"
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

// execAgentsJSON runs `claude agents --json --cwd /workspace` in the container
// and returns stdout. Overridable in tests.
//
// This exec is driven both by the TUI's unconditional 3-second poll and by
// every one-shot CLI command (which pass context.Background(), i.e. no
// caller-supplied deadline). `claude agents --json` is a full Node process;
// if it ever hangs (container under load, claude wedged), an unbounded exec
// would block FetchSnapshot forever, freezing the TUI poll goroutine or
// hanging a one-shot command indefinitely. The 5-second timeout bounds that
// blast radius. exec.go's execCore closes the underlying hijacked connection
// when ctx is done, so this timeout actually unblocks the read — it isn't
// merely cosmetic.
var execAgentsJSON = func(ctx context.Context, cli *client.Client, containerID string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return ctr.ExecNonInteractive(ctx, cli, containerID, ctr.ContainerUser,
		[]string{"claude", "agents", "--json", "--cwd", ctr.ContainerWorkspace})
}

// FetchAgents enumerates the container's Claude sessions via
// `claude agents --json`, scoped to the /workspace cwd. Returns an empty slice
// (not an error) when there are no sessions.
func FetchAgents(ctx context.Context, cli *client.Client, containerID string) ([]AgentSession, error) {
	out, err := execAgentsJSON(ctx, cli, containerID)
	if err != nil {
		return nil, fmt.Errorf("running claude agents --json: %w", err)
	}
	all, err := parseAgents([]byte(out))
	if err != nil {
		return nil, err
	}
	scoped := make([]AgentSession, 0, len(all))
	for _, s := range all {
		if s.CWD == ctr.ContainerWorkspace {
			scoped = append(scoped, s)
		}
	}
	return scoped, nil
}
