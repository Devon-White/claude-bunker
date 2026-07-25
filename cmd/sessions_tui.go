package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	dockercontainer "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/spf13/cobra"

	ctr "github.com/Devon-White/claude-bunker/internal/container"
	"github.com/Devon-White/claude-bunker/internal/sessions"
)

// runSessionsTUI launches the interactive TUI or falls back to list output.
func runSessionsTUI(cmd *cobra.Command, args []string) error {
	initVerbosity(cmd)

	// Non-TTY fallback: print list output for pipes and automation.
	if !isTTY() {
		return runSessionsList(cmd, args)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cli, err := ctr.NewClient()
	if err != nil {
		return fmt.Errorf("docker client: %w", err)
	}
	defer cli.Close()

	mgr := sessions.NewManager(cli)

	watcher := sessions.NewWatcher(mgr)
	updateCh := watcher.Subscribe(ctx)

	m := newSessionsModel(ctx, cancel, mgr, cli, updateCh)

	p := tea.NewProgram(m, tea.WithAltScreen())
	finalModel, err := p.Run()
	if err != nil {
		return fmt.Errorf("TUI: %w", err)
	}

	// If the user requested an attach, execute it after the TUI exits.
	if fm, ok := finalModel.(sessionsModel); ok && fm.pendingAttach != nil {
		return fm.pendingAttach()
	}

	return nil
}

// sessionsModel is the bubbletea model for the sessions TUI.
type sessionsModel struct {
	// State
	snapshot sessions.Snapshot
	err      error
	items    []viewItem // flattened tree for cursor navigation
	status   string     // transient status message (e.g., "Stopped project-alpha")

	// Navigation
	cursor   int
	expanded map[string]bool // containerID → expanded

	// Confirmation state for destructive actions.
	confirm *confirmState

	// Rename state: when non-nil, user is typing a new name.
	rename *renameState

	// Dependencies
	ctx      context.Context
	cancel   context.CancelFunc
	mgr      *sessions.Manager
	cli      *client.Client
	updateCh <-chan sessions.UpdateMsg

	// UI dimensions
	width  int
	height int

	// Pending action after TUI exits (e.g., attach).
	pendingAttach func() error
}

// Confirm action types for TUI destructive operations.
const (
	actionStop    = "stop"
	actionDelete  = "delete"
	actionRestart = "restart"
)

// confirmState tracks a pending destructive action awaiting user confirmation.
type confirmState struct {
	action      string // actionStop, actionDelete, or actionRestart
	displayName string
	containerID string
}

// renameState tracks an in-progress rename operation.
// Supports both container-level and session-level renames.
type renameState struct {
	containerID string
	sessionID   string // non-empty for session-level rename
	sessionPID  string // PID of the session being renamed
	displayName string // original name (shown as placeholder)
	textInput   textinput.Model
}

// viewItem is a flattened entry in the hierarchical session tree.
type viewItem struct {
	kind        string // "container", "session", "subagent"
	containerID string
	container   *sessions.ContainerState
	session     *sessions.SessionInfo
	subagent    *sessions.SubagentInfo
	depth       int
}

func newSessionsModel(ctx context.Context, cancel context.CancelFunc, mgr *sessions.Manager, cli sessions.DockerClient, updateCh <-chan sessions.UpdateMsg) sessionsModel {
	dockerCli, _ := cli.(*client.Client)
	return sessionsModel{
		ctx:      ctx,
		cancel:   cancel,
		mgr:      mgr,
		cli:      dockerCli,
		updateCh: updateCh,
		expanded: make(map[string]bool),
	}
}

// -- Bubbletea messages --

type snapshotMsg sessions.UpdateMsg
type actionErrorMsg struct{ err error }
type statusMsg string

// -- Init (bubbletea v1 interface) --

func (m sessionsModel) Init() tea.Cmd {
	return tea.Batch(
		waitForUpdate(m.updateCh),
		tea.WindowSize(),
	)
}

// waitForUpdate reads the next update from the watcher channel.
func waitForUpdate(ch <-chan sessions.UpdateMsg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return tea.Quit()
		}
		return snapshotMsg(msg)
	}
}

// -- Update (bubbletea v1 interface) --

