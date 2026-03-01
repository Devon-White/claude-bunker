package config

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
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
	return "claude-code-bashhistory-" + containerName
}

// ClaudeConfigVolume returns the volume name for claude config persistence.
func ClaudeConfigVolume(containerName string) string {
	return "claude-code-config-" + containerName
}

// ImageTag returns the Docker image tag for this container.
func ImageTag(containerName string) string {
	return "claude-bunker:" + containerName
}
