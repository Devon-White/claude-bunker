package sessions

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/docker/docker/client"

	ctr "github.com/Devon-White/claude-bunker/internal/container"
)

// Session Title Sync — Abstraction Layer
//
// This file implements bidirectional sync between claude-bunker's session
// names and Claude Code's internal session title storage.
//
// IMPORTANT: This is a WORKAROUND for the lack of an official Claude Code
// session rename API. Claude Code currently stores custom titles by appending
// {"type":"custom-title","customTitle":"..."} entries to session JSONL files
// and scanning the last 64KB of each file for titles. This has known bugs:
//
//   - 64KB eviction: on long sessions the title gets pushed out of the scan window
//   - Content contamination: "customTitle" in tool output can be picked up as title
//   - last-prompt overwrite: session close appends a last-prompt entry that wins
//
// See: https://github.com/anthropics/claude-code/issues/32150
// See: https://github.com/anthropics/claude-code/issues/33165
//
// When Claude Code ships an official rename API (e.g., a VS Code command,
// a CLI flag, or a title-registry.json), replace the implementation of
// TitleSyncer with a call to that API. The interface is designed so callers
// (the TUI, CLI commands) don't need to change — only this file.

// TitleSyncer manages session title synchronization between claude-bunker
// and Claude Code's internal storage. Designed as an interface so the
// implementation can be swapped when an official API becomes available.
type TitleSyncer interface {
	// SetTitle sets the title for a session and syncs it to Claude Code.
	// containerID identifies the Docker container, sessionID is the Claude
	// Code session UUID, and title is the desired display name.
	SetTitle(ctx context.Context, containerID, sessionID, title string) error

	// GetTitle returns the title for a session, or empty string if none set.
	GetTitle(containerID, sessionID string) string

	// RefreshTitles re-applies all known titles to their JSONL files.
	// Call periodically to keep titles within Claude Code's 64KB scan window.
	// This is a workaround for the 64KB eviction bug and will be unnecessary
	// once Claude Code ships proper title persistence.
	RefreshTitles(ctx context.Context, containerID string) error

	// ResolveSessionIDs reads all active Claude Code session metadata from
	// inside the container and returns a map of container-namespace PID → sessionID.
	// Callers must handle the host-to-container PID translation themselves.
	ResolveSessionIDs(ctx context.Context, containerID string) (map[int]string, error)

	// PushTitle stores a title received from a hook event (no container exec needed).
	// This is the fast path: the Stop hook reads the title inside the container
	// and sends it to the host via the socket, bypassing docker exec entirely.
	PushTitle(containerID, sessionID, title string)

	// ReadTitleFromContainer reads the current custom-title from a session's
	// JSONL file inside the container. This picks up titles set by Claude
	// Code's /rename command. Returns empty string if no title is found.
	ReadTitleFromContainer(ctx context.Context, containerID, sessionID string) string
}

// titleStore is the persistent store for session titles.
// Stored at ~/.claude/session-titles.json on the host.
// Keys are "containerID:sessionID" → title.
//
// This store is the source of truth for session names. The JSONL
// custom-title entries are downstream copies that may be lost due to
// Claude Code's 64KB scanning bug. The store allows re-application.
var titleStore = newJSONMapStore("session-titles.json")

func registryKey(containerID, sessionID string) string {
	return containerID + ":" + sessionID
}

// allTitlesForContainer returns all titles for sessions in a given container.
func allTitlesForContainer(containerID string) map[string]string {
	all := titleStore.All()
	prefix := containerID + ":"
	result := make(map[string]string)
	for key, title := range all {
		if strings.HasPrefix(key, prefix) {
			sessionID := strings.TrimPrefix(key, prefix)
			result[sessionID] = title
		}
	}
	return result
}

// jsonlTitleSyncer implements TitleSyncer using Claude Code's JSONL
// custom-title entries. This is the workaround implementation.
//
// REPLACE THIS: When Claude Code ships an official rename API, create a
// new implementation of TitleSyncer that calls that API instead of
// manipulating JSONL files directly. The registry can be kept as a cache
// or removed entirely if the API provides its own persistence.
type jsonlTitleSyncer struct {
	// cli is the Docker client used to exec commands inside containers.
	// We need the concrete *client.Client (not the interface) because
	// ExecNonInteractive requires it.
	cli *client.Client
}

