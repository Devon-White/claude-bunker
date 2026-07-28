# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository. Codex will review your code when you're done.

## What This Project Is

claude-bunker is a Go CLI that runs Claude Code inside a hardened Docker container with defense-in-depth security: iptables firewall, an SNI-aware egress proxy, bubblewrap sandbox, managed settings, and bind-mounted workspace isolation. It prevents prompt injection attacks from exfiltrating credentials.

## Build & Test Commands

```bash
go build -o claude-bunker .           # Build binary
go test ./...                         # Run all tests
go test -v ./cmd -run TestName        # Run a single test
go run cmd/genbuild/main.go           # Generate Docker build context to .bunker-build/
```

Version is injected via ldflags: `-X github.com/Devon-White/claude-bunker/cmd.Version=...`

Releases are built by goreleaser (`.goreleaser.yml`) for linux/darwin/windows on amd64/arm64.

## Architecture

### Entry Flow

`main.go` → `cmd.Execute()` → root command (`cmd/root.go`; run logic in `cmd/run.go`):
1. `extractBunkerFlags()` manually parses claude-bunker flags (root command has `DisableFlagParsing: true` so unknown flags pass through to `claude`)
2. Load project config from `.devcontainer/devcontainer.json` via `internal/devcontainer.LoadProjectConfig`
3. Check fingerprints — rebuild image or recreate container only when config changes
4. Build Docker image if needed (embedded scripts + generated Dockerfile, all in-memory tar)
5. Create/reuse container, configure firewall, seed sandbox settings
6. `ExecInteractive()` — attach TTY to running container
7. Cleanup on exit (stop/remove container unless `--keep`)

### Package Layout

| Package | Role |
|---------|------|
| `cmd/` | Cobra CLI commands. Root disables flag parsing; subcommands re-enable it. |
| `internal/config/` | Config loading (`project.go`), SHA-256 fingerprinting (`fingerprint.go`), deterministic container naming (`naming.go`), env var expansion (`expand.go`), OCI feature-name validation (`features.go`) |
| `internal/devcontainer/` | Parse/generate `.devcontainer/devcontainer.json` (JSONC + `${localEnv}`), map to `ProjectConfig`, strip bunker-managed features (`devcontainer.go`, `generate.go`, `jsonc.go`, `load.go`, `merge.go`) |
| `internal/container/` | Docker API wrapper: image build (`build.go`), Dockerfile generation (`generate.go`), container lifecycle (`lifecycle.go`), interactive exec (`exec.go`), domain/firewall management (`domains.go`), credential masking (`masking.go`), plus `lockfile.go` (devcontainer-lock.json), `baseimage.go`, `volumes.go`, `features.go`, `presets.go`, `constants.go`, `copy.go`, `client.go`, `embed.go` |
| `internal/container/scripts/` | Embedded shell scripts (`//go:embed`): `init-firewall.sh`, `refresh-firewall.sh`, `firewall-common.sh`, `base.dockerfile.tmpl`, `tmux.conf` |
| `internal/container/egressproxy/` | Stdlib-only SNI-aware egress proxy compiled into the image via multi-stage build (`main.go`, `config.go`, `allowlist.go`, `sniread.go`, `splice.go`, `terminate.go`, `mask.go`, `ca.go`); sources embedded and exposed via `container.EgressProxySources()` |
| `internal/buildlock/` | Cross-process build lock (unix/windows variants) |
| `internal/sessions/` | Session/subagent tree via `claude agents --json` (store, watcher, manager) |
| `internal/sandbox/` | Sandbox settings seeding (`seed.go`), plugin loading (`plugins.go`), proxy config (`proxy.go`) |
| `internal/log/` | Logging helpers |
| `internal/platform/` | Platform-specific TTY, terminal resize (Unix signals vs Windows polling), Windows VT support |

### Config

