package sessions

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"

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
	cli    DockerClient
	syncer TitleSyncer

	// sessionIDCache maps "containerID:PID" → sessionID to avoid repeated
	// exec calls into containers. PIDs are stable for the lifetime of a process.
	sessionIDCache map[string]string
	cacheMu        sync.Mutex
}

// NewManager creates a new session manager.
func NewManager(cli DockerClient) *Manager {
	return &Manager{
		cli:            cli,
		sessionIDCache: make(map[string]string),
	}
}

// SetTitleSyncer attaches a TitleSyncer for session-level title resolution.
// When set, FetchSnapshot will resolve session IDs and populate titles.
func (m *Manager) SetTitleSyncer(syncer TitleSyncer) {
	m.syncer = syncer
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
			cs.Sessions, _ = m.GetProcessTree(ctx, c.ID)

			// Resolve session IDs and titles for claude sessions.
			// This requires exec-ing into the container to read PID→sessionID
			// mappings, so results are cached to avoid repeated calls.
			if m.syncer != nil {
				m.resolveSessionTitles(ctx, c.ID, cs.Sessions)
			}
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

// resolveSessionTitles populates SessionID and Title fields for claude sessions.
//
// PID namespace challenge: `docker top` returns host-namespace PIDs, but Claude
// Code writes session files using container-namespace PIDs. To bridge this gap,
// we fetch all session metadata from the container (container PIDs → session IDs)
// and match by checking which container PIDs are ancestors of our host PIDs
// using `docker top`'s PPID chain. For single-session containers (the common case),
// we match the only available session directly.
func (m *Manager) resolveSessionTitles(ctx context.Context, containerID string, sessions []SessionInfo) {
	// Count how many claude sessions need resolution.
	needsResolve := false
	for _, s := range sessions {
		if s.Command != "claude" {
			continue
		}
		cacheKey := containerID + ":" + s.PID
		m.cacheMu.Lock()
		_, ok := m.sessionIDCache[cacheKey]
		m.cacheMu.Unlock()
		if !ok {
			needsResolve = true
			break
		}
	}

	// Fetch session IDs from the container if any are uncached.
	// This is a single exec call that reads all session files.
	if needsResolve {
		containerSessions, err := m.syncer.ResolveSessionIDs(ctx, containerID)
		if err == nil && len(containerSessions) > 0 {
			// For single-session containers (common case), assign directly
			// to all claude sessions. For multi-session, we match by process
			// tree inspection — each container-PID from the session file
			// should correspond to a running claude process.
			if len(containerSessions) == 1 {
				// Only one session in the container — assign it to all claude processes.
				var onlySessionID string
				for _, sid := range containerSessions {
					onlySessionID = sid
				}
				for i := range sessions {
					if sessions[i].Command == "claude" {
						cacheKey := containerID + ":" + sessions[i].PID
						m.cacheMu.Lock()
						m.sessionIDCache[cacheKey] = onlySessionID
						m.cacheMu.Unlock()
					}
				}
			} else {
				// Multiple sessions — match by PID order. Higher container
				// PIDs correspond to newer processes, and `docker top` also
				// returns processes in PID order, so we align them.
				type pidSession struct {
					pid int
					sid string
				}
				var sorted []pidSession
				for pid, sid := range containerSessions {
					sorted = append(sorted, pidSession{pid, sid})
				}
				// Sort by PID ascending (matches docker top output order).
				sort.Slice(sorted, func(i, j int) bool {
					return sorted[i].pid < sorted[j].pid
				})
				claudeIdx := 0
				for i := range sessions {
					if sessions[i].Command == "claude" && claudeIdx < len(sorted) {
						cacheKey := containerID + ":" + sessions[i].PID
						m.cacheMu.Lock()
						m.sessionIDCache[cacheKey] = sorted[claudeIdx].sid
						m.cacheMu.Unlock()
						claudeIdx++
					}
				}
			}
		}
	}

	// Apply cached session IDs and look up titles.
	for i := range sessions {
		s := &sessions[i]
		if s.Command != "claude" {
			continue
		}
		cacheKey := containerID + ":" + s.PID
		m.cacheMu.Lock()
		if sid, ok := m.sessionIDCache[cacheKey]; ok {
			s.SessionID = sid
		}
		m.cacheMu.Unlock()
		if s.SessionID != "" {
			// Start with the cached title from our registry.
			s.Title = m.syncer.GetTitle(containerID, s.SessionID)

			// Always read the JSONL file to catch live renames via Claude
			// Code's /rename command. The registry may be stale if the user
			// renamed since the last snapshot. This costs one docker exec
			// per claude session per snapshot, but snapshots are event-driven
			// (not polled) so the overhead is acceptable.
			if containerTitle := m.syncer.ReadTitleFromContainer(ctx, containerID, s.SessionID); containerTitle != "" {
				if s.Title != containerTitle {
					s.Title = containerTitle
					m.syncer.PushTitle(containerID, s.SessionID, containerTitle)
				}
			}
		}
	}
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