// NewTitleSyncer creates a TitleSyncer that writes custom-title entries
// to Claude Code's session JSONL files.
//
// This is a WORKAROUND implementation. When Claude Code provides an
// official session rename API, replace this constructor to return an
// implementation that uses that API instead.
func NewTitleSyncer(cli *client.Client) TitleSyncer {
	return &jsonlTitleSyncer{cli: cli}
}

func (s *jsonlTitleSyncer) SetTitle(ctx context.Context, containerID, sessionID, title string) error {
	// 1. Save to our persistent store (source of truth).
	if err := titleStore.Set(registryKey(containerID, sessionID), title); err != nil {
		return fmt.Errorf("saving title to store: %w", err)
	}

	// 2. Write the custom-title entry to the JSONL file inside the container.
	//    This is the part that syncs with Claude Code's session list / --resume.
	if err := s.writeCustomTitle(ctx, containerID, sessionID, title); err != nil {
		return fmt.Errorf("writing custom-title to JSONL: %w", err)
	}

	return nil
}

func (s *jsonlTitleSyncer) PushTitle(containerID, sessionID, title string) {
	if title == "" || sessionID == "" {
		return
	}
	_ = titleStore.Set(registryKey(containerID, sessionID), title)
}

func (s *jsonlTitleSyncer) GetTitle(containerID, sessionID string) string {
	return titleStore.Get(registryKey(containerID, sessionID))
}

// RefreshTitles re-appends custom-title entries for all known sessions
// in a container. This defeats Claude Code's 64KB tail-scan eviction:
// by re-appending, the title entry is always near the end of the file.
//
// WORKAROUND: This entire method exists because Claude Code scans only
// the last 64KB of JSONL files for titles. Once Claude Code fixes this
// (e.g., by using a separate title index), this method becomes a no-op.
func (s *jsonlTitleSyncer) RefreshTitles(ctx context.Context, containerID string) error {
	titles := allTitlesForContainer(containerID)
	if len(titles) == 0 {
		return nil
	}
	for sessionID, title := range titles {
		// Best-effort: don't fail the whole refresh if one session's file is gone.
		_ = s.writeCustomTitle(ctx, containerID, sessionID, title)
	}
	return nil
}

func (s *jsonlTitleSyncer) ResolveSessionIDs(ctx context.Context, containerID string) (map[int]string, error) {
	if s.cli == nil {
		return nil, fmt.Errorf("no Docker client available")
	}

	// Read all session files from ~/.claude/sessions/ inside the container.
	// Each file is named <container-PID>.json and contains:
	//   {"pid":<PID>,"sessionId":"<UUID>","cwd":"...","startedAt":<timestamp>}
	//
	// IMPORTANT: These PIDs are in the CONTAINER namespace. The caller
	// (resolveSessionTitles) receives host-namespace PIDs from `docker top`
	// and must translate them. We return container-namespace PIDs here.
	sessionsDir := fmt.Sprintf("%s/.claude/sessions", ctr.ContainerHome)
	script := fmt.Sprintf(
		`cd %s 2>/dev/null && for f in *.json; do [ -f "$f" ] && cat "$f" && printf '\n'; done`,
		sessionsDir)
	output, err := ctr.ExecNonInteractive(ctx, s.cli, containerID, ctr.ContainerUser,
		[]string{"sh", "-c", script})
	if err != nil {
		return nil, fmt.Errorf("listing session files: %w", err)
	}

	type sessionMeta struct {
		PID       int    `json:"pid"`
		SessionID string `json:"sessionId"`
	}

	result := make(map[int]string)
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var meta sessionMeta
		if err := json.Unmarshal([]byte(line), &meta); err != nil {
			continue
		}
		if meta.PID > 0 && meta.SessionID != "" {
			result[meta.PID] = meta.SessionID
		}
	}

	return result, nil
}