func (m sessionsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return m.handleKey(msg)

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case snapshotMsg:
		if msg.Err != nil {
			m.err = msg.Err
		} else {
			m.snapshot = msg.Snapshot
			m.rebuildItems()
		}
		return m, waitForUpdate(m.updateCh)

	case actionErrorMsg:
		m.status = fmt.Sprintf("Error: %s", msg.err)
		return m, nil

	case statusMsg:
		m.status = string(msg)
		return m, nil
	}

	// Forward unrecognized messages to textinput when in rename mode
	// (handles cursor blink timer messages).
	if m.rename != nil {
		var cmd tea.Cmd
		m.rename.textInput, cmd = m.rename.textInput.Update(msg)
		return m, cmd
	}

	return m, nil
}

func (m sessionsModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// If in rename mode, handle text input.
	if m.rename != nil {
		return m.handleRename(msg)
	}

	// If in confirmation mode, handle y/n.
	if m.confirm != nil {
		return m.handleConfirm(msg)
	}

	switch msg.String() {
	case "q", "ctrl+c":
		m.cancel()
		return m, tea.Quit

	case "j", "down":
		if m.cursor < len(m.items)-1 {
			m.cursor++
		}
		return m, nil

	case "k", "up":
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil

	case " ", "enter":
		// Toggle expand/collapse on containers.
		if m.cursor < len(m.items) && m.items[m.cursor].kind == "container" {
			id := m.items[m.cursor].containerID
			m.expanded[id] = !m.expanded[id]
			m.rebuildItems()
		}
		return m, nil

	case "a":
		// Attach to the selected container. If stopped, start it first.
		item := m.selectedContainerItem()
		if item == nil || m.cli == nil {
			return m, nil
		}

		c := item.container
		if c.Status != "running" {
			// Start the stopped container, then attach.
			mgrRef := m.mgr
			cliRef := m.cli
			containerID := c.ID
			m.status = fmt.Sprintf("Starting %s...", c.DisplayName)
			return m, func() tea.Msg {
				ctx := context.Background()
				if err := mgrRef.StartContainer(ctx, containerID); err != nil {
					return actionErrorMsg{err: fmt.Errorf("start %s: %w", c.DisplayName, err)}
				}
				// Run post-start setup: re-inject auth and refresh firewall.
				if cliRef != nil {
					reinjectOnStart(ctx, cliRef, containerID)
				}
				snap, err := mgrRef.FetchSnapshot(ctx)
				if err != nil {
					return actionErrorMsg{err: err}
				}
				return snapshotMsg(sessions.UpdateMsg{Snapshot: snap})
			}
		}

		cliRef := m.cli
		containerID := c.ID
		m.pendingAttach = func() error {
			return attachAndCleanup(cliRef, containerID, []string{"claude", "--continue"})
		}
		m.cancel()
		return m, tea.Quit

	case "n":
		// Attach with a new (fresh) session.
		item := m.selectedContainerItem()
		if item == nil || item.container.Status != "running" {
			return m, nil
		}
		if m.cli == nil {
			return m, nil
		}

		c := item.container
		cliRef := m.cli
		containerID := c.ID
		m.pendingAttach = func() error {
			return attachAndCleanup(cliRef, containerID, []string{"claude"})
		}
		m.cancel()
		return m, tea.Quit

	case "e":
		// Rename — enter inline text input mode.
		// Works on both containers (depth 0) and sessions (depth 1).
		if m.cursor >= len(m.items) {
			return m, nil
		}
		item := m.items[m.cursor]
		switch item.kind {
		case "container":
			c := item.container
			ti, cmd := newRenameInput(c.DisplayName)
			m.rename = &renameState{
				containerID: c.ID,
				displayName: c.DisplayName,
				textInput:   ti,
			}
			return m, cmd
		case "session":
			s := item.session
			if s.Command != "claude" {
				return m, nil
			}

			// SessionID is populated by FetchSnapshot from `claude agents --json`.
			sessionID := s.SessionID
			if sessionID == "" {
				m.status = "Cannot rename: session ID not available"
				return m, nil
			}

			currentTitle := s.Title
			if currentTitle == "" {
				currentTitle = s.Command
			}
			ti, cmd := newRenameInput(currentTitle)
			m.rename = &renameState{
				containerID: item.containerID,
				sessionID:   sessionID,
				sessionPID:  s.PID,
				displayName: currentTitle,
				textInput:   ti,
			}
			return m, cmd
		default:
			return m, nil
		}

	case "s":
		// Stop — request confirmation first.
		item := m.selectedContainerItem()
		if item == nil || item.container.Status != "running" {
			return m, nil
		}
		m.confirm = &confirmState{
			action:      actionStop,
			displayName: item.container.DisplayName,
			containerID: item.container.ID,
		}
		return m, nil

	case "d":
		// Delete — request confirmation first.
		item := m.selectedContainerItem()
		if item == nil {
			return m, nil
		}
		m.confirm = &confirmState{
			action:      actionDelete,
			displayName: item.container.DisplayName,
			containerID: item.container.ID,
		}
		return m, nil

	case "r":
		// Restart — request confirmation first.
		item := m.selectedContainerItem()
		if item == nil || item.container.Status != "running" {
			return m, nil
		}
		m.confirm = &confirmState{
			action:      actionRestart,
			displayName: item.container.DisplayName,
			containerID: item.container.ID,
		}
		return m, nil
	}

	return m, nil
}

