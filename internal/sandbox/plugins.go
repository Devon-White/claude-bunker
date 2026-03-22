package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"

	"github.com/docker/docker/client"

	"github.com/Devon-White/claude-bunker/internal/config"
	"github.com/Devon-White/claude-bunker/internal/container"
)

// safeCmdPattern matches command names that are safe to embed in a shell fragment.
// Only alphanumeric characters, dashes, underscores, dots, and forward slashes are allowed.
var safeCmdPattern = regexp.MustCompile(`^[a-zA-Z0-9_./-]+$`)

// ExtractPluginDomains reads MCP configs from host files and extracts HTTP/SSE
// server domains for firewall allowlisting. Called during resolveNaming() before
// the container exists.
func ExtractPluginDomains(workspace, pluginLevel string) []string {
	if pluginLevel == "" {
		return nil
	}

	var domains []string

	// "project" level and above: read workspace .mcp.json
	if config.PluginLevelAtLeast(pluginLevel, config.PluginLevelProject) {
		projectMCP := filepath.Join(workspace, ".mcp.json")
		domains = append(domains, extractMCPDomains(projectMCP)...)
	}

	// "user" level and above: read ~/.claude/settings.json, ~/.claude.json,
	// and plugin cache .mcp.json files (marketplace plugins with HTTP MCP servers)
	if config.PluginLevelAtLeast(pluginLevel, config.PluginLevelUser) {
		home, err := os.UserHomeDir()
		if err == nil {
			// Primary: MCP servers in ~/.claude/settings.json
			userSettings := filepath.Join(home, ".claude", "settings.json")
			domains = append(domains, extractMCPDomains(userSettings)...)
			// Fallback: some setups put MCP servers in ~/.claude.json
			userMCP := filepath.Join(home, ".claude.json")
			domains = append(domains, extractMCPDomains(userMCP)...)
			// Plugin cache: each installed plugin may have its own .mcp.json
			// with HTTP MCP servers (e.g. GitHub plugin → api.githubcopilot.com)
			pluginCacheDir := filepath.Join(home, ".claude", "plugins", "cache")
			domains = append(domains, extractPluginCacheDomains(pluginCacheDir)...)
		}
	}

	// "all" level: read managed-mcp.json
	if config.PluginLevelAtLeast(pluginLevel, config.PluginLevelAll) {
		if p := hostManagedMCPPath(); p != "" {
			domains = append(domains, extractMCPDomains(p)...)
		}
	}

	// Deduplicate
	seen := make(map[string]bool, len(domains))
	var unique []string
	for _, d := range domains {
		if !seen[d] {
			seen[d] = true
			unique = append(unique, d)
		}
	}

	// Validate through the same domain validator used by allowDomains.
	// Drop invalid domains silently (they come from external configs, not user input).
	var valid []string
	for _, d := range unique {
		if config.IsValidDomain(d) {
			valid = append(valid, d)
		}
	}

	return valid
}

