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

`main.go` → `cmd.Execute()` → root command (`cmd/run.go`):
1. `extractBunkerFlags()` manually parses claude-bunker flags (root command has `DisableFlagParsing: true` so unknown flags pass through to `claude`)
2. Load project config from `.claude/.claude-bunker/config.json`
3. Check fingerprints — rebuild image or recreate container only when config changes
4. Build Docker image if needed (embedded scripts + generated Dockerfile, all in-memory tar)
5. Create/reuse container, configure firewall, seed sandbox settings
6. `ExecInteractive()` — attach TTY to running container
7. Cleanup on exit (stop/remove container unless `--keep`)

### Package Layout

| Package | Role |
|---------|------|
| `cmd/` | Cobra CLI commands. Root disables flag parsing; subcommands re-enable it. |
| `internal/config/` | Config loading (`project.go`), SHA-256 fingerprinting (`fingerprint.go`), deterministic container naming (`naming.go`), env var expansion (`expand.go`) |
| `internal/container/` | Docker API wrapper: image build (`build.go`), Dockerfile generation (`generate.go`), container lifecycle (`lifecycle.go`), interactive exec (`exec.go`), domain/firewall management (`domains.go`) |
| `internal/container/scripts/` | Embedded shell scripts (`//go:embed`): `init-firewall.sh`, `refresh-firewall.sh`, `firewall-common.sh`, `base.dockerfile.tmpl` |
| `internal/sandbox/` | Sandbox settings seeding (`seed.go`), plugin loading (`plugins.go`), proxy config (`proxy.go`) |
| `internal/platform/` | Platform-specific TTY, terminal resize (Unix signals vs Windows polling), Windows VT support |

### Two Config Systems

- **`.claude/.claude-bunker/config.json`** — container infrastructure: allowed domains, apt packages, devcontainer features, env vars, secrets, hooks
- **`.claude/settings.json`** — Claude Code behavior settings, merged with managed-settings.json inside the container

### Five Security Layers

1. **Docker container** — process isolation
2. **iptables firewall** — default-deny egress, IPv6 blocked, domain allowlist resolved to IPs at startup, self-test verifies blocking
3. **Bubblewrap sandbox** — Claude Code's built-in sandbox with domain-level filtering
4. **managed-settings.json** — enforced Claude Code settings (injected to `/etc/claude-code/`)
5. **Bind-mount workspace** — only project directory is visible, exclude paths overlaid with tmpfs

### Secrets

Auth tokens are injected via tmpfs at `/run/secrets/`, never as environment variables. An auth wrapper script (`~/.claude-auth-wrapper.sh`) exports them before exec.

### Fingerprinting & Caching

`ImageFingerprint()` hashes version + Dockerfile + scripts + apt + features + env + onCreateCommand. `ContainerFingerprint()` hashes domains + workspace + excludes + postStartCommand. Reproducible mod times (`2025-01-01T00:00:00Z`) ensure Docker layer cache hits.

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
| `claude-bunker prune` | Manage Docker volumes |
| `claude-bunker logs` | View container logs |
| `claude-bunker --dump-dockerfile [dir]` | Export Docker build context (used by CI) |