- **`.devcontainer/devcontainer.json`** — the single project config. Standard devcontainer keys at top level (features, containerEnv, onCreateCommand, postStartCommand, capAdd, remoteUser); bunker extras under `customizations["claude-bunker"]` (exclude, allowDomains, plugins, ghToken, seedHistory, workspace). Parsed by `internal/devcontainer`. OS packages are NOT a bunker extra — they're expressed via the standard `ghcr.io/rocker-org/devcontainer-features/apt-packages:1` feature in the top-level `features` map, a normal user feature that bunker resolves and VS Code/Codespaces honor the same way. A deprecation warning fires if a config still has the old `customizations["claude-bunker"].apt` key.
- **`.devcontainer/devcontainer-lock.json`** — pins feature digests (reproducible builds); digests fold into the image fingerprint.
- Enforcement of Claude Code behavior is a runtime read-only `/etc/claude-code/managed-settings.json` (written each start); host `settings.json`/`settings.local.json` are NOT injected.

### Six Security Layers

1. **Docker container** — process isolation
2. **iptables firewall** — default-deny egress, IPv6 blocked, domain allowlist resolved to IPs at startup, self-test verifies blocking
3. **SNI-aware egress proxy** — a stdlib-only Go proxy (`internal/container/egressproxy`) baked via multi-stage build; `init-firewall.sh` transparently REDIRECTs agent TCP/443 to it. It allowlists by TLS SNI (closing CDN-IP domain-fronting the ipset /24 tier can't see) and, in the bunker-CLI path, terminates the 1–3 credential hosts to swap a per-session sentinel for the real token (`InjectAuthSecrets` gives the agent only sentinels; real secrets live in `bunker-proxy`-owned files). Portable path runs the same binary in Tier-1 (splice) mode.
4. **Bubblewrap sandbox** — Claude Code's built-in sandbox with domain-level filtering
5. **managed-settings.json** — enforced Claude Code settings (injected to `/etc/claude-code/`)
6. **Bind-mount workspace** — only project directory is visible, exclude paths overlaid with tmpfs

### Secrets

Auth tokens are injected via tmpfs at `/run/secrets/`, never as environment variables. An auth wrapper script (`~/.claude-auth-wrapper.sh`) exports them before exec. When credential masking is active (`container.ShouldMask` — secrets present and no upstream proxy), the agent instead holds only per-session sentinels; the real secrets live in `bunker-proxy`-owned config under the egress proxy's control, which swaps sentinel→real on the 1–3 credential hosts it terminates.

### Fingerprinting & Caching

The public API is `CompareFingerprints(BuildInput, containerName) FingerprintResult` (unexported `imageFingerprint`/`containerFingerprint` do the hashing). The image fingerprint covers version + Dockerfile + scripts + **egress-proxy Go source (`container.EgressProxySources()`, compiled into the image by the multi-stage build)** + features + env + onCreateCommand **plus resolved feature digests from `devcontainer-lock.json`** (this is also how OS packages now factor in, since they're expressed as a feature); the container fingerprint covers domains + workspace + excludes + postStartCommand + plugins + seedHistory + **whether credential masking is active** (`BuildInput.MaskActive`, set by the run flow from `container.ShouldMask(auth, hasUpstreamProxy)` — masking changes runtime proxy setup, so toggling it forces a recreate, but raw auth/secret values themselves are never hashed). Reproducible mod times (`2025-01-01T00:00:00Z`) ensure Docker layer cache hits.

### Portability (VS Code / Codespaces) via `build.dockerfile`

`claude-bunker init` targets the VS Code/Codespaces path with a committed `.devcontainer/` bundle instead of OCI Features — there is no ghcr publishing and no Feature packages anywhere in this model. `writeDevContainer` (`cmd/init.go`) writes, alongside `devcontainer.json`:

- **`Dockerfile`** — `container.GenerateBaseDockerfile()` (the same base-image template bunker's own build uses, `internal/container/scripts/base.dockerfile.tmpl`) plus a small generated suffix that `COPY`s `allowed-domains.txt` in and locks it down root-owned/read-only (`chmod 0444`) before switching back to the non-root user. This one file carries all of bunker's hardening: firewall tooling, bubblewrap, the `claude-bunker` user, Claude Code install, and a scoped `sudo` grant (arg-pinned to `container.FirewallScriptPath`/`RefreshFirewallScriptPath` against `container.AllowedDomainsPath` — see `TestGenerateBaseDockerfile_ScopedSudoersGrant` in `internal/container/baseimage_test.go`).
- **`init-firewall.sh`, `refresh-firewall.sh`, `firewall-common.sh`, `tmux.conf`** — `container.BuildContextScripts()`, the same embedded scripts bunker's native image build COPYs in.
- **`allowed-domains.txt`** — `container.BuiltinDomains()` plus the project's `allowDomains`, written root-owned/read-only into the image at `container.AllowedDomainsPath` (`/etc/claude-bunker/allowed-domains.txt`) so a sandboxed agent can't widen its own firewall allowlist.
- **`seccomp.json`** — `container.SeccompProfileJSON()`, the same profile bunker's native container creation applies.

`devcontainer.Generate` (`internal/devcontainer/generate.go`) wires these into `devcontainer.json`: `"build": {"dockerfile": "Dockerfile"}` (no `image` key — Generate never emits one), `runArgs` applying the seccomp profile (`--security-opt seccomp=${localWorkspaceFolder}/.devcontainer/seccomp.json`), forced `capAdd: [NET_ADMIN, NET_RAW]` and `remoteUser: claude-bunker`, and a `postStartCommand` produced by `firewallPostStartCommand()` that `sudo`-runs the firewall against the baked `AllowedDomainsPath` (then backgrounds the refresh daemon), with any user `postStartCommand` chained after it via `&&`. User `features` (language runtimes, `apt-packages`) pass through untouched and layer on top of the built Dockerfile.

On its own build path, bunker ignores `build`/`runArgs` entirely (its `DevContainer` struct doesn't model them) and builds from its own embedded Dockerfile instead; `LoadProjectConfig` calls `stripBunkerPostStart` (`internal/devcontainer/load.go`) to strip the generated firewall bootstrap back out of `PostStartCommand`, since bunker's native `RunPostStart` already execs the firewall directly as root (`internal/container/lifecycle.go`) and the baked `AllowedDomainsPath` file isn't even present in bunker's native image. `bunkerManagedFeaturePrefixes` in `load.go` still strips any of the old Feature refs (`claude-code`, the deleted `claude-bunker/firewall`, `claude-bunker/hardening`, `common-utils`) if a hand-edited devcontainer.json still contains one — a defensive no-op today, since `Generate` no longer emits any of them.

## Conventions

- **Error handling**: `die(msg)` for fatal errors (prints to stderr, cleans up runner, exits). `warn()`, `info()`, `verbose()` respect the verbosity level (`-1`=quiet, `0`=normal, `1`=verbose).
- **Flag parsing**: Root command manually extracts flags via `extractBunkerFlags()` since Cobra flag parsing is disabled. Supports `--flag value` and `--flag=value`.
- **Platform code**: Build-tag files (`_unix.go`, `_windows.go`) for resize and TTY handling. Windows uses polling; Unix uses SIGWINCH.
- **Testing**: Table-driven tests with `t.Run()` subtests. Fingerprint tests verify determinism and change detection.
- **Container constants**: User `claude-bunker`, home `/home/claude-bunker`, workspace `/workspace`, secrets `/run/secrets`, managed settings `/etc/claude-code`.
- **Env var expansion**: Config values support `$VAR`, `${VAR}`, and `${VAR:-default}` syntax.

## CLI Commands

| Command | Purpose |
|---------|---------|
| `claude-bunker [claude-flags...]` | Run Claude Code in sandbox (default) |
| `claude-bunker shell` | Open bash shell in sandbox |
| `claude-bunker init` | Interactive config wizard |
| `claude-bunker status` | Show sandbox state |
| `claude-bunker doctor` | Environment readiness check (Docker/version/devcontainer) |
| `claude-bunker prune` | Manage Docker volumes and images |
| `claude-bunker logs` | View container logs |
| `claude-bunker sessions [list\|stop\|attach\|logs]` | Session manager (TUI + scripting subcommands) |
| `claude-bunker completion <shell>` | Shell completion script |
| `claude-bunker version` | Print version (`--json`) |
| `claude-bunker --dump-dockerfile [dir]` | Export Docker build context (used by CI) |