// SeedPlugins copies plugin files and configs into the running container.
func SeedPlugins(ctx context.Context, cli *client.Client, opts SeedOpts) error {
	if !config.PluginLevelAtLeast(opts.PluginLevel, config.PluginLevelUser) {
		return nil // "project" is a no-op — .mcp.json is already bind-mounted via workspace
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("getting home dir: %w", err)
	}

	// Copy plugin-related keys from ~/.claude/settings.json into the
	// container's user-scope settings. This is where enabledPlugins,
	// extraKnownMarketplaces, and mcpServers actually live.
	hostSettingsJSON := filepath.Join(home, ".claude", "settings.json")
	containerSettingsJSON := container.ContainerHome + "/.claude/settings.json"
	// Collect runtime checks across all sources, then batch-verify in one exec
	runtimeSeen := make(map[string]bool)
	var runtimeChecks []runtimeCheck

	settingsData, err := os.ReadFile(hostSettingsJSON)
	if err != nil {
		fmt.Fprintf(opts.LogW, "[claude-bunker] WARNING: reading ~/.claude/settings.json: %v\n", err)
	} else {
		if err := copyFilteredSettings(ctx, cli, opts.ContainerID, settingsData, containerSettingsJSON, opts.LogW); err != nil {
			fmt.Fprintf(opts.LogW, "[claude-bunker] WARNING: copying settings.json plugin keys: %v\n", err)
		}
		runtimeChecks = append(runtimeChecks, collectStdioRuntimes(settingsData, runtimeSeen)...)
	}

	// Also copy ~/.claude.json if it has mcpServers (some setups use this)
	containerClaudeJSON := container.ContainerHome + "/.claude.json"
	userClaudeJSON := filepath.Join(home, ".claude.json")
	if claudeData, err := os.ReadFile(userClaudeJSON); err == nil {
		if err := copyFilteredClaudeJSON(ctx, cli, opts.ContainerID, claudeData, containerClaudeJSON, opts.LogW); err != nil {
			fmt.Fprintf(opts.LogW, "[claude-bunker] WARNING: copying ~/.claude.json: %v\n", err)
		}
	}

	// Copy entire plugins directory (installed_plugins.json, known_marketplaces.json,
	// config.json, cache/, marketplaces/, repos/). Skip daemon state files (*.pid, *.log)
	// that are host-specific.
	pluginsDir := filepath.Join(home, ".claude", "plugins")
	containerPluginsDir := container.ContainerHome + "/.claude/plugins"
	if info, err := os.Stat(pluginsDir); err == nil && info.IsDir() {
		if err := container.EnsureContainerDir(ctx, cli, opts.ContainerID, containerPluginsDir); err != nil {
			fmt.Fprintf(opts.LogW, "[claude-bunker] WARNING: mkdir plugins dir: %v\n", err)
		}
		if err := container.CopyDirToContainerExec(ctx, cli, opts.ContainerID, pluginsDir, containerPluginsDir,
			func(relPath string, isDir bool) bool {
				base := filepath.Base(relPath)
				// Skip .git, repos, and marketplaces dirs — these are git
				// clones that Claude Code will re-clone on demand. Copying
				// them without .git creates broken repos, and marketplace
				// paths in known_marketplaces.json reference host paths that
				// cause mangled directory names on Linux.
				if isDir && (base == ".git" || base == "repos" || base == "marketplaces") {
					return true
				}
				// Skip daemon runtime files (PIDs, logs) — host-specific
				ext := filepath.Ext(relPath)
				return ext == ".pid" || ext == ".log"
			}); err != nil {
			fmt.Fprintf(opts.LogW, "[claude-bunker] WARNING: copying plugins dir: %v\n", err)
		} else {
			fmt.Fprintf(opts.LogW, "[claude-bunker] Copied plugins directory\n")
		}
		// Rewrite installPath values in installed_plugins.json from host
		// paths (e.g. C:\Users\devon\.claude\plugins\...) to container paths
		rewritePluginPaths(ctx, cli, opts.ContainerID, pluginsDir, containerPluginsDir, opts.LogW)
		// Collect stdio runtimes from plugin cache .mcp.json files
		pluginCacheDir := filepath.Join(pluginsDir, "cache")
		runtimeChecks = append(runtimeChecks, collectPluginCacheRuntimes(pluginCacheDir, runtimeSeen)...)
	}

	// "all" level: copy managed-mcp.json
	if config.PluginLevelAtLeast(opts.PluginLevel, config.PluginLevelAll) {
		if p := hostManagedMCPPath(); p != "" {
			if data, err := os.ReadFile(p); err == nil {
				const managedMCPPath = container.ManagedSettingsDir + "/managed-mcp.json"
				if err := container.CopyContentToContainer(ctx, cli, opts.ContainerID, data, managedMCPPath); err != nil {
					fmt.Fprintf(opts.LogW, "[claude-bunker] WARNING: copying managed-mcp.json: %v\n", err)
				} else {
					fmt.Fprintf(opts.LogW, "[claude-bunker] Copied managed-mcp.json\n")
				}
			}
		}
	}

	// Batch-verify all collected stdio MCP server runtimes in one exec
	batchCheckRuntimes(ctx, cli, opts.ContainerID, runtimeChecks, opts.LogW)

	// Fix ownership on all plugin-related files (settings.json, plugins dir, ~/.claude.json)
	if err := container.ChownRecursive(ctx, cli, opts.ContainerID, container.ContainerHome+"/.claude"); err != nil {
		fmt.Fprintf(opts.LogW, "[claude-bunker] WARNING: chown plugin files: %v\n", err)
	}
	if err := container.ChownRecursive(ctx, cli, opts.ContainerID, container.ContainerHome+"/.claude.json"); err != nil {
		// ~/.claude.json may not exist — ignore errors silently
	}

	return nil
}