// writeCustomTitle appends a custom-title JSON entry to the session's JSONL file.
//
// WORKAROUND: This directly manipulates Claude Code's internal storage format.
// The entry format is: {"type":"custom-title","sessionId":"<UUID>","customTitle":"<title>"}
// Claude Code's sidebar/--resume scanner looks for this entry type in the last
// 64KB of the file to determine the display title.
//
// Known limitations (from github.com/anthropics/claude-code/issues/32150):
//   - Entry may be evicted from the 64KB scan window on long sessions
//   - A "last-prompt" entry appended on session close may override it
//   - The string "customTitle" in tool output can cause false matches
//
// RefreshTitles() mitigates the first issue by periodically re-appending.
func (s *jsonlTitleSyncer) writeCustomTitle(ctx context.Context, containerID, sessionID, title string) error {
	if s.cli == nil {
		return fmt.Errorf("no Docker client available")
	}

	// Build the JSONL entry. This matches Claude Code's internal format
	// as observed in session JSONL files and confirmed by issue #32150.
	entry := map[string]interface{}{
		"type":        "custom-title",
		"sessionId":   sessionID,
		"customTitle": title,
		"timestamp":   time.Now().UnixMilli(),
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}

	// Find the JSONL file path inside the container.
	// Claude Code stores sessions at:
	//   ~/.claude/projects/<encoded-workspace-path>/<sessionID>.jsonl
	//
	// The workspace inside the container is always /workspace, which encodes
	// to "-workspace" using Claude Code's path encoding (replace / with -).
	jsonlPath := fmt.Sprintf("%s/.claude/projects/-workspace/%s.jsonl",
		ctr.ContainerHome, sessionID)

	// Append the entry to the JSONL file.
	// Using printf with \n to ensure proper line termination.
	// The shell command is run as the container user to match file ownership.
	appendCmd := fmt.Sprintf(`printf '%%s\n' '%s' >> %s`,
		strings.ReplaceAll(string(data), "'", "'\\''"),
		jsonlPath)

	_, err = ctr.ExecNonInteractive(ctx, s.cli, containerID, ctr.ContainerUser,
		[]string{"sh", "-c", appendCmd})
	if err != nil {
		return fmt.Errorf("appending custom-title: %w", err)
	}

	return nil
}

// ReadTitleFromContainer reads the custom-title from a session's JSONL file
// inside the container. This detects titles set by Claude Code's /rename
// command, enabling bidirectional sync.
//
// WORKAROUND: Scans the last 64KB of the JSONL file (matching Claude Code's
// own scanner behavior) for {"type":"custom-title","customTitle":"..."} entries.
// Takes the last match (most recent rename). When Claude Code ships a proper
// title API, replace this with an API call.
func (s *jsonlTitleSyncer) ReadTitleFromContainer(ctx context.Context, containerID, sessionID string) string {
	if s.cli == nil {
		return ""
	}

	jsonlPath := fmt.Sprintf("%s/.claude/projects/-workspace/%s.jsonl",
		ctr.ContainerHome, sessionID)

	// Read the last 64KB of the JSONL file and extract custom-title entries.
	// Using tail -c to match Claude Code's own 64KB scan window.
	// grep for the specific type to avoid false matches from conversation content.
	script := fmt.Sprintf(
		`tail -c 65536 %s 2>/dev/null | grep '"type":"custom-title"' | tail -1`,
		jsonlPath)
	output, err := ctr.ExecNonInteractive(ctx, s.cli, containerID, ctr.ContainerUser,
		[]string{"sh", "-c", script})
	if err != nil || strings.TrimSpace(output) == "" {
		return ""
	}

	var entry struct {
		Type        string `json:"type"`
		CustomTitle string `json:"customTitle"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &entry); err != nil {
		return ""
	}
	if entry.Type == "custom-title" && entry.CustomTitle != "" {
		return entry.CustomTitle
	}
	return ""
}

// GetSessionTitle returns the title for a session from the registry.
// Convenience function for use outside the TitleSyncer interface.
func GetSessionTitle(containerID, sessionID string) string {
	return titleStore.Get(registryKey(containerID, sessionID))
}
