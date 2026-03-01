package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/docker/docker/client"

	"github.com/Devon-White/claude-bunker/internal/container"
)

// baseDomains are the default allowed domains for Claude Code's sandbox.
var baseDomains = []string{
	"api.anthropic.com",
	"statsig.anthropic.com",
	"statsig.com",
	"sentry.io",
	"github.com",
	"*.github.com",
	"registry.npmjs.org",
	"pypi.org",
	"files.pythonhosted.org",
}

// containerUserGroup is the "user:group" string used for chown operations.
var containerUserGroup = container.ContainerUser + ":" + container.ContainerUser

// SeedSettings writes managed-settings.json with the full sandbox config
// (including dynamic domain allowlist), copies host .claude/*.json into the
// container, and fixes ownership.
//
// The log writer controls where informational output is sent; pass
// io.Discard to suppress it.
func SeedSettings(ctx context.Context, cli *client.Client, containerID, workspace, extraDomains string, log io.Writer) error {
	// Write managed-settings.json with full sandbox config including domains.
	// This has the highest precedence in Claude Code and cannot be overridden.
	if err := writeManagedSettings(ctx, cli, containerID, extraDomains, log); err != nil {
		return fmt.Errorf("managed-settings: %w", err)
	}

	hostClaudeDir := filepath.Join(workspace, ".claude")

	// Ensure the tmpfs at /workspace/.claude is owned by the container user
	// and the directory exists (Docker mounts tmpfs as root by default).
	_, _ = container.ExecNonInteractive(ctx, cli, containerID, "root",
		[]string{"sh", "-c", "chown " + containerUserGroup + " /workspace/.claude && mkdir -p /workspace/.claude"})

	// Copy host's .claude/*.json into the container's tmpfs overlay.
	if info, err := os.Stat(hostClaudeDir); err == nil && info.IsDir() {
		matches, err := filepath.Glob(filepath.Join(hostClaudeDir, "*.json"))
		if err != nil {
			return fmt.Errorf("globbing .claude/*.json: %w", err)
		}

		for _, f := range matches {
			base := filepath.Base(f)
			if err := container.CopyFileToContainer(ctx, cli, containerID, f, "/workspace/.claude/"); err != nil {
				fmt.Fprintf(log, "[claude-bunker] WARNING: Failed to copy %s: %v\n", base, err)
			}
		}
	}

	// Fix ownership of all files in /workspace/.claude/ — CopyToContainer
	// creates files as root, but Claude Code runs as claude-bunker.
	_, _ = container.ExecNonInteractive(ctx, cli, containerID, "root",
		[]string{"chown", "-R", containerUserGroup, "/workspace/.claude"})

	return nil
}

// writeManagedSettings generates and writes /etc/claude-code/managed-settings.json
// with the full sandbox configuration including the dynamic domain allowlist.
//
// managed-settings.json has the highest precedence in Claude Code — it cannot
// be overridden by settings.json, settings.local.json, or the /sandbox command.
func writeManagedSettings(ctx context.Context, cli *client.Client, containerID, extraDomains string, log io.Writer) error {
	domains := make([]string, len(baseDomains))
	copy(domains, baseDomains)
	if extraDomains != "" {
		for _, d := range strings.Split(extraDomains, ",") {
			d = strings.TrimSpace(d)
			if d != "" {
				domains = append(domains, d)
			}
		}
	}

	settings := map[string]interface{}{
		"sandbox": map[string]interface{}{
			"enabled":                   true,
			"allowUnsandboxedCommands":  false,
			"enableWeakerNestedSandbox": true,
			"network": map[string]interface{}{
				"allowedDomains": domains,
			},
		},
	}

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling: %w", err)
	}
	data = append(data, '\n')

	const managedSettingsPath = container.ManagedSettingsDir + "/managed-settings.json"

	if err := container.CopyContentToContainer(ctx, cli, containerID, data, managedSettingsPath); err != nil {
		return fmt.Errorf("writing managed-settings.json: %w", err)
	}

	// Make it read-only and owned by root to prevent tampering
	_, err = container.ExecNonInteractive(ctx, cli, containerID, "root",
		[]string{"chmod", "444", managedSettingsPath})
	if err != nil {
		return fmt.Errorf("chmod managed-settings.json: %w", err)
	}

	fmt.Fprintf(log, "[claude-bunker] Wrote managed-settings.json with %d allowed domains\n", len(domains))
	return nil
}

// SeedSessionHistory copies session history from the host's ~/.claude/projects/
// into the container so that `claude --resume` shows previous conversations.
//
// Host sessions live at:   ~/.claude/projects/<encoded-host-path>/*.jsonl
// Container sessions at:   /home/claude-bunker/.claude/projects/-workspace/*.jsonl
//
// The path encoding differs (host uses full path, container uses /workspace),
// so we map one to the other.
func SeedSessionHistory(ctx context.Context, cli *client.Client, containerID, workspace string) error {
	// Find the host's Claude config directory
	hostClaudeHome, err := findHostClaudeHome()
	if err != nil {
		return nil // no host claude config, nothing to seed
	}

	// Encode the workspace path the way Claude does
	encodedPath := encodeProjectPath(workspace)
	hostSessionDir := filepath.Join(hostClaudeHome, "projects", encodedPath)

	info, err := os.Stat(hostSessionDir)
	if err != nil || !info.IsDir() {
		return nil // no sessions for this project on host
	}

	// Ensure the container's session directory exists
	containerSessionDir := container.ContainerHome + "/.claude/projects/-workspace/"
	_, _ = container.ExecNonInteractive(ctx, cli, containerID, container.ContainerUser,
		[]string{"mkdir", "-p", containerSessionDir})

	// Copy all session files (transcripts, sub-agents, tool results)
	if err := container.CopyDirToContainer(ctx, cli, containerID, hostSessionDir, containerSessionDir); err != nil {
		return fmt.Errorf("copying session history: %w", err)
	}

	// Fix ownership — CopyToContainer creates files as root, but Claude runs as claude-bunker
	_, _ = container.ExecNonInteractive(ctx, cli, containerID, "root",
		[]string{"chown", "-R", containerUserGroup, containerSessionDir})

	return nil
}

// findHostClaudeHome returns the path to ~/.claude/ on the host.
func findHostClaudeHome() (string, error) {
	// Check CLAUDE_CONFIG_DIR env var first
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir, nil
		}
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	dir := filepath.Join(home, ".claude")
	if info, err := os.Stat(dir); err == nil && info.IsDir() {
		return dir, nil
	}

	return "", fmt.Errorf(".claude directory not found")
}

// encodeProjectPath encodes a workspace path the way Claude Code does:
// replace : with -, replace \ and / with -, collapse multiples.
// e.g. C:\Users\devon\projects\my-app -> C--Users-devon-projects-my-app
func encodeProjectPath(path string) string {
	// Normalize to absolute path
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}

	// Replace : and path separators with -
	encoded := strings.ReplaceAll(abs, ":", "-")
	encoded = strings.ReplaceAll(encoded, "\\", "-")
	encoded = strings.ReplaceAll(encoded, "/", "-")

	return encoded
}