// rewritePluginPaths rewrites host paths in installed_plugins.json and
// known_marketplaces.json to container paths. On Windows hosts, paths like
// "C:\\Users\\devon\\.claude\\plugins\\cache\\..." become
// "/home/claude-bunker/.claude/plugins/cache/...".
// Without this, Claude Code creates mangled directory names in the workspace.
func rewritePluginPaths(ctx context.Context, cli *client.Client, containerID, hostPluginsDir, containerPluginsDir string, logW io.Writer) {
	rewriteInstalledPlugins(ctx, cli, containerID, hostPluginsDir, containerPluginsDir, logW)
	rewriteKnownMarketplaces(ctx, cli, containerID, hostPluginsDir, containerPluginsDir, logW)
}

// rewriteInstalledPlugins rewrites installPath and projectPath in installed_plugins.json.
func rewriteInstalledPlugins(ctx context.Context, cli *client.Client, containerID, hostPluginsDir, containerPluginsDir string, logW io.Writer) {
	hostNorm := filepath.ToSlash(hostPluginsDir)
	data, err := os.ReadFile(filepath.Join(hostPluginsDir, "installed_plugins.json"))
	if err != nil {
		return
	}

	var installed struct {
		Version int                          `json:"version"`
		Plugins map[string][]json.RawMessage `json:"plugins"`
	}
	if err := json.Unmarshal(data, &installed); err != nil {
		return
	}

	modified := false
	for pluginName, entries := range installed.Plugins {
		for i, raw := range entries {
			var entry map[string]interface{}
			if err := json.Unmarshal(raw, &entry); err != nil {
				continue
			}
			entryModified := rewritePathField(entry, "installPath", hostNorm, containerPluginsDir)
			// Rewrite projectPath to container workspace
			if pp, ok := entry["projectPath"].(string); ok && pp != "" {
				entry["projectPath"] = container.ContainerWorkspace
				entryModified = true
			}
			if entryModified {
				modified = true
				if rewritten, err := json.Marshal(entry); err == nil {
					installed.Plugins[pluginName][i] = rewritten
				}
			}
		}
	}

	if !modified {
		return
	}

	out, err := json.MarshalIndent(installed, "", "  ")
	if err != nil {
		return
	}
	out = append(out, '\n')

	if err := container.CopyContentToContainer(ctx, cli, containerID, out, containerPluginsDir+"/installed_plugins.json"); err != nil {
		fmt.Fprintf(logW, "[claude-bunker] WARNING: rewriting installed_plugins.json: %v\n", err)
	} else {
		fmt.Fprintf(logW, "[claude-bunker] Rewrote installed_plugins.json paths for container\n")
	}
}

