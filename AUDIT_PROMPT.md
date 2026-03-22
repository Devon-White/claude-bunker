# claude-bunker Deep Audit — Agent Prompt

You are a senior systems engineer performing a deep audit of the claude-bunker codebase. claude-bunker is a Go CLI that runs Claude Code inside a hardened Docker container with defense-in-depth security (iptables firewall, bubblewrap sandbox, managed settings, bind-mount isolation).

Produce a structured audit report covering the sections below. For each section, read the relevant source files thoroughly, trace code paths end-to-end, and provide specific findings with file:line references. Do not speculate — base every finding on code you have read.

---

## 1. ARCHITECTURE REVIEW

Evaluate whether the current package structure and responsibilities are well-designed or need refactoring.

**Files to examine:**
- `cmd/run.go` — main orchestration flow (extractBunkerFlags → loadConfig → build → create → exec → cleanup)
- `cmd/root.go` — Cobra setup, DisableFlagParsing strategy
- `internal/config/project.go` — ProjectConfig struct, loading, validation
- `internal/container/build.go` — image build pipeline (concurrent pull + feature resolution)
- `internal/container/lifecycle.go` — container create/start/stop/remove, mount strategy, seccomp profile
- `internal/container/generate.go` — Dockerfile generation from config
- `internal/sandbox/seed.go` — settings injection, managed-settings.json construction
- `internal/platform/` — all files (TTY, resize, VT support)

**Questions to answer:**
- Is the separation between `config`, `container`, `sandbox`, and `platform` clean? Are there responsibilities that leak across package boundaries?
- Does `cmd/run.go` do too much orchestration? Should the build→create→exec→cleanup pipeline be extracted into its own package or struct?
- Is the two-config system (`.claude/.claude-bunker/config.json` for infra vs `.claude/settings.json` for Claude behavior) well-defined, or do concerns bleed between them?
- Are there circular dependencies, god objects, or functions that are too long (>100 lines)?
- Is the error handling strategy (die/warn/info/verbose in cmd/ui.go) consistent across all packages, or do some packages handle errors differently?
- Should the Docker client be injected as a dependency rather than created inline?

---

## 2. SECURITY AUDIT

Evaluate the defense-in-depth model for completeness and correctness.

**Files to examine:**
- `internal/container/scripts/init-firewall.sh` — iptables setup, fail-closed design, DNS resolver lockdown, self-test
- `internal/container/scripts/refresh-firewall.sh` — atomic ipset swap, daemon loop, log rotation
- `internal/container/scripts/firewall-common.sh` — DNS resolution, /24 subnet strategy, IPv4 validation
- `internal/container/lifecycle.go` — seccomp profile (lines 28-78), capability grants (NET_ADMIN, NET_RAW), apparmor=unconfined
- `internal/container/lifecycle.go` — secrets injection via tmpfs, auth wrapper pattern
- `internal/sandbox/seed.go` — managed-settings.json construction, chmod 444, domain merging
- `internal/sandbox/plugins.go` — plugin path rewriting, runtime sanitization (safeCmdPattern)
- `internal/container/copy.go` — tar building, symlink rejection, path traversal checks
- `internal/container/features.go` — OCI feature extraction, maxExtractFileSize, tar bomb defense
- `internal/config/project.go` — domain validation, credential literal detection

**Questions to answer:**
- Can a prompt injection attack escape the sandbox? Trace the attack surface: What can Claude Code execute inside the container, and what prevents exfiltration?
- Is the firewall truly fail-closed? What happens if init-firewall.sh partially executes (e.g., crashes after setting DROP policies but before adding allow rules)?
- Is there a TOCTOU window between DNS resolution at startup and the first firewall refresh? How long is it and what's the real-world risk?
- Can the managed-settings.json be tampered with? Is `/etc/claude-code/` directory itself protected, or just the file?
- Are secrets truly invisible? Check: `docker inspect`, `/proc/*/environ`, `/proc/*/cmdline`, container logs, shell history. Does the auth wrapper leave traces?
- Is the seccomp profile sufficient? Are there syscalls that should be blocked but aren't (e.g., ptrace, process_vm_readv)?
- Can a malicious devcontainer feature escape during extraction? Check symlink handling, path traversal, file size limits, and whether the tar extraction runs as root.
- Does the `/24 subnet` strategy create an overly broad allowlist? Could a CDN IP neighbor be exploited?
- What happens if IPv6 is enabled in Docker? Is the IPv6 blocking robust?
- Are apt package names validated against shell injection? Check the regex in generate.go.
- Can postStartCommand (from project config) be used to weaken security? What's the trust model?

