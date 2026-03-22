package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/docker/docker/client"

	"github.com/Devon-White/claude-bunker/internal/container"
)

const (
	// maxSessionFiles is the maximum number of session files to copy into the
	// container. When a project has more sessions than this, only the most
	// recent files (by modification time) are included.
	maxSessionFiles = 50

	// maxSessionBytes is the maximum total size of session files to copy.
	// Files are selected newest-first; once this budget is exhausted, remaining
	// files are skipped.
	maxSessionBytes = 50 * 1024 * 1024 // 50 MB
)

// SeedOpts holds all options for seeding sandbox settings into a container.
type SeedOpts struct {
	ContainerID  string
	Workspace    string
	ExtraDomains []string
	PluginLevel  string
	LogW         io.Writer
}

// SeedSettings writes managed-settings.json with the full sandbox config
// (including dynamic domain allowlist), copies host .claude/*.json into the
// container, and fixes ownership.
//
// The log writer controls where informational output is sent; pass
// io.Discard to suppress it.
func SeedSettings(ctx context.Context, cli *client.Client, opts SeedOpts) error {
	// Seed plugin files/configs before managed-settings so MCP configs are in place.
	if err := SeedPlugins(ctx, cli, opts); err != nil {
		fmt.Fprintf(opts.LogW, "[claude-bunker] WARNING: seeding plugins: %v\n", err)
	}

	// Write managed-settings.json with full sandbox config including domains.
	// This has the highest precedence in Claude Code and cannot be overridden.
	if err := writeManagedSettings(ctx, cli, opts); err != nil {
		return fmt.Errorf("managed-settings: %w", err)
	}

	hostClaudeDir := filepath.Join(opts.Workspace, ".claude")

	claudeDir := container.ContainerWorkspace + "/.claude"

	// Copy host's .claude/ tree into the container's tmpfs overlay,
	// skipping the .claude-bunker subdirectory (that's our own config).
	// Uses CopyDirToContainerExecWithChown which combines mkdir, tar extraction,
	// and chown -R into a single exec call, saving ~400ms of Docker API overhead
	// compared to separate EnsureContainerDir + CopyDirToContainerExec + ChownRecursive.
	if info, err := os.Stat(hostClaudeDir); err == nil && info.IsDir() {
		if err := container.CopyDirToContainerExecWithChown(ctx, cli, opts.ContainerID, hostClaudeDir, claudeDir,
			container.ContainerUserGroup,
			func(relPath string, isDir bool) bool {
				// Skip our own config subdirectory.
				if isDir && filepath.Base(relPath) == ".claude-bunker" {
					return true
				}
				// Skip workspace settings files — injected settings.json or
				// settings.local.json could override managed-settings.json,
				// weakening the sandbox. Legitimate config (commands/, agents/)
				// is still copied.
				if !isDir {
					base := filepath.Base(relPath)
					if base == "settings.json" || base == "settings.local.json" {
						return true
					}
				}
				return false
			}); err != nil {
			fmt.Fprintf(opts.LogW, "[claude-bunker] WARNING: copying .claude/ tree: %v\n", err)
		}
	} else {
		// No host .claude/ dir to copy, but still ensure the tmpfs directory
		// exists and is owned by the container user.
		if _, err := container.ExecNonInteractive(ctx, cli, opts.ContainerID, container.RootUser,
			[]string{"sh", "-c", fmt.Sprintf("mkdir -p %q && chown %s %q", claudeDir, container.ContainerUserGroup, claudeDir)}); err != nil {
			fmt.Fprintf(opts.LogW, "[claude-bunker] WARNING: setup %s: %v\n", claudeDir, err)
		}
	}

	return nil
}

