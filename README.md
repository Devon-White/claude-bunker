# claude-bunker

A portable, sandboxed Linux container for running [Claude Code](https://docs.anthropic.com/en/docs/claude-code) securely in any project. Point it at any directory and get a firewalled environment where Claude can work freely without risking your host system or leaking data to unauthorized endpoints.

## Why

Running Claude Code with `--dangerously-skip-permissions` is powerful but risky on a bare host. Claude can execute arbitrary shell commands, install packages, and access the network. A single prompt injection in a dependency README or issue body could exfiltrate your credentials.

claude-bunker solves this by running Claude inside a Docker container with:

- **Network firewall** -- default-deny iptables policy. Only Anthropic's API, GitHub, npm, and a handful of essential services are reachable. Everything else is blocked.
- **Inner sandbox** -- Claude Code's bubblewrap sandbox is enabled automatically and enforced via tamper-proof managed settings.
- **No credential passthrough** -- API keys and tokens are never injected via environment variables. You authenticate once inside the container, and credentials are stored in an isolated Docker volume.
- **Bind-mounted workspace** -- your project directory is mounted read-write into the container. Claude edits your actual files. You commit and push from your host.

## Quick start

**Prerequisites:** [Docker](https://www.docker.com/get-started/), [Go 1.26+](https://go.dev/dl/).

```bash
# Install via go install
go install github.com/Devon-White/claude-bunker@latest

# Or download a release binary from GitHub Releases and add to your PATH

# Navigate to any project and run Claude
cd ~/projects/my-app
claude-bunker
```

That's it. The sandbox builds on first run (takes a few minutes, cached after that), starts automatically, and tears down when Claude exits. Configuration changes are detected and trigger a rebuild automatically.

```bash
# Skip permission prompts (safe -- the container is firewalled)
claude-bunker --dangerously-skip-permissions

# Run with a direct prompt
claude-bunker -p "fix the failing tests"

# All flags pass through to the claude CLI
claude-bunker --model sonnet -p "add tests for auth.ts"
```

## Two config systems

claude-bunker has two separate config files. They control different things and don't overlap:

| File | Controls | Read by |
|------|----------|---------|
| `.claude/.claude-bunker/config.json` | Container infrastructure: what's mounted, what's reachable, packages, env vars | claude-bunker (before container starts) |
| `.claude/settings.json` | Claude Code behavior: permissions, hooks, sandbox overrides | Claude Code (inside the container) |

Think of it this way: `config.json` sets up the **room** Claude works in. `settings.json` sets the **rules** Claude follows inside that room.

## Commands

| Command | Description |
|---------|-------------|
| `claude-bunker [flags]` | Run Claude. All unknown flags pass through to the `claude` CLI |
| `claude-bunker shell` | Open a zsh shell in the sandbox (for debugging or manual work) |
| `claude-bunker status` | Show sandbox state: container info, uptime, active sessions |
| `claude-bunker prune` | List and remove Docker volumes. Supports `--force` and `--all` |
| `claude-bunker completion <shell>` | Generate shell completion script (bash, zsh, fish, powershell) |
| `claude-bunker version` | Print the version |
| `claude-bunker help` | Show usage information |

### claude-bunker flags

These flags are consumed by claude-bunker and not passed through to the Claude CLI:

| Flag | Description |
|------|-------------|
| `--verbose` | Show detailed output during startup |
| `--quiet` | Suppress informational output |
| `--gh-token <token>` | GitHub fine-grained PAT for git operations inside the container |
| `--api-key <key>` | Anthropic API key (injected securely via tmpfs file, not env var) |
| `--oauth-token <token>` | Claude Code OAuth token for headless/CI auth |

All other flags (e.g. `--dangerously-skip-permissions`, `-p`, `--model`, `--team`) are passed through directly to the `claude` CLI.

## How it works

### Security layers

claude-bunker uses defense-in-depth with multiple independent security layers:

```
Layer 4: managed-settings.json     (tamper-proof sandbox enforcement)
Layer 3: Claude sandbox (bwrap)    (filesystem write restriction, domain-level network filter)
Layer 2: iptables firewall         (IP-level network allowlist, default-deny)
Layer 1: Docker container          (process/mount/network namespace isolation)
Layer 0: Bind-mount workspace      (your files, read-write by design)
```

If a prompt injection bypasses one layer, the next catches it. For example, if Claude disables the sandbox via `/sandbox` toggle, `managed-settings.json` (Layer 4) overrides it back. If the sandbox domain filter is somehow bypassed, the iptables firewall (Layer 2) still blocks the traffic at the IP level.

### Architecture

```
Host machine                         Docker container
+---------------------------+        +--------------------------------+
|                           |        |  iptables firewall (DROP all)  |
|  ~/projects/my-app/  ----bind----> |  /workspace/                   |
|                           | mount  |                                |
|  git push, git commit     |        |  Claude Code (as claude-bunker)|
|  (done on host)           |        |  bubblewrap sandbox            |
|                           |        |  zsh, tmux, git-delta, jq ...  |
+---------------------------+        +--------------------------------+
```

### What happens when you run `claude-bunker`

1. The Docker image is built via the Docker API (cached after first build)
2. Your project directory is bind-mounted to `/workspace`
3. `init-firewall.sh` runs -- configures iptables rules, resolves allowed IPs, verifies the firewall works
4. Sandbox defaults are injected into your project's Claude Code settings (always overwrites the `sandbox` key to ensure correctness)
5. Claude Code launches
6. When Claude exits, the container is stopped and removed (volumes persist for auth/history)

If the container is already running (from another terminal), Claude connects to it directly. The container only tears down when the last session exits.

### Automatic rebuild detection

claude-bunker fingerprints its configuration files and subdirectories. When you modify the Dockerfile, devcontainer.json, or `.claude-bunker/config.json`, the next run detects the change and rebuilds automatically. No manual `rebuild` command needed.

### Network firewall

The firewall uses a default-deny policy with an allowlist. Only these destinations are reachable:

| Service | Why |
|---------|-----|
| `api.anthropic.com` | Claude Code API |
| `statsig.anthropic.com`, `statsig.com` | Claude Code telemetry |
| `sentry.io` | Claude Code error reporting |
| `github.com` (all GitHub IP ranges) | Git operations, `gh` CLI |
| `registry.npmjs.org` | npm package installs |
| `pypi.org`, `files.pythonhosted.org` | Python package installs |
| `marketplace.visualstudio.com`, `vscode.blob.core.windows.net`, `update.code.visualstudio.com` | VS Code Remote Containers support |

All other outbound traffic is rejected immediately (ICMP admin-prohibited). IPv6 is fully blocked to prevent firewall bypass.

On startup, the firewall verifies itself:
- Confirms `example.com` is **blocked**
- Confirms `api.github.com` is **allowed**

If either check fails, the container refuses to start.

#### Dual domain allowlists

`allowDomains` in `config.json` configures two separate systems:

- **iptables firewall** -- resolves domains to IPs at startup, blocks at network level. Fast and reliable, but resolved IPs can become stale if a CDN rotates them during a long session.
- **Claude Code sandbox** -- filters by domain name at request time. Survives IP rotation but only applies when the sandbox is active.

Both exist for defense-in-depth: if one is bypassed, the other still blocks unauthorized traffic.

#### DNS resolution limitation

Domain IPs are resolved once when the container starts. CDN-backed services rotate IPs over time. In long-running containers, resolved IPs may become stale, causing allowed connections to fail. The sandbox layer (Layer 3) performs domain-level filtering that survives IP rotation. If connections to allowed domains start failing, restart the container to re-resolve IPs.

## Authentication

### First-time setup (OAuth)

On first launch, Claude Code prompts you to log in via browser. The OAuth flow redirects to localhost. Credentials are stored in a Docker volume mounted at `~/.claude` inside the container -- they persist across container restarts. You only authenticate once per project.

Each project gets its own Docker volume (keyed to the workspace directory name), so different projects can use different credentials. To reset credentials, use `claude-bunker prune`.

### API key users

Pass your Anthropic API key via the `--api-key` flag. The key is injected into the container via a tmpfs file at `/run/secrets/api_key` -- it never appears as an environment variable or in `docker inspect`.

```bash
claude-bunker --api-key sk-ant-...
```

You can also set the `ANTHROPIC_API_KEY` environment variable on the host -- claude-bunker will read it and inject it the same way.

### Headless / CI (OAuth token)

For CI or headless environments where browser auth is not possible, use `claude setup-token` on a machine with a browser to generate a long-lived OAuth token, then pass it to claude-bunker:

```bash
claude-bunker --oauth-token <token>
```

Or set the `CLAUDE_CODE_OAUTH_TOKEN` environment variable on the host.

### GitHub access (git push from container)

By default, the container has no git credentials. You push from your host. If you need git push from inside the container (autonomous workflows, CI), provide a [GitHub fine-grained PAT](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/managing-your-personal-access-tokens) scoped to the target repo:

```bash
claude-bunker --gh-token ghp_...
```

Or set it in your project's `config.json`:

```json
{ "ghToken": "ghp_..." }
```

The token is injected via tmpfs at `/run/secrets/gh_token` and configured as a git credential helper. The firewall limits blast radius -- the token is only usable against GitHub's IP ranges.

## Claude Code settings

claude-bunker deliberately does not bake Claude Code settings into the container image. Settings are a project-level concern -- different projects have different needs.

### Settings precedence

Claude Code reads settings from multiple sources, in order of precedence (highest first):

1. **`/etc/claude-code/managed-settings.json`** -- tamper-proof, baked into the Docker image. Cannot be overridden by any user action. Enforces `sandbox.enabled`, `allowUnsandboxedCommands: false`, and `enableWeakerNestedSandbox`.
2. **Command line arguments** -- flags passed directly to `claude`
3. **`.claude/settings.local.json`** -- personal overrides, never committed
4. **`.claude/settings.json`** -- shared project settings, meant to be committed
5. **`~/.claude/settings.json`** -- user-level defaults (inside the container, this is a Docker volume)

Same key in a higher-precedence source completely overrides the lower one. Because `managed-settings.json` is highest, sandbox enforcement cannot be disabled -- not even via the `/sandbox` toggle inside Claude.

### Automatic sandbox injection

On every container start, claude-bunker injects sandbox defaults into both `.claude/settings.json` and `.claude/settings.local.json` inside the container (on the tmpfs overlay -- your host files are not modified):

- **File doesn't exist** -- creates it with sandbox defaults
- **File exists** -- merges sandbox defaults, always overwriting the `sandbox` key to ensure correctness

The injected defaults include the bubblewrap sandbox configuration with `enableWeakerNestedSandbox` (required because Docker containers lack `CAP_SYS_ADMIN`) and the domain allowlist matching the firewall.

### Configuring your project

Add any Claude Code settings to your project's `.claude/settings.json`. Common examples:

**Permission deny list** -- block dangerous commands:
```json
{
  "permissions": {
    "deny": [
      "Bash(rm -rf *)",
      "Bash(git push --force *)",
      "Bash(git reset --hard *)",
      "Bash(git commit --no-verify *)",
      "Read(./.env)",
      "Read(./.env.*)",
      "Edit(./.env)",
      "Edit(./.env.*)"
    ]
  }
}
```

**Permission allow list** -- pre-approve safe commands:
```json
{
  "permissions": {
    "allow": [
      "Bash(git diff *)",
      "Bash(git status *)",
      "Bash(npm test *)",
      "Bash(npm run *)"
    ]
  }
}
```

See the [Claude Code settings documentation](https://docs.anthropic.com/en/docs/claude-code/settings) for all available options.

### Personal overrides

Create `.claude/settings.local.json` (gitignored) for machine-specific settings that shouldn't affect your team:

```json
{
  "permissions": {
    "allow": [
      "Bash(cargo test *)"
    ]
  }
}
```

This takes precedence over `.claude/settings.json` for your machine only. Note that sandbox settings in `settings.local.json` are overwritten by claude-bunker on each start to prevent Claude Code from auto-disabling the sandbox.

## Project config

Place a `.claude/.claude-bunker/config.json` in your project to customize container behavior. All fields are optional.

```json
{
  "workspace": "./packages/backend",
  "exclude": ["secrets/", ".env.production"],
  "allowDomains": ["private-registry.company.com"],
  "apt": ["python3", "python3-pip"],
  "features": {
    "ghcr.io/devcontainers/features/node:1": {"version": "20"}
  },
  "env": {"PYTHONDONTWRITEBYTECODE": "1"},
  "postStartCommand": "pip install -r requirements.txt"
}
```

### Fields

#### workspace

Sets the working directory inside the container. The full project is still mounted at `/workspace` (preserving git context), but Claude and your shell start in the specified subdirectory.

```json
{ "workspace": "./packages/backend" }
```

Useful for monorepos where you want Claude focused on one package. Claude can still navigate to other directories if needed -- this is a convenience default, not a security boundary.

#### exclude

Hides paths inside the container using tmpfs overlays. The files are genuinely invisible -- not just permission-blocked, but replaced with empty filesystems.

```json
{ "exclude": ["secrets/", ".env.production", "credentials/"] }
```

Paths are relative to the project root. This is a strong isolation mechanism: even with `--dangerously-skip-permissions`, Claude cannot see excluded files because they don't exist in the container's filesystem.

#### allowDomains

Adds domains to both the iptables firewall allowlist and the Claude Code sandbox network settings. These are resolved at container startup and added alongside the built-in domains.

```json
{ "allowDomains": ["private-registry.company.com", "pypi.org"] }
```

Domains must have at least 2 segments (e.g. `example.com`). Overly broad patterns like `*.com` are rejected. If a domain fails to resolve, the container still starts (warning is logged, domain is skipped).

#### apt

Additional apt packages to install in the container. Avoids the need to fork the repo and edit the Dockerfile for common tools.

```json
{ "apt": ["python3", "python3-pip", "ripgrep"] }
```

#### features

OCI [devcontainer features](https://containers.dev/features) to install. These are community-maintained feature packages that add languages, tools, and runtimes.

```json
{
  "features": {
    "ghcr.io/devcontainers/features/node:1": {"version": "20"},
    "ghcr.io/devcontainers/features/rust:1": {}
  }
}
```

#### env

Environment variables injected into the container at creation time.

```json
{ "env": {"PYTHONDONTWRITEBYTECODE": "1", "NODE_ENV": "development"} }
```

#### postStartCommand

A shell command that runs after the container is created (after the firewall is configured). Runs as the `claude-bunker` user inside `/workspace`.

```json
{ "postStartCommand": "pip install -r requirements.txt && npm install" }
```

**Trust boundary:** This command comes from the project's `config.json`, which lives inside the cloned repository. A malicious `config.json` can execute arbitrary shell commands here -- this is inherent to the devcontainer model (VS Code has the same trust issue). Review `config.json` before running claude-bunker on untrusted repos. The firewall limits blast radius by restricting network access.

#### ghToken

GitHub fine-grained PAT for git operations inside the container. Same as the `--gh-token` CLI flag. The CLI flag takes priority if both are set.

#### seedHistory

Whether to seed session history from the host into the container. Defaults to `true`. Set to `false` if your host sessions contain sensitive information you don't want inside the container.

## Multiple projects

Each project gets its own container and Docker volumes, derived from the workspace directory name. You can run multiple projects simultaneously:

```bash
# Terminal 1
cd ~/projects/frontend
claude-bunker

# Terminal 2
cd ~/projects/backend
claude-bunker
```

Each container has its own firewall, credentials, and bash history.

## Agent teams

The container includes tmux with a pre-configured setup for Claude Code's agent teams feature. The `--team` flag passes through to the Claude CLI. Tmux prefix is `Ctrl-a`.

```bash
claude-bunker --team
```

Useful tmux bindings:

| Binding | Action |
|---------|--------|
| `Ctrl-a \|` | Split pane horizontally |
| `Ctrl-a -` | Split pane vertically |
| `Alt+Arrow` | Navigate between panes |
| `Ctrl+Arrow` | Resize panes |
| `Ctrl-a S` | Toggle synchronized input to all panes |
| `Ctrl-a M-5` | Tiled layout (all panes equal) |

## Extending

### Adding tools to the container

There are three ways to add tools, from simplest to most flexible:

**1. apt packages** -- for system libraries and simple tools:

```json
{ "apt": ["python3", "python3-pip", "ripgrep", "libsqlite3-dev"] }
```

**2. Devcontainer features** -- for language runtimes and complex toolchains:

```json
{
  "features": {
    "ghcr.io/devcontainers/features/go:1": { "version": "1.22" },
    "ghcr.io/devcontainers/features/node:1": { "version": "20" },
    "ghcr.io/devcontainers/features/rust:1": {}
  }
}
```

Features are OCI packages from the [devcontainer features registry](https://containers.dev/features). Each feature bundles an `install.sh` script that handles version management, PATH setup, and dependencies. Browse the registry to find the feature reference for your toolchain -- the key is the full OCI image reference and the value is an object of options (check each feature's documentation for available options).

Features are installed in dependency order during image build. The image is cached, so subsequent runs skip installation.

**3. postStartCommand** -- for project-specific setup that depends on your source code:

```json
{ "postStartCommand": "pip install -r requirements.txt && npm install" }
```

This runs after the container starts, every time. Use it for installing project dependencies, not for installing tools (those belong in `apt` or `features` so they're cached in the image).

**Choosing between them:** Use `apt` for system packages (`ffmpeg`, `libssl-dev`). Use `features` for language runtimes and toolchains (`go`, `node`, `rust`, `python`). Use `postStartCommand` for project dependency installation (`npm install`, `pip install`). All three can be combined in the same `config.json`.

### Adding allowed domains

**Per-project (recommended):** Add `allowDomains` to your project's `config.json`:

```json
{ "allowDomains": ["private-registry.company.com"] }
```

This adds domains to both the firewall and sandbox settings automatically. No forking required.

**Globally (all projects):** The firewall allowlist is compiled into the Go binary. To customize it globally, fork the repo and edit `internal/container/scripts/init-firewall.sh`.

## Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `CLAUDE_BUNKER_WS` | `$PWD` | Workspace directory to mount (override current directory) |
| `CLAUDE_BUNKER_QUIET` | unset | Set to `1` to suppress informational output (same as `--quiet`) |
| `ANTHROPIC_API_KEY` | unset | Anthropic API key (read by claude-bunker if `--api-key` not provided) |
| `CLAUDE_CODE_OAUTH_TOKEN` | unset | Claude Code OAuth token (read if `--oauth-token` not provided) |
| `TZ` | `UTC` | Container timezone |

## Platform support

| Platform | Status | Notes |
|----------|--------|-------|
| macOS | Supported | `consistency=delegated` mount for performance |
| Linux | Supported | Primary target |
| Windows (Git Bash / MSYS2) | Supported | Path normalization handled automatically |
| WSL2 | Supported | Works via Docker Desktop or native Docker |

## Security model

### What the container provides

- **Network isolation** -- iptables default-deny with IPv4 allowlist and full IPv6 block
- **Process isolation** -- Docker container boundary, non-root `claude-bunker` user
- **Inner sandbox** -- bubblewrap (weak mode) enforced via `managed-settings.json` (cannot be toggled off)
- **No credential passthrough** -- auth tokens stored in isolated Docker volume or injected via tmpfs files, never in environment variables
- **Firewall self-test** -- container refuses to start if the firewall doesn't work

### What the container does NOT provide

- **File isolation** -- your project is bind-mounted read-write. Claude can modify any file in the project. This is by design -- Claude needs to edit your code.
- **Git push protection** -- Claude can `git commit` inside the container (it has git). Pushing requires credentials, which depend on your setup. For maximum safety, push from your host.
- **Infallible command filtering** -- the firewall is the real security boundary. There is no regex-based command filter because those are inherently bypassable. Use Claude Code's [permission deny lists](https://docs.anthropic.com/en/docs/claude-code/settings) in your project settings instead.

### Known trade-offs

- **GitHub access is broad** -- the firewall allows all GitHub IP ranges (web + api + git). A prompt injection could theoretically exfiltrate data to a public GitHub repo. This is necessary for git operations to work. If your workflow keeps git operations on the host, you could remove GitHub IPs from the firewall.
- **DNS resolution at startup** -- domain IPs are resolved once when the container starts. If an IP changes while the container is running, the new IP won't be reachable until restart. The sandbox layer provides domain-level filtering as a backup.
- **AppArmor not enforced** -- Docker Desktop on macOS/Windows doesn't support AppArmor profiles. On Linux with AppArmor available, consider adding a custom profile for additional process-level restrictions.

## Troubleshooting

**Container fails to start with DNS errors**
The firewall script resolves domain names at startup. If your network is slow or a DNS server is temporarily unreachable, startup will fail. Run `claude-bunker` again to retry.

**Claude can't reach the API**
The firewall might have stale IPs. Exit Claude and run `claude-bunker` again -- the container is recreated fresh each time.

**Credentials lost**
Credentials are stored in Docker volumes. They persist across sessions. If you ran `claude-bunker prune`, volumes were deleted and you need to log in again.

**Windows path errors**
If you see paths like `c:\c\Users\...` in error messages, ensure you're running from Git Bash without manually setting `MSYS_NO_PATHCONV`. The tool handles path conversion automatically.

**Docker not running**
claude-bunker requires Docker to be running. On first use, it checks for the Docker daemon and provides a clear error message if Docker is not available.

## Project structure

```
claude-bunker/
  main.go                             # Entry point
  cmd/                                # CLI commands (root, shell, init, prune, status, version)
  internal/
    config/                           # Configuration loading, fingerprinting, naming
    container/                        # Docker API (build, create, exec, copy)
      scripts/                        # Embedded scripts (init-firewall.sh, tmux.conf, zshrc)
    sandbox/                          # Sandbox settings seeding
    platform/                         # Platform-specific helpers (TTY, paths)
  .goreleaser.yml                     # Release automation
  .gitattributes                      # LF line endings for shell scripts
  .gitignore                          # Ignores .env, .claude/, etc.

# In your project (created by `claude-bunker init` or manually):
your-project/
  .claude/
    .claude-bunker/
      config.json                     # Optional -- container overrides
    settings.json                     # Claude Code project settings (sandbox injected here)
    settings.local.json               # Personal overrides (gitignored)
```

## License

MIT