---

## 3. DEVELOPER EXPERIENCE

Evaluate how easy it is to install, configure, use, and troubleshoot claude-bunker.

**Files to examine:**
- `cmd/init.go` — interactive wizard (huh forms, language presets, version fetching)
- `cmd/status.go` — container state display
- `cmd/prune.go` — volume/image cleanup
- `cmd/logs.go` — log streaming
- `cmd/shell.go` — interactive shell access
- `cmd/ui.go` — output formatting (die, warn, info, verbose, success)
- `cmd/run.go` — flag extraction, error messages during build/start
- `cmd/completion.go` — shell completion
- `internal/config/project.go` — config validation and error messages
- `internal/container/presets.go` — language preset definitions, version detection

**Questions to answer:**
- First-run experience: What happens when a user runs `claude-bunker` with no config? Is the error message helpful? Does it guide them to `claude-bunker init`?
- How long does the first build take? Is there adequate progress indication? Are there spinner/progress bar opportunities?
- What happens when a build fails? Are Docker build errors surfaced clearly, or buried in verbose output?
- Is the init wizard discoverable and complete? Can it handle all common project types? What's missing?
- Can users validate their config without running a full build? Is there a `config validate` or `doctor` command?
- How are version upgrades handled? Does the user know when their image is stale?
- Is the `--verbose` / `--quiet` system useful? Does `--verbose` show enough for debugging, and does `--quiet` suppress enough for CI?
- Are error messages actionable? Do they tell the user what to do, not just what went wrong?
- Is there a `--dry-run` mode for destructive operations (prune, rebuild)?
- How does the tool behave in CI environments (no TTY, no interactive input)?

---

## 4. PLUGIN & MCP SERVER HANDLING

Evaluate whether Claude Code's plugin and MCP server ecosystem is correctly supported inside the sandbox.

**Files to examine:**
- `internal/sandbox/plugins.go` — full file (606 lines): ExtractPluginDomains, SeedPlugins, MCP config reading, path rewriting, runtime checks
- `internal/sandbox/seed.go` — enableAllProjectMcpServers setting, workspace .claude/ protection
- `internal/container/lifecycle.go` — mount strategy for .claude/ directory, tmpfs overlay
- `internal/container/domains.go` — BuiltinDomains, SandboxExtraDomains
- `internal/config/project.go` — Plugins field, PluginLevelAtLeast

**Questions to answer:**
- **Stdio MCP servers**: If a project uses a stdio MCP server (e.g., `npx @modelcontextprotocol/server-filesystem`), does the runtime exist in the container? How does `batchCheckRuntimes()` handle missing runtimes — warning only, or does it block?
- **HTTP/SSE MCP servers**: Are domains correctly extracted and added to BOTH the firewall allowlist AND the sandbox allowlist? Trace the full path from `.mcp.json` → `ExtractPluginDomains()` → firewall domains file → init-firewall.sh → managed-settings.json.
- **Plugin path rewriting**: Does the Windows→Linux path translation work correctly for all edge cases? What about WSL paths, Git Bash paths, drive letters, UNC paths?
- **Plugin level hierarchy**: Is `"project"` vs `"user"` vs `"all"` correctly implemented? What does each level expose, and is the boundary enforced?
- **MCP server config formats**: Does the code handle both `{ "mcpServers": { ... } }` (settings.json format) and flat `{ "name": { ... } }` (.mcp.json format) correctly?
- **Managed MCP**: Does the `"all"` plugin level correctly locate and copy `managed-mcp.json` on all platforms (macOS, Linux, Windows)?
- **enableAllProjectMcpServers**: Is this setting always injected when plugins are enabled? Could it be accidentally omitted?
- **Plugin isolation**: Can a malicious MCP server in a plugin escape the sandbox? What prevents a compromised MCP server from exfiltrating data?
- **Silent failures**: Are there MCP-related errors that are swallowed silently? (Check: extractMCPDomains returning nil on parse error, plugin cache walk skipping bad JSON)
- **New MCP features**: Does the code handle `streamableHttp` transport type? Does it handle MCP server auth (OAuth tokens for remote servers)?