// writeManagedSettings generates and writes /etc/claude-code/managed-settings.json
// with the full sandbox configuration including the dynamic domain allowlist.
//
// managed-settings.json has the highest precedence in Claude Code — it cannot
// be overridden by settings.json, settings.local.json, or the /sandbox command.
func writeManagedSettings(ctx context.Context, cli *client.Client, opts SeedOpts) error {
	// Build the sandbox domain list from the canonical builtin list plus
	// sandbox-only wildcards (e.g. *.github.com) and user extras.
	domains := container.BuiltinDomains()
	domains = append(domains, container.SandboxExtraDomains()...)
	domains = append(domains, opts.ExtraDomains...)

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
	if opts.PluginLevel != "" {
		settings["enableAllProjectMcpServers"] = true
	}

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling: %w", err)
	}
	data = append(data, '\n')

	const managedSettingsPath = container.ManagedSettingsDir + "/managed-settings.json"

	// Write and lock down managed-settings.json in a single exec. The base64
	// decode + chmod happen in one shell command, saving a Docker API round-trip
	// (~200ms) compared to separate CopyContentToContainer + chmod calls.
	if err := container.CopyContentToContainerWithMode(ctx, cli, opts.ContainerID, data, managedSettingsPath, "444"); err != nil {
		return fmt.Errorf("writing managed-settings.json: %w", err)
	}

	fmt.Fprintf(opts.LogW, "[claude-bunker] Wrote managed-settings.json with %d allowed domains\n", len(domains))
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
//
// To avoid slow startup on projects with large session histories, only the most
// recent files are copied, subject to maxSessionFiles and maxSessionBytes limits.
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

	// Scan the session directory and decide which files to include.
	allowed, skippedCount, skippedBytes := selectSessionFiles(hostSessionDir)

	if skippedCount > 0 {
		fmt.Fprintf(logW, "[claude-bunker] Session history capped: skipped %d files (%d MB) to stay within limits (%d files, %d MB)\n",
			skippedCount, skippedBytes/(1024*1024), maxSessionFiles, maxSessionBytes/(1024*1024))
	}

	// Copy session files into the container with correct ownership in a single exec.
	containerSessionDir := container.ContainerHome + "/.claude/projects/" + encodeProjectPath(container.ContainerWorkspace) + "/"
	if err := container.CopyDirToContainerExecWithChown(ctx, cli, containerID, hostSessionDir, containerSessionDir,
		container.ContainerUserGroup,
		func(relPath string, isDir bool) bool {
			if isDir {
				return false // always include directories for tar structure
			}
			_, ok := allowed[filepath.ToSlash(relPath)]
			return !ok // skip files not in the allowed set
		}); err != nil {
		return fmt.Errorf("copying session history: %w", err)
	}

	return nil
}

// sessionFileInfo holds metadata for a file in the session directory.
type sessionFileInfo struct {
	relPath string // slash-separated path relative to session dir
	size    int64
	modTime int64 // UnixNano for sorting
}

// selectSessionFiles scans hostSessionDir and returns:
//   - allowed: set of slash-separated relative paths to include
//   - skippedCount: number of files excluded by limits
//   - skippedBytes: total bytes excluded by limits
//
// Files are selected newest-first by modification time, subject to
// maxSessionFiles and maxSessionBytes.
func selectSessionFiles(hostSessionDir string) (allowed map[string]bool, skippedCount int, skippedBytes int64) {
	var files []sessionFileInfo

	base := filepath.Clean(hostSessionDir)
	_ = filepath.Walk(base, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info.IsDir() {
			return nil
		}
		relPath, err := filepath.Rel(base, path)
		if err != nil {
			return nil
		}
		files = append(files, sessionFileInfo{
			relPath: filepath.ToSlash(relPath),
			size:    info.Size(),
			modTime: info.ModTime().UnixNano(),
		})
		return nil
	})

	// Fast path: if everything fits within limits, include all files.
	if len(files) <= maxSessionFiles {
		var totalSize int64
		for _, f := range files {
			totalSize += f.size
		}
		if totalSize <= maxSessionBytes {
			allowed = make(map[string]bool, len(files))
			for _, f := range files {
				allowed[f.relPath] = true
			}
			return allowed, 0, 0
		}
	}

	// Sort by modification time descending (newest first).
	sort.Slice(files, func(i, j int) bool {
		return files[i].modTime > files[j].modTime
	})

	allowed = make(map[string]bool)
	var cumSize int64
	for _, f := range files {
		if len(allowed) >= maxSessionFiles || cumSize+f.size > maxSessionBytes {
			skippedCount++
			skippedBytes += f.size
			continue
		}
		allowed[f.relPath] = true
		cumSize += f.size
	}

	return allowed, skippedCount, skippedBytes
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
