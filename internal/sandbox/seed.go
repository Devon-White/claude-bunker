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


// SeedSettings writes managed-settings.json with the full sandbox config
// (including dynamic domain allowlist), copies host .claude/*.json into the
// container, and fixes ownership.
//
// The log writer controls where informational output is sent; pass
// io.Discard to suppress it.
func SeedSettings(ctx context.Context, cli *client.Client, containerID, workspace string, extraDomains []string, pluginLevel string, logW io.Writer) error {
	// Seed plugin files/configs before managed-settings so MCP configs are in place.
	if err := SeedPlugins(ctx, cli, containerID, workspace, pluginLevel, logW); err != nil {
		fmt.Fprintf(logW, "[claude-bunker] WARNING: seeding plugins: %v\n", err)
	}

	// Write managed-settings.json with full sandbox config including domains.
	// This has the highest precedence in Claude Code and cannot be overridden.
	if err := writeManagedSettings(ctx, cli, containerID, extraDomains, pluginLevel, logW); err != nil {
		return fmt.Errorf("managed-settings: %w", err)
	}

	hostClaudeDir := filepath.Join(workspace, ".claude")

	claudeDir := container.ContainerWorkspace + "/.claude"

	// Ensure the tmpfs at <workspace>/.claude exists and is owned by the
	// container user (Docker mounts tmpfs as root by default).
	// mkdir first so chown doesn't fail if the directory is missing.
	if _, err := container.ExecNonInteractive(ctx, cli, containerID, container.RootUser,
		[]string{"sh", "-c", "mkdir -p " + claudeDir + " && chown " + container.ContainerUserGroup + " " + claudeDir}); err != nil {
		fmt.Fprintf(logW, "[claude-bunker] WARNING: setup %s: %v\n", claudeDir, err)
	}

	// Copy host's .claude/ tree into the container's tmpfs overlay,
	// skipping the .claude-bunker subdirectory (that's our own config).
	if info, err := os.Stat(hostClaudeDir); err == nil && info.IsDir() {
		if err := container.CopyDirToContainerExec(ctx, cli, containerID, hostClaudeDir, claudeDir,
			func(relPath string, isDir bool) bool {
				return isDir && filepath.Base(relPath) == ".claude-bunker"
			}); err != nil {
			fmt.Fprintf(logW, "[claude-bunker] WARNING: copying .claude/ tree: %v\n", err)
		}
	}

	// Fix ownership — CopyToContainer creates files as root, but Claude Code
	// runs as the container user.
	if _, err := container.ExecNonInteractive(ctx, cli, containerID, container.RootUser,
		[]string{"chown", "-R", container.ContainerUserGroup, claudeDir}); err != nil {
		fmt.Fprintf(logW, "[claude-bunker] WARNING: chown -R %s: %v\n", claudeDir, err)
	}

	return nil
}

// writeManagedSettings generates and writes /etc/claude-code/managed-settings.json
// with the full sandbox configuration including the dynamic domain allowlist.
//
// managed-settings.json has the highest precedence in Claude Code — it cannot
// be overridden by settings.json, settings.local.json, or the /sandbox command.
func writeManagedSettings(ctx context.Context, cli *client.Client, containerID string, extraDomains []string, pluginLevel string, logW io.Writer) error {
	// Build the sandbox domain list from the canonical builtin list plus
	// sandbox-only wildcards (e.g. *.github.com) and user extras.
	domains := container.BuiltinDomains()
	domains = append(domains, container.SandboxExtraDomains()...)
	domains = append(domains, extraDomains...)

	settings := map[string]interface{}{
		"sandbox": map[string]interface{}{
			"enabled":                   true,
			"allowUnsandboxedCommands":  false,
			"enableWeakerNestedSandbox": true,
			"writableRoots": []string{
				container.ContainerHome + "/.cache",
			},
			"network": map[string]interface{}{
				"allowedDomains": domains,
			},
		},
	}

	// When plugins are enabled, auto-allow project MCP servers
	if pluginLevel != "" {
		settings["enableAllProjectMcpServers"] = true
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
	_, err = container.ExecNonInteractive(ctx, cli, containerID, container.RootUser,
		[]string{"chmod", "444", managedSettingsPath})
	if err != nil {
		return fmt.Errorf("chmod managed-settings.json: %w", err)
	}

	fmt.Fprintf(logW, "[claude-bunker] Wrote managed-settings.json with %d allowed domains\n", len(domains))
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
func SeedSessionHistory(ctx context.Context, cli *client.Client, containerID, workspace string, logW io.Writer) error {
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

	// Ensure the container's session directory exists.
	// Use encodeProjectPath to derive the encoded form of the container workspace
	// path, matching how Claude Code encodes project paths internally.
	containerSessionDir := container.ContainerHome + "/.claude/projects/" + encodeProjectPath(container.ContainerWorkspace) + "/"
	if _, err := container.ExecNonInteractive(ctx, cli, containerID, container.ContainerUser,
		[]string{"mkdir", "-p", containerSessionDir}); err != nil {
		fmt.Fprintf(logW, "[claude-bunker] WARNING: mkdir -p %s: %v\n", containerSessionDir, err)
	}

	// Copy all session files (transcripts, sub-agents, tool results)
	if err := container.CopyDirToContainer(ctx, cli, containerID, hostSessionDir, containerSessionDir); err != nil {
		return fmt.Errorf("copying session history: %w", err)
	}

	// Fix ownership — CopyToContainer creates files as root, but Claude runs as claude-bunker
	if _, err := container.ExecNonInteractive(ctx, cli, containerID, container.RootUser,
		[]string{"chown", "-R", container.ContainerUserGroup, containerSessionDir}); err != nil {
		fmt.Fprintf(logW, "[claude-bunker] WARNING: chown -R %s: %v\n", containerSessionDir, err)
	}

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
