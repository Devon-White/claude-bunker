package config

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// Volume and image name prefixes. Used by naming functions to construct names
// and by volume/image listing to discover claude-bunker resources.
const (
	BashHistoryVolumePrefix  = "claude-code-bashhistory-"
	ClaudeConfigVolumePrefix = "claude-code-config-"
	ImagePrefix              = "claude-bunker"
)

var nonAlnum = regexp.MustCompile(`[^a-z0-9-]+`)
var multiDash = regexp.MustCompile(`-{2,}`)

// ContainerName derives a deterministic container name from the workspace path.
// Uses the directory basename for readability plus a short hash of the full
// absolute path for uniqueness, so different projects with the same directory
// name (or git worktrees) get separate containers.
func ContainerName(workspace string) string {
	// Normalize to absolute path for consistent hashing across relative/absolute invocations
	abs, err := filepath.Abs(workspace)
	if err != nil {
		abs = workspace
	}
	// Normalize separators so the same directory hashes identically on Windows
	// regardless of slash style (e.g. C:\Users\... vs C:/Users/...)
	abs = filepath.ToSlash(abs)

	// Windows paths are case-insensitive: C:\Users and c:\users are the same
	// directory. Lowercase before hashing so cmd, PowerShell, and Git Bash
	// (which may report different casings) all produce the same container name.
	if runtime.GOOS == "windows" {
		abs = strings.ToLower(abs)
	}

	base := filepath.Base(workspace)
	name := strings.ToLower(base)
	name = nonAlnum.ReplaceAllString(name, "-")
	name = multiDash.ReplaceAllString(name, "-")
	name = strings.Trim(name, "-")
	if name == "" {
		name = "root"
	}

	// 8-char hash of the full path for uniqueness
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(abs)))[:8]

	return name + "-" + hash
}

// BashHistoryVolume returns the volume name for bash history persistence.
func BashHistoryVolume(containerName string) string {
	return BashHistoryVolumePrefix + containerName
}

// ClaudeConfigVolume returns the volume name for claude config persistence.
func ClaudeConfigVolume(containerName string) string {
	return ClaudeConfigVolumePrefix + containerName
}

// ImageTag returns the Docker image tag for this container.
func ImageTag(containerName string) string {
	return ImagePrefix + ":" + containerName
}

// DisplayName extracts the human-readable portion of a container name by
// stripping the trailing hash suffix (e.g., "project-alpha-a1b2c3d4" → "project-alpha").
// This is the inverse of ContainerName.
func DisplayName(name string) string {
	// Container names are "{basename}-{8-char-hash}". Strip the last dash + hash.
	idx := strings.LastIndex(name, "-")
	if idx < 0 {
		return name
	}
	suffix := name[idx+1:]
	// Verify suffix looks like a hex hash (8 chars).
	if len(suffix) == 8 && isHex(suffix) {
		return name[:idx]
	}
	return name
}

// isHex returns true if s contains only hexadecimal characters.
func isHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return len(s) > 0
}