// newRenameInput creates a focused textinput pre-filled with value.
func newRenameInput(value string) (textinput.Model, tea.Cmd) {
	ti := textinput.New()
	ti.Prompt = ""
	ti.SetValue(value)
	cmd := ti.Focus()
	return ti, cmd
}

// handleRename processes keyboard input during rename mode.
func (m sessionsModel) handleRename(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		// Confirm rename.
		r := m.rename
		m.rename = nil
		newName := strings.TrimSpace(r.textInput.Value())
		if newName == "" || newName == r.displayName {
			return m, nil
		}
		mgrRef := m.mgr
		containerID := r.containerID

		if r.sessionID != "" {
			// Session-level rename: store a bunker-set title as a fallback/
			// override for when Claude's own agent name is empty or stale.
			sessionID := r.sessionID
			return m, func() tea.Msg {
				if err := sessions.SetSessionTitle(containerID, sessionID, newName); err != nil {
					return actionErrorMsg{err: fmt.Errorf("rename session: %w", err)}
				}
				snap, err := mgrRef.FetchSnapshot(context.Background())
				if err != nil {
					return actionErrorMsg{err: err}
				}
				return snapshotMsg(sessions.UpdateMsg{Snapshot: snap})
			}
		}

		// Container-level rename.
		return m, func() tea.Msg {
			if err := mgrRef.RenameContainer(context.Background(), containerID, newName); err != nil {
				return actionErrorMsg{err: fmt.Errorf("rename: %w", err)}
			}
			snap, err := mgrRef.FetchSnapshot(context.Background())
			if err != nil {
				return actionErrorMsg{err: err}
			}
			return snapshotMsg(sessions.UpdateMsg{Snapshot: snap})
		}

	case "esc", "ctrl+c":
		m.rename = nil
		return m, nil
	}

	// Delegate all other keys to the textinput component.
	var cmd tea.Cmd
	m.rename.textInput, cmd = m.rename.textInput.Update(msg)
	return m, cmd
}

// attachAndCleanup runs an interactive session in a container and stops
// the container if no other active sessions remain when the session exits.
func attachAndCleanup(cli *client.Client, containerID string, claudeCmd []string) error {
	ctx := context.Background()
	command := wrapWithAuth(ctx, cli, containerID, claudeCmd)
	exitCode, execID, err := ctr.ExecInteractive(ctx, cli, containerID, ctr.ContainerUser, command)
	if err != nil {
		return err
	}

	teardownAfterSession(ctx, cli, containerID, execID, false, false)

	if exitCode != 0 {
		return fmt.Errorf("session exited with code %d", exitCode)
	}
	return nil
}

// shouldStopAfterSession decides whether to tear down the container when an
// attached session exits. Fails closed: when other sessions are active or the
// check errored, the container is left running unless --force.
func shouldStopAfterSession(keep, otherActive bool, checkErr error, force bool) bool {
	if keep {
		return false
	}
	if checkErr != nil || otherActive {
		return force
	}
	return true
}