// rewriteKnownMarketplaces rewrites installLocation in known_marketplaces.json.
func rewriteKnownMarketplaces(ctx context.Context, cli *client.Client, containerID, hostPluginsDir, containerPluginsDir string, logW io.Writer) {
	hostNorm := filepath.ToSlash(hostPluginsDir)
	data, err := os.ReadFile(filepath.Join(hostPluginsDir, "known_marketplaces.json"))
	if err != nil {
		return
	}

	var marketplaces map[string]map[string]interface{}
	if err := json.Unmarshal(data, &marketplaces); err != nil {
		return
	}

	modified := false
	for _, mp := range marketplaces {
		if rewritePathField(mp, "installLocation", hostNorm, containerPluginsDir) {
			modified = true
		}
	}

	if !modified {
		return
	}

	out, err := json.MarshalIndent(marketplaces, "", "  ")
	if err != nil {
		return
	}
	out = append(out, '\n')

	if err := container.CopyContentToContainer(ctx, cli, containerID, out, containerPluginsDir+"/known_marketplaces.json"); err != nil {
		fmt.Fprintf(logW, "[claude-bunker] WARNING: rewriting known_marketplaces.json: %v\n", err)
	} else {
		fmt.Fprintf(logW, "[claude-bunker] Rewrote known_marketplaces.json paths for container\n")
	}
}

// rewritePathField rewrites a string field in a map from a host path prefix
// to a container path prefix. Returns true if the field was modified.
func rewritePathField(m map[string]interface{}, key, hostPrefix, containerPrefix string) bool {
	val, ok := m[key].(string)
	if !ok || val == "" {
		return false
	}
	normalized := filepath.ToSlash(val)
	if strings.HasPrefix(normalized, hostPrefix) {
		m[key] = containerPrefix + normalized[len(hostPrefix):]
		return true
	}
	return false
}

// mcpServerEntry represents a single MCP server config (works for both
// the { "mcpServers": { ... } } format and the flat plugin .mcp.json format).
type mcpServerEntry struct {
	URL     string `json:"url"`
	Command string `json:"command"`
}

// extractMCPDomains reads an MCP config file and extracts HTTP server domains.
// Handles two formats:
//   - settings.json / .claude.json: { "mcpServers": { "name": { "url": "..." } } }
//   - plugin .mcp.json:            { "name": { "url": "..." } }
func extractMCPDomains(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return extractMCPDomainsFromData(data)
}

