package sessions

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"

	"github.com/Devon-White/claude-bunker/internal/config"
	ctr "github.com/Devon-White/claude-bunker/internal/container"
)

// proc is used internally for process tree parsing.
type proc struct {
	pid  string
	ppid string
	comm string
	args string
}

// DockerClient is the subset of the Docker API used by the sessions manager.
// Extracted as an interface for testing with mocks.
type DockerClient interface {
	ContainerList(ctx context.Context, options container.ListOptions) ([]container.Summary, error)
	ContainerInspect(ctx context.Context, containerID string) (container.InspectResponse, error)
	ContainerTop(ctx context.Context, containerID string, arguments []string) (container.TopResponse, error)
	ContainerStop(ctx context.Context, containerID string, options container.StopOptions) error
	ContainerRemove(ctx context.Context, containerID string, options container.RemoveOptions) error
	ContainerStart(ctx context.Context, containerID string, options container.StartOptions) error
	ContainerRename(ctx context.Context, containerID string, newContainerName string) error
	Events(ctx context.Context, options events.ListOptions) (<-chan events.Message, <-chan error)
}

// Manager fetches and tracks session state from the Docker daemon.
// Designed as a standalone component so a future swarm orchestrator can
// aggregate Managers from multiple Docker hosts into a unified view.
type Manager struct {
	cli DockerClient
}

// NewManager creates a new session manager.
func NewManager(cli DockerClient) *Manager {
	return &Manager{
		cli: cli,
	}
}

// Client returns the underlying Docker client for operations that need
// direct access (e.g., ExecInteractive for attach).
func (m *Manager) Client() DockerClient {
	return m.cli
}

// FetchSnapshot queries Docker for all claude-bunker containers and their
// exec sessions. Returns an immutable Snapshot. This is the primary API
// for both CLI (one-shot) and TUI (periodic polling).
func (m *Manager) FetchSnapshot(ctx context.Context) (Snapshot, error) {
	containers, err := m.listAllContainers(ctx)
	if err != nil {
		return Snapshot{}, fmt.Errorf("listing containers: %w", err)
	}

	states := make([]ContainerState, 0, len(containers))
	activeIDs := make(map[string]bool, len(containers))
	for _, c := range containers {
		activeIDs[c.ID] = true
		cs := ContainerState{
			ID:          c.ID,
			Name:        c.Labels[ctr.LabelKey],
			DisplayName: config.DisplayName(c.Labels[ctr.LabelKey]),
			Status:      c.State,
		}

		// Use custom display name if set.
		if custom := GetCustomName(c.ID); custom != "" {
			cs.DisplayName = custom
		}

		// Only fetch process tree and detailed state for running containers.
		if c.State == "running" {
			inspect, err := m.cli.ContainerInspect(ctx, c.ID)
			if err == nil && inspect.State != nil && inspect.State.StartedAt != "" {
				cs.StartedAt, _ = time.Parse(time.RFC3339Nano, inspect.State.StartedAt)
			}
			// Claude sessions come from `claude agents --json` (authoritative).
			cs.Sessions = m.claudeSessions(ctx, c.ID)
			// bash/shell sessions still come from the process tree.
			cs.Sessions = append(cs.Sessions, m.bashSessions(ctx, c.ID)...)
		}

		states = append(states, cs)
	}

	// Clean up names for containers that no longer exist.
	PruneStaleNames(activeIDs)

	return Snapshot{
		Containers: states,
		UpdatedAt:  time.Now(),
	}, nil
}