---

## 5. DEV ENVIRONMENT HANDLING

Evaluate cross-platform support and environment-specific behavior.

**Files to examine:**
- `internal/platform/tty.go`, `tty_unix.go`, `tty_windows.go` — raw mode, terminal handling
- `internal/platform/resize.go`, `resize_unix.go`, `resize_windows.go` — SIGWINCH vs polling
- `internal/platform/vt_windows.go` — VT100 emulation
- `internal/config/naming.go` — path normalization, case sensitivity
- `internal/sandbox/plugins.go` — managed MCP path resolution (macOS/Linux/Windows), plugin path rewriting
- `internal/sandbox/proxy.go` — proxy environment detection, cert validation
- `internal/container/lifecycle.go` — macOS consistency hints, Docker socket detection
- `cmd/run.go` — signal handling (SIGINT, SIGTERM, SIGHUP)

**Questions to answer:**
- **Windows (Git Bash/MSYS2)**: Does terminal resize work correctly? Is VT100 mode reliably enabled? Are there known issues with path translation?
- **Windows (WSL2)**: Is Docker Desktop correctly detected vs native Docker? Are workspace paths translated correctly between Windows and WSL2 filesystem?
- **macOS**: Is the `consistency=delegated` mount hint still relevant for newer Docker Desktop versions? Are there Apple Silicon-specific issues?
- **Linux**: Any assumptions about systemd, cgroups v2, rootless Docker, or Podman compatibility?
- **CI/CD (headless)**: Does the tool work without a TTY? Are all interactive prompts guarded by TTY detection? Does `--quiet` mode suppress all output?
- **Proxy environments**: Is proxy detection complete? Does it handle HTTPS_PROXY with authentication? Are cert files correctly propagated into the container?
- **Docker variants**: Does the code work with Docker Desktop, Docker Engine, Colima, Rancher Desktop, or Podman? Are there Docker API version dependencies?
- **Signal handling**: On Windows, SIGWINCH doesn't exist. Is the polling fallback reliable? What's the CPU cost of 500ms polling?

---

## 6. SCALABILITY & EFFICIENCY

Evaluate resource usage, performance bottlenecks, and how the tool handles growth.

**Files to examine:**
- `internal/container/build.go` — concurrent pull, in-memory tar construction, Docker cache strategy
- `internal/container/features.go` — OCI image download, temp directory usage, no feature caching
- `internal/config/fingerprint.go` — SHA-256 hashing, fingerprint comparison, cache file I/O
- `internal/container/lifecycle.go` — HasOtherActiveSessions (iterates all exec IDs), volume creation
- `internal/container/copy.go` — in-memory tar building, base64 encoding for large payloads
- `internal/sandbox/plugins.go` — plugin cache walking, batch runtime checks