// teardownAfterSession stops the container via the Docker API (SIGTERM, 10s
// grace) only when shouldStopAfterSession says it is safe.
func teardownAfterSession(ctx context.Context, cli *client.Client, containerID, myExecID string, keep, force bool) {
	active, err := ctr.HasOtherActiveSessions(ctx, cli, containerID, myExecID)
	if !shouldStopAfterSession(keep, active, err, force) {
		info("Leaving container running (other sessions active or --keep).")
		return
	}
	info("Stopping container...")
	timeout := 10
	_ = cli.ContainerStop(ctx, containerID, dockercontainer.StopOptions{Timeout: &timeout})
}

// reinjectOnStart re-injects auth secrets after starting a stopped container.
// Secrets live on tmpfs which is lost when the container stops.
func reinjectOnStart(ctx context.Context, cli *client.Client, containerID string) {
	// Re-inject auth from environment (same precedence as cmd/run.go).
	auth := ctr.AuthTokens{
		ApiKey:     os.Getenv("ANTHROPIC_API_KEY"),
		OAuthToken: os.Getenv("CLAUDE_CODE_OAUTH_TOKEN"),
		GhToken:    os.Getenv("GITHUB_TOKEN"),
	}
	if auth.HasSecrets() {
		_ = ctr.InjectAuthSecrets(ctx, cli, containerID, auth)
	}
}

// handleConfirm processes y/n input during a confirmation prompt.
func (m sessionsModel) handleConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y":
		conf := m.confirm
		m.confirm = nil
		mgrRef := m.mgr

		cliRef := m.cli
		switch conf.action {
		case actionStop:
			return m, func() tea.Msg {
				if err := mgrRef.StopContainer(context.Background(), conf.containerID); err != nil {
					return actionErrorMsg{err: fmt.Errorf("stop %s: %w", conf.displayName, err)}
				}
				snap, err := mgrRef.FetchSnapshot(context.Background())
				if err != nil {
					return actionErrorMsg{err: err}
				}
				return snapshotMsg(sessions.UpdateMsg{Snapshot: snap})
			}

		case actionRestart:
			containerID := conf.containerID
			displayName := conf.displayName
			return m, func() tea.Msg {
				if err := mgrRef.StopContainer(context.Background(), containerID); err != nil {
					return actionErrorMsg{err: fmt.Errorf("stop %s: %w", displayName, err)}
				}
				if err := mgrRef.StartContainer(context.Background(), containerID); err != nil {
					return actionErrorMsg{err: fmt.Errorf("start %s: %w", displayName, err)}
				}
				if cliRef != nil {
					reinjectOnStart(context.Background(), cliRef, containerID)
				}
				snap, err := mgrRef.FetchSnapshot(context.Background())
				if err != nil {
					return actionErrorMsg{err: err}
				}
				return snapshotMsg(sessions.UpdateMsg{Snapshot: snap})
			}

		case actionDelete:
			return m, func() tea.Msg {
				if err := mgrRef.StopContainer(context.Background(), conf.containerID); err != nil {
					// Container might already be stopped; continue to remove.
				}
				if err := mgrRef.RemoveContainer(context.Background(), conf.containerID); err != nil {
					return actionErrorMsg{err: fmt.Errorf("delete %s: %w", conf.displayName, err)}
				}
				snap, err := mgrRef.FetchSnapshot(context.Background())
				if err != nil {
					return actionErrorMsg{err: err}
				}
				return snapshotMsg(sessions.UpdateMsg{Snapshot: snap})
			}
		}

	case "n", "N", "esc", "ctrl+c":
		m.confirm = nil
		return m, nil
	}

	// Ignore other keys during confirmation.
	return m, nil
}

// selectedContainerItem returns the container-level viewItem for the current cursor.
func (m sessionsModel) selectedContainerItem() *viewItem {
	if len(m.items) == 0 || m.cursor >= len(m.items) {
		return nil
	}
	item := m.items[m.cursor]
	// If cursor is on a session/subagent, walk back to the parent container.
	if item.kind != "container" {
		for i := m.cursor - 1; i >= 0; i-- {
			if m.items[i].kind == "container" {
				return &m.items[i]
			}
		}
		return nil
	}
	return &item
}