// GetProcessTree uses ContainerTop to get process info for a running container.
// Parses output to detect claude/bash sessions and their child processes (subagents).
func (m *Manager) GetProcessTree(ctx context.Context, containerID string) ([]SessionInfo, error) {
	top, err := m.cli.ContainerTop(ctx, containerID, []string{"-eo", "pid,ppid,comm,args"})
	if err != nil {
		return nil, fmt.Errorf("container top: %w", err)
	}

	// Find column indices from headers.
	// Docker's ps outputs both "comm" and "args" as "COMMAND", so we assign
	// the first occurrence to commIdx and the second to argsIdx.
	pidIdx, ppidIdx, commIdx, argsIdx := -1, -1, -1, -1
	for i, title := range top.Titles {
		switch strings.ToUpper(strings.TrimSpace(title)) {
		case "PID":
			pidIdx = i
		case "PPID":
			ppidIdx = i
		case "COMMAND", "COMM":
			if commIdx < 0 {
				commIdx = i
			} else if argsIdx < 0 {
				argsIdx = i
			}
		case "ARGS", "CMD":
			argsIdx = i
		}
	}
	if pidIdx < 0 || commIdx < 0 {
		return nil, fmt.Errorf("unexpected ContainerTop output format")
	}

	// Parse all processes.
	procs := make([]proc, 0, len(top.Processes))
	for _, row := range top.Processes {
		p := proc{}
		if pidIdx < len(row) {
			p.pid = strings.TrimSpace(row[pidIdx])
		}
		if ppidIdx >= 0 && ppidIdx < len(row) {
			p.ppid = strings.TrimSpace(row[ppidIdx])
		}
		if commIdx < len(row) {
			p.comm = strings.TrimSpace(row[commIdx])
		}
		if argsIdx >= 0 && argsIdx < len(row) {
			p.args = strings.TrimSpace(row[argsIdx])
		}
		procs = append(procs, p)
	}

	// Build a PID → children map for subagent detection.
	children := map[string][]proc{}
	for _, p := range procs {
		if p.ppid != "" {
			children[p.ppid] = append(children[p.ppid], p)
		}
	}

	// Find top-level sessions (claude or bash processes).
	// Claude may appear as "claude" (native binary) or as "node" running
	// a claude script (older npm-based installs).
	var sessions []SessionInfo
	for _, p := range procs {
		isClaude := isClaudeProcess(p)
		isBash := p.comm == "bash"

		if !isClaude && !isBash {
			continue
		}
		// Skip processes that are children of a claude process (subshells/subagents).
		if (isBash || (isClaude && p.comm != "claude")) && isChildOfClaude(p, procs) {
			continue
		}

		command := p.comm
		if isClaude && p.comm != "claude" {
			command = "claude" // normalize for display
		}

		s := SessionInfo{
			PID:     p.pid,
			Command: command,
		}

		// For claude sessions, look for subagent child processes.
		if isClaude {
			s.Subagents = findSubagents(p.pid, children)
		}

		sessions = append(sessions, s)
	}

	return sessions, nil
}

// claudeSessions builds SessionInfo entries for the container's Claude sessions
// from `claude agents --json`. Interactive sessions become top-level sessions;
// background sessions (subagents) are nested under the first interactive session,
// or promoted to their own entry if there is no interactive parent.
func (m *Manager) claudeSessions(ctx context.Context, containerID string) []SessionInfo {
	realCli, _ := m.cli.(*client.Client)
	agents, err := FetchAgents(ctx, realCli, containerID)
	if err != nil || len(agents) == 0 {
		return nil
	}
	var sessions []SessionInfo
	var subagents []SubagentInfo
	for _, a := range agents {
		if a.Kind == "background" {
			subagents = append(subagents, SubagentInfo{PID: strconv.Itoa(a.PID), Name: a.Name})
			continue
		}
		sessions = append(sessions, SessionInfo{
			PID:       strconv.Itoa(a.PID),
			Command:   "claude",
			SessionID: a.SessionID,
			Title:     a.Name, // Claude's authoritative name; store fallback applied in Task 5
		})
	}
	if len(sessions) > 0 {
		sessions[0].Subagents = append(sessions[0].Subagents, subagents...)
	} else if len(subagents) > 0 {
		// Background-only: surface them as sessions so the TUI still shows activity.
		for _, sa := range subagents {
			sessions = append(sessions, SessionInfo{PID: sa.PID, Command: "claude", Title: sa.Name})
		}
	}
	return sessions
}

// bashSessions returns top-level bash (shell) sessions from the process tree.
// Claude sessions are intentionally excluded — those come from claude agents --json.
func (m *Manager) bashSessions(ctx context.Context, containerID string) []SessionInfo {
	all, err := m.GetProcessTree(ctx, containerID)
	if err != nil {
		return nil
	}
	var out []SessionInfo
	for _, s := range all {
		if s.Command == "bash" {
			out = append(out, s)
		}
	}
	return out
}

// ResolveContainer finds a container by exact or prefix match against all
// claude-bunker containers. Returns an error if zero or multiple containers match.
func (m *Manager) ResolveContainer(ctx context.Context, nameOrPrefix string) (ContainerState, error) {
	snap, err := m.FetchSnapshot(ctx)
	if err != nil {
		return ContainerState{}, err
	}

	var matches []ContainerState
	for _, c := range snap.Containers {
		// Check custom name too.
		customName := GetCustomName(c.ID)
		if c.Name == nameOrPrefix || c.DisplayName == nameOrPrefix || customName == nameOrPrefix {
			// Exact match — return immediately.
			return c, nil
		}
		if strings.HasPrefix(c.Name, nameOrPrefix) || strings.HasPrefix(c.DisplayName, nameOrPrefix) ||
			(customName != "" && strings.HasPrefix(customName, nameOrPrefix)) {
			matches = append(matches, c)
		}
	}

	switch len(matches) {
	case 0:
		return ContainerState{}, fmt.Errorf("no container matching %q", nameOrPrefix)
	case 1:
		return matches[0], nil
	default:
		names := make([]string, len(matches))
		for i, c := range matches {
			names[i] = c.Name
		}
		return ContainerState{}, fmt.Errorf("ambiguous match %q: %s", nameOrPrefix, strings.Join(names, ", "))
	}
}