// extractMCPDomainsFromData extracts HTTP server domains from MCP config JSON.
func extractMCPDomainsFromData(data []byte) []string {
	// Try nested format first: { "mcpServers": { ... } }
	var nested struct {
		MCPServers map[string]mcpServerEntry `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &nested); err == nil && len(nested.MCPServers) > 0 {
		return domainsFromServers(nested.MCPServers)
	}

	// Try flat format: { "name": { "url": "..." } }
	var flat map[string]mcpServerEntry
	if err := json.Unmarshal(data, &flat); err == nil {
		return domainsFromServers(flat)
	}

	return nil
}

// domainsFromServers extracts domains from a map of MCP server entries.
func domainsFromServers(servers map[string]mcpServerEntry) []string {
	var domains []string
	for _, server := range servers {
		if server.URL == "" {
			continue // stdio server, no URL
		}
		if host := extractHost(server.URL); host != "" {
			domains = append(domains, host)
		}
	}
	return domains
}

// pluginCacheEntry holds the parsed data from a single plugin cache .mcp.json file.
type pluginCacheEntry struct {
	pluginName string
	data       []byte
}

// pluginCacheMu protects pluginCacheResults from concurrent access.
var pluginCacheMu sync.Mutex

// pluginCacheResults caches the output of walkPluginCacheMCP so that multiple
// callers (ExtractPluginDomains and SeedPlugins) avoid redundant directory walks
// within the same process.
var pluginCacheResults struct {
	dir     string
	entries []pluginCacheEntry
}

// walkPluginCacheMCP walks the plugin cache directory structure and returns
// all .mcp.json entries. The structure is: cache/<marketplace>/<plugin>/<version>/.mcp.json
// Results are cached per directory path to avoid redundant walks.
func walkPluginCacheMCP(cacheDir string) []pluginCacheEntry {
	pluginCacheMu.Lock()
	defer pluginCacheMu.Unlock()

	if pluginCacheResults.dir == cacheDir && pluginCacheResults.entries != nil {
		return pluginCacheResults.entries
	}
	entries := walkPluginCacheMCPUncached(cacheDir)
	pluginCacheResults.dir = cacheDir
	pluginCacheResults.entries = entries
	return entries
}

// walkPluginCacheMCPUncached performs the actual directory walk.
func walkPluginCacheMCPUncached(cacheDir string) []pluginCacheEntry {
	marketplaces, err := os.ReadDir(cacheDir)
	if err != nil {
		return nil
	}
	var entries []pluginCacheEntry
	for _, mp := range marketplaces {
		if !mp.IsDir() {
			continue
		}
		plugins, err := os.ReadDir(filepath.Join(cacheDir, mp.Name()))
		if err != nil {
			continue
		}
		for _, plugin := range plugins {
			if !plugin.IsDir() {
				continue
			}
			versions, err := os.ReadDir(filepath.Join(cacheDir, mp.Name(), plugin.Name()))
			if err != nil {
				continue
			}
			for _, ver := range versions {
				if !ver.IsDir() {
					continue
				}
				mcpPath := filepath.Join(cacheDir, mp.Name(), plugin.Name(), ver.Name(), ".mcp.json")
				data, err := os.ReadFile(mcpPath)
				if err != nil {
					continue
				}
				entries = append(entries, pluginCacheEntry{pluginName: plugin.Name(), data: data})
			}
		}
	}
	return entries
}

// extractPluginCacheDomains scans installed plugin cache directories for
// .mcp.json files containing HTTP MCP servers and extracts their domains.
func extractPluginCacheDomains(cacheDir string) []string {
	var domains []string
	for _, entry := range walkPluginCacheMCP(cacheDir) {
		domains = append(domains, extractMCPDomainsFromData(entry.data)...)
	}
	return domains
}

// pluginSettingsKeys are the keys from settings.json that relate to plugins/MCP.
var pluginSettingsKeys = []string{"mcpServers", "enabledPlugins", "extraKnownMarketplaces"}

// copyFilteredSettings extracts plugin-related keys from ~/.claude/settings.json
// and writes them to the container's user-scope settings.json.
// This is the primary config file for enabledPlugins, mcpServers, and marketplaces.
func copyFilteredSettings(ctx context.Context, cli *client.Client, containerID string, data []byte, containerPath string, logW io.Writer) error {
	return copyFilteredJSON(ctx, cli, containerID, data, containerPath, pluginSettingsKeys, "settings.json", logW)
}

// copyFilteredClaudeJSON extracts only mcpServers from ~/.claude.json
// (some setups configure MCP servers here).
func copyFilteredClaudeJSON(ctx context.Context, cli *client.Client, containerID string, data []byte, containerPath string, logW io.Writer) error {
	return copyFilteredJSON(ctx, cli, containerID, data, containerPath, []string{"mcpServers"}, "~/.claude.json", logW)
}

// copyFilteredJSON extracts specified keys from JSON data and writes to a container path.
func copyFilteredJSON(ctx context.Context, cli *client.Client, containerID string, data []byte, containerPath string, keys []string, label string, logW io.Writer) error {
	var full map[string]json.RawMessage
	if err := json.Unmarshal(data, &full); err != nil {
		return fmt.Errorf("parsing %s: %w", label, err)
	}

	filtered := make(map[string]json.RawMessage)
	for _, key := range keys {
		if v, ok := full[key]; ok {
			filtered[key] = v
		}
	}

	if len(filtered) == 0 {
		return nil
	}

	out, err := json.MarshalIndent(filtered, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')

	if err := container.CopyContentToContainer(ctx, cli, containerID, out, containerPath); err != nil {
		return err
	}

	var keyNames []string
	for k := range filtered {
		keyNames = append(keyNames, k)
	}
	fmt.Fprintf(logW, "[claude-bunker] Copied %s (%s)\n", label, strings.Join(keyNames, ", "))
	return nil
}

// runtimeCheck tracks an MCP server command that needs a runtime availability check.
type runtimeCheck struct {
	cmd        string // binary name (e.g. "node", "uvx")
	serverName string // MCP server name for the warning message
	source     string // optional context (e.g. " (plugin \"github\")")
}

// collectStdioRuntimes collects stdio MCP server commands from settings.json data.
func collectStdioRuntimes(data []byte, seen map[string]bool) []runtimeCheck {
	var nested struct {
		MCPServers map[string]mcpServerEntry `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &nested); err != nil {
		return nil
	}
	return collectFromServers(nested.MCPServers, "", seen)
}