// rebuildItems flattens the snapshot into navigable viewItems.
func (m *sessionsModel) rebuildItems() {
	items := make([]viewItem, 0, len(m.snapshot.Containers)*3)

	for i := range m.snapshot.Containers {
		c := &m.snapshot.Containers[i]
		items = append(items, viewItem{
			kind:        "container",
			containerID: c.ID,
			container:   c,
			depth:       0,
		})

		if !m.expanded[c.ID] {
			continue
		}
		if c.Status != "running" {
			continue
		}

		for j := range c.Sessions {
			s := &c.Sessions[j]
			items = append(items, viewItem{
				kind:        "session",
				containerID: c.ID,
				container:   c,
				session:     s,
				depth:       1,
			})

			for k := range s.Subagents {
				items = append(items, viewItem{
					kind:        "subagent",
					containerID: c.ID,
					container:   c,
					subagent:    &s.Subagents[k],
					depth:       2,
				})
			}
		}
	}

	m.items = items

	// Keep cursor in bounds.
	if m.cursor >= len(items) {
		m.cursor = max(0, len(items)-1)
	}
}

// -- View --

func (m sessionsModel) View() string {
	var b strings.Builder

	// Header
	header := lipgloss.NewStyle().
		Bold(true).
		Foreground(colorBrand).
		Render("Claude Bunker Sessions")
	b.WriteString("\n  " + header + "\n\n")

	if m.err != nil {
		fmt.Fprintf(&b, "  Error: %s\n", m.err)
	}

	if m.status != "" {
		fmt.Fprintf(&b, "  %s\n\n", m.status)
	}

	if len(m.items) == 0 && m.err == nil {
		b.WriteString("  " + dimStyle.Render("No containers found. Start one with: claude-bunker") + "\n")
	}

	for i, item := range m.items {
		cursor := "  "
		if i == m.cursor {
			cursor = brandStyle.Render("> ")
		}

		switch item.kind {
		case "container":
			c := item.container
			dot := stateStyle(c.Status).Render("●")
			if c.Status != "running" {
				dot = dimStyle.Render("○")
			}
			uptime := ""
			if c.Status == "running" && !c.StartedAt.IsZero() {
				uptime = dimStyle.Render(fmt.Sprintf(" (%s)", sessions.FormatUptime(c.StartedAt)))
			}
			expand := "▶"
			if m.expanded[c.ID] {
				expand = "▼"
			}
			name := boldStyle.Render(c.DisplayName)
			// Show original name as dim suffix when a custom name is set.
			originalName := sessions.DisplayName(c.Name)
			nameSuffix := ""
			if c.DisplayName != originalName {
				nameSuffix = dimStyle.Render(fmt.Sprintf(" (%s)", originalName))
			}
			state := stateStyle(c.Status).Render(c.Status)
			fmt.Fprintf(&b, "%s%s %s %s %s%s%s\n", cursor, dot, expand, name, state, uptime, nameSuffix)

		case "session":
			s := item.session
			connector := dimStyle.Render("  ├─")
			label := s.Command
			if s.Title != "" {
				label = boldStyle.Render(s.Title) + dimStyle.Render(fmt.Sprintf(" (%s)", s.Command))
			}
			fmt.Fprintf(&b, "%s%s %s [pid %s]\n", cursor, connector, label, s.PID)

		case "subagent":
			a := item.subagent
			connector := dimStyle.Render("  │   ├─")
			fmt.Fprintf(&b, "%s%s %s [pid %s]\n", cursor, connector, a.Name, a.PID)
		}
	}

	// Rename prompt
	if m.rename != nil {
		b.WriteString("\n")
		fmt.Fprintf(&b, "  Rename: %s\n", m.rename.textInput.View())
		b.WriteString("  " + dimStyle.Render("[enter] confirm  [esc] cancel") + "\n")
	}

	// Confirmation prompt
	if m.confirm != nil {
		b.WriteString("\n")
		actionLabel := strings.ToUpper(m.confirm.action[:1]) + m.confirm.action[1:]
		prompt := fmt.Sprintf("  %s %s? [y/n]",
			lipgloss.NewStyle().Bold(true).Foreground(colorWarn).Render(actionLabel),
			boldStyle.Render(m.confirm.displayName))
		b.WriteString(prompt + "\n")
	}

	// Footer: keybinding help
	b.WriteString("\n")
	help := dimStyle.Render("  [a]ttach  [n]ew  [e]dit name  [s]top  [r]estart  [d]elete  [q]uit  [space] expand")
	b.WriteString(help + "\n")

	return b.String()
}
