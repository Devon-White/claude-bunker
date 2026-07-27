# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository. Codex will review your code when you're done.

## What This Project Is

claude-bunker is a Go CLI that runs Claude Code inside a hardened Docker container with defense-in-depth security: iptables firewall, bubblewrap sandbox, managed settings, and bind-mounted workspace isolation. It prevents prompt injection attacks from exfiltrating credentials.

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
| `internal/container/` | Docker API wrapper: image build (`build.go`), Dockerfile generation (`generate.go`), container lifecycle (`lifecycle.go`), interactive exec (`exec.go`), domain/firewall management (`domains.go`), plus `lockfile.go` (devcontainer-lock.json), `baseimage.go`, `volumes.go`, `features.go`, `presets.go`, `constants.go`, `copy.go`, `client.go`, `embed.go` |
| `internal/container/scripts/` | Embedded shell scripts (`//go:embed`): `init-firewall.sh`, `refresh-firewall.sh`, `firewall-common.sh`, `base.dockerfile.tmpl`, `tmux.conf` |
| `internal/buildlock/` | Cross-process build lock (unix/windows variants) |
| `internal/sessions/` | Session/subagent tree via `claude agents --json` (store, watcher, manager) |
| `internal/sandbox/` | Sandbox settings seeding (`seed.go`), plugin loading (`plugins.go`), proxy config (`proxy.go`) |
| `internal/log/` | Logging helpers |
| `internal/platform/` | Platform-specific TTY, terminal resize (Unix signals vs Windows polling), Windows VT support |

### Config

- **`.devcontainer/devcontainer.json`** — the single project config. Standard devcontainer keys at top level (features, containerEnv, onCreateCommand, postStartCommand, capAdd, remoteUser); bunker extras under `customizations["claude-bunker"]` (exclude, allowDomains, plugins, ghToken, seedHistory, workspace). Parsed by `internal/devcontainer`. OS packages are NOT a bunker extra — they're expressed via the standard `ghcr.io/rocker-org/devcontainer-features/apt-packages:1` feature in the top-level `features` map, a normal user feature that bunker resolves and VS Code/Codespaces honor the same way. A deprecation warning fires if a config still has the old `customizations["claude-bunker"].apt` key.
- **`.devcontainer/devcontainer-lock.json`** — pins feature digests (reproducible builds); digests fold into the image fingerprint.
- Enforcement of Claude Code behavior is a runtime read-only `/etc/claude-code/managed-settings.json` (written each start); host `settings.json`/`settings.local.json` are NOT injected.

### Five Security Layers

1. **Docker container** — process isolation
2. **iptables firewall** — default-deny egress, IPv6 blocked, domain allowlist resolved to IPs at startup, self-test verifies blocking
3. **Bubblewrap sandbox** — Claude Code's built-in sandbox with domain-level filtering
4. **managed-settings.json** — enforced Claude Code settings (injected to `/etc/claude-code/`)
5. **Bind-mount workspace** — only project directory is visible, exclude paths overlaid with tmpfs

### Secrets

Auth tokens are injected via tmpfs at `/run/secrets/`, never as environment variables. An auth wrapper script (`~/.claude-auth-wrapper.sh`) exports them before exec.

### Fingerprinting & Caching

The public API is `CompareFingerprints(BuildInput, containerName) FingerprintResult` (unexported `imageFingerprint`/`containerFingerprint` do the hashing). The image fingerprint covers version + Dockerfile + scripts + features + env + onCreateCommand **plus resolved feature digests from `devcontainer-lock.json`** (this is also how OS packages now factor in, since they're expressed as a feature); the container fingerprint covers domains + workspace + excludes + postStartCommand + plugins + seedHistory. Reproducible mod times (`2025-01-01T00:00:00Z`) ensure Docker layer cache hits.

### OCI Features / Portability (VS Code / Codespaces)

`claude-bunker init` also targets the VS Code/Codespaces path via two OCI Dev Container Features, sourced under `features/src/`: `firewall` (`ghcr.io/Devon-White/claude-bunker/firewall`) ports the iptables firewall scripts + builtin domain allowlist, and `hardening` (`ghcr.io/Devon-White/claude-bunker/hardening`) installs bubblewrap and sets `securityOpt: apparmor=unconfined`. The custom seccomp profile can't be embedded in a Feature (the spec's `seccomp` value is a host-resolved path), so it's applied via `runArgs: ["--security-opt", "seccomp=${localWorkspaceFolder}/.devcontainer/seccomp.json"]` (`internal/devcontainer/generate.go`) plus a sibling `.devcontainer/seccomp.json` written by `init` from `container.SeccompProfileJSON()` — the same profile bunker's native container creation uses. None of these portable artifacts are hand-maintained: `cmd/genfeatures` derives the firewall Feature's scripts and domain list from the embedded scripts and `container.BuiltinDomains()`, and `features/firewall_drift_test.go` fails CI if the packaged copies ever diverge from source. On its own build path, bunker strips both features from `ProjectConfig` (`bunkerManagedFeaturePrefixes` in `internal/devcontainer/load.go`) and ignores `runArgs` entirely, so nothing double-applies. The Features are published to GHCR from `features/src/` only by the opt-in `.github/workflows/publish-features.yml` (manual dispatch or a `features-v*` tag) — never as part of the normal `v*` release path.

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