// collectPluginCacheRuntimes collects stdio MCP server commands from plugin cache .mcp.json files.
func collectPluginCacheRuntimes(cacheDir string, seen map[string]bool) []runtimeCheck {
	var checks []runtimeCheck
	for _, entry := range walkPluginCacheMCP(cacheDir) {
		var servers map[string]mcpServerEntry
		if err := json.Unmarshal(entry.data, &servers); err != nil {
			continue
		}
		checks = append(checks, collectFromServers(servers, fmt.Sprintf(" (plugin %q)", entry.pluginName), seen)...)
	}
	return checks
}

// collectFromServers extracts runtime checks from a server map, deduplicating by command.
func collectFromServers(servers map[string]mcpServerEntry, source string, seen map[string]bool) []runtimeCheck {
	var checks []runtimeCheck
	for name, server := range servers {
		if server.URL != "" || server.Command == "" {
			continue
		}
		if seen[server.Command] {
			continue
		}
		seen[server.Command] = true
		checks = append(checks, runtimeCheck{cmd: server.Command, serverName: name, source: source})
	}
	return checks
}

// batchCheckRuntimes verifies all collected runtime commands in a single container exec.
func batchCheckRuntimes(ctx context.Context, cli *client.Client, containerID string, checks []runtimeCheck, logW io.Writer) {
	if len(checks) == 0 {
		return
	}

	// Collect unique commands, sanitizing to prevent shell metacharacter injection.
	// MCP server command names come from external config files and could contain
	// arbitrary strings — only include commands with safe characters.
	var cmds []string
	for _, c := range checks {
		if safeCmdPattern.MatchString(c.cmd) {
			cmds = append(cmds, c.cmd)
		}
	}
	if len(cmds) == 0 {
		return
	}

	// Single exec: check all commands at once, output missing ones
	script := "for cmd in " + strings.Join(cmds, " ") + "; do which \"$cmd\" >/dev/null 2>&1 || echo \"$cmd\"; done"
	output, err := container.ExecNonInteractive(ctx, cli, containerID, container.ContainerUser,
		[]string{"sh", "-c", script})
	if err != nil {
		return // container exec failed, skip warnings
	}

	missing := make(map[string]bool)
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			missing[line] = true
		}
	}

	for _, c := range checks {
		if missing[c.cmd] {
			fmt.Fprintf(logW, "[claude-bunker] WARNING: MCP server %q%s requires %q which is not found in the container. "+
				"Add it via \"apt\" or \"features\" in config.json.\n", c.serverName, c.source, c.cmd)
		}
	}
}

// hostManagedMCPPath returns the platform-specific path to managed-mcp.json.
func hostManagedMCPPath() string {
	switch runtime.GOOS {
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		return filepath.Join(home, "Library", "Application Support", "claude-code", "managed-mcp.json")
	case "linux":
		if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
			return filepath.Join(dir, "claude-code", "managed-mcp.json")
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		return filepath.Join(home, ".config", "claude-code", "managed-mcp.json")
	case "windows":
		if dir := os.Getenv("APPDATA"); dir != "" {
			return filepath.Join(dir, "claude-code", "managed-mcp.json")
		}
		return ""
	default:
		return ""
	}
}