**Questions to answer:**
- **Build performance**: How long does a cold build take vs a warm build? Is the Docker layer cache used effectively? Are there unnecessary cache invalidations?
- **Feature caching**: Features are re-downloaded on every image build. Should they be cached locally? What's the bandwidth and time cost?
- **Memory usage**: The build context tar is constructed entirely in memory (`bytes.Buffer`). What happens with large features or many apt packages? Is there a memory ceiling?
- **Fingerprint efficiency**: Is SHA-256 hashing fast enough? Are there unnecessary re-computations? Is the fingerprint cache file I/O a bottleneck?
- **Container startup time**: What's the breakdown of startup time? (firewall init, DNS resolution, secret injection, plugin seeding, postStartCommand)
- **Multi-session overhead**: Does `HasOtherActiveSessions()` scale with many exec sessions? Could stale exec IDs accumulate?
- **Plugin seeding**: Is the plugin directory walk efficient for large plugin caches? Are there filesystem I/O bottlenecks?
- **Domain resolution**: With many allowed domains, does DNS resolution at startup become slow? Is it parallelized?
- **Scaling limits**: What happens with 50+ features, 100+ domains, or very large workspaces? Are there hard limits?

---

## 7. RELIABILITY & ERROR HANDLING

Evaluate failure modes, recovery, and correctness under adverse conditions.

**Files to examine:**
- `cmd/run.go` — signal handling, cleanup paths, activeRunner pattern
- `cmd/ui.go` — die() cleanup behavior, mutex on activeRunner
- `internal/container/lifecycle.go` — StopAndRemove, HasOtherActiveSessions, cleanup on context cancellation
- `internal/container/build.go` — goroutine management, timeout handling, fallback paths
- `internal/container/exec.go` — stream error handling, exit code propagation
- `internal/config/fingerprint.go` — malformed cache file handling, I/O errors
- `internal/sandbox/plugins.go` — silent error patterns (extractMCPDomains, rewritePluginPaths, plugin cache walk)

**Questions to answer:**
- **Crash recovery**: If claude-bunker is killed (SIGKILL, power loss), what state is left behind? Orphaned containers? Volumes? Stale fingerprint caches?
- **Partial builds**: If a Docker build fails mid-way, is the state cleaned up? Can the user retry without manual intervention?
- **Network failures**: What happens if Docker Hub, GHCR, or endoflife.date is unreachable? Are fallbacks adequate?
- **Container death**: If the container crashes during exec, is the user experience clean? Is the exit code propagated?
- **Silent failures**: Catalog all places where errors are swallowed (returned as nil, logged as warn but continued). Are any of these actually critical?
- **Concurrent access**: What happens if two `claude-bunker` processes run simultaneously for the same project? Is there a lock file? Race conditions on fingerprint cache?
- **Docker API failures**: Are Docker API errors retried? Are they surfaced clearly to the user?
- **Cleanup ordering**: In die(), cleanup() happens before os.Exit(1). Is the cleanup reliable? Can it hang? Is there a timeout?

---

## 8. TEST COVERAGE ASSESSMENT

Evaluate what is tested, what is not, and where the gaps create risk.

**Files to examine:** All `*_test.go` files in the repository.

**Questions to answer:**
- What percentage of packages have test coverage? What are the critical untested paths?
- Are there integration tests that verify the full pipeline (build → create → exec → cleanup)?
- Is the firewall tested? (init-firewall.sh, refresh-firewall.sh behavior under various conditions)
- Are platform-specific code paths tested on their target platforms?
- Is the plugin/MCP seeding tested with realistic multi-format configs?
- Are error paths tested (network failures, malformed configs, missing Docker)?
- What tests would provide the highest risk-reduction per effort?

---

## 9. VERDICT & RECOMMENDATIONS

Based on your findings across all sections above, provide:

1. **Architecture Verdict**: Is the current structure fundamentally sound, or does it need significant refactoring? If refactoring is needed, what specifically would you change and why?

2. **Top 5 Critical Issues**: The most important problems found, ranked by risk (security > reliability > correctness > DX > efficiency).

3. **Top 5 Quick Wins**: Low-effort improvements that would have high impact.

4. **Refactoring Roadmap**: If architectural changes are needed, propose a phased approach that doesn't break existing functionality.

5. **Missing Capabilities**: Features or integrations that should exist but don't (e.g., config validation command, healthcheck endpoint, structured logging).

Format your entire report with clear headers, file:line references, severity ratings (CRITICAL / HIGH / MEDIUM / LOW / INFO), and concrete code examples where relevant.