// listAllContainers returns all containers with the claude-bunker label,
// regardless of which project they belong to.
func (m *Manager) listAllContainers(ctx context.Context) ([]container.Summary, error) {
	f := filters.NewArgs()
	f.Add("label", ctr.LabelKey)
	return m.cli.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: f,
	})
}

// DisplayName delegates to config.DisplayName for backward compatibility.
// It extracts the human-readable portion of a container name by stripping
// the trailing hash suffix (e.g., "project-alpha-a1b2c3d4" → "project-alpha").
func DisplayName(name string) string {
	return config.DisplayName(name)
}

// isClaudeProcess checks whether a process is a claude session.
// Handles both the native binary (comm == "claude") and Node.js-based installs
// where comm is "node" but args contain the claude entry point.
func isClaudeProcess(p proc) bool {
	if p.comm == "claude" {
		return true
	}
	// Node.js-based claude: comm is "node" but args reference claude.
	if p.comm == "node" && strings.Contains(p.args, "claude") {
		// Exclude node subagent processes (already handled by findSubagents).
		// A top-level claude-via-node has "claude" early in the args, typically
		// as the script path (e.g., "/home/.../.local/bin/claude").
		return true
	}
	return false
}

// isChildOfClaude checks whether a bash process is a descendant of a claude process.
func isChildOfClaude(bash proc, all []proc) bool {
	visited := map[string]bool{}
	ppid := bash.ppid
	for ppid != "" && ppid != "0" && !visited[ppid] {
		visited[ppid] = true
		for _, p := range all {
			if p.pid == ppid {
				if isClaudeProcess(p) {
					return true
				}
				ppid = p.ppid
				break
			}
		}
	}
	return false
}

// findSubagents walks the process tree below a claude session PID to find
// interesting child processes (node/claude subagents, not internal helpers).
func findSubagents(parentPID string, children map[string][]proc) []SubagentInfo {
	var agents []SubagentInfo
	// BFS through children to find subagent processes.
	queue := []string{parentPID}
	visited := map[string]bool{parentPID: true}

	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]

		for _, child := range children[pid] {
			if visited[child.pid] {
				continue
			}
			visited[child.pid] = true

			// Look for claude subagent processes (node processes running claude).
			name := classifySubagent(child)
			if name != "" {
				agents = append(agents, SubagentInfo{
					PID:  child.pid,
					Name: name,
				})
			}
			// Continue walking for nested subagents.
			queue = append(queue, child.pid)
		}
	}
	return agents
}

// classifySubagent attempts to identify a process as a known subagent type
// from its command name and arguments.
func classifySubagent(p proc) string {
	// Claude subagents typically show as "node" or "claude" processes.
	switch p.comm {
	case "claude":
		// Nested claude process — likely a subagent.
		return parseAgentName(p.args)
	case "node":
		// Node process under claude — could be a subagent.
		if strings.Contains(p.args, "claude") {
			return parseAgentName(p.args)
		}
	}
	return ""
}

// parseAgentName tries to extract an agent type name from process arguments.
func parseAgentName(args string) string {
	// Look for common subagent patterns in args.
	for _, marker := range []string{"--name", "--agent-type", "subagent_type"} {
		idx := strings.Index(args, marker)
		if idx >= 0 {
			rest := strings.TrimSpace(args[idx+len(marker):])
			rest = strings.TrimLeft(rest, "= ")
			parts := strings.Fields(rest)
			if len(parts) > 0 {
				return parts[0]
			}
		}
	}
	return "subagent"
}

// StopContainer stops a container by ID.
func (m *Manager) StopContainer(ctx context.Context, containerID string) error {
	timeout := 10
	return m.cli.ContainerStop(ctx, containerID, container.StopOptions{Timeout: &timeout})
}

// StartContainer starts a stopped container by ID.
func (m *Manager) StartContainer(ctx context.Context, containerID string) error {
	return m.cli.ContainerStart(ctx, containerID, container.StartOptions{})
}

// RemoveContainer removes a container by ID.
func (m *Manager) RemoveContainer(ctx context.Context, containerID string) error {
	return m.cli.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true})
}

// RenameContainer sets a custom display name for a container and renames
// it in Docker so the name is visible in `docker ps`.
func (m *Manager) RenameContainer(ctx context.Context, containerID, newName string) error {
	if err := SetCustomName(containerID, newName); err != nil {
		return fmt.Errorf("saving display name: %w", err)
	}
	// Also rename in Docker so `docker ps` shows the custom name.
	// Ignore errors — Docker rename can fail if the name conflicts,
	// but the custom display name in our metadata still works.
	_ = m.cli.ContainerRename(ctx, containerID, newName)
	return nil
}

// FormatUptime returns a human-readable duration string.
func FormatUptime(started time.Time) string {
	if started.IsZero() {
		return ""
	}
	d := time.Since(started).Truncate(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		m := int(d.Minutes())
		s := int(d.Seconds()) % 60
		return fmt.Sprintf("%dm %ds", m, s)
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	return fmt.Sprintf("%dh %dm", h, m)
}
