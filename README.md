# claude-bunker

A portable, sandboxed Linux container for running [Claude Code](https://docs.anthropic.com/en/docs/claude-code) securely in any project. Point it at any directory and get a firewalled environment where Claude can work freely without risking your host system or leaking data to unauthorized endpoints.

## Why

Running Claude Code with `--dangerously-skip-permissions` is powerful but risky on a bare host. Claude can execute arbitrary shell commands, install packages, and access the network. A single prompt injection in a dependency README or issue body could exfiltrate your credentials.

claude-bunker solves this by running Claude inside a Docker container with:

- **Network firewall** — default-deny iptables policy. Only Anthropic's API, GitHub, npm, and a handful of essential services are reachable. Everything else is blocked.
- **Inner sandbox** — Claude Code's bubblewrap sandbox is enabled automatically inside the container, adding a second isolation layer.
- **No credential passthrough** — API keys and tokens are never injected via environment variables. You authenticate once inside the container, and credentials are stored in an isolated Docker volume.
- **Bind-mounted workspace** — your project directory is mounted read-write into the container. Claude edits your actual files. You commit and push from your host.

## Quick start

**Prerequisites:** Docker and Node.js (for the devcontainer CLI, auto-installed if missing).

```bash
# Clone claude-bunker somewhere permanent
git clone https://github.com/your-username/claude-bunker.git ~/claude-bunker

# Add to your PATH (add this line to .bashrc / .zshrc)
export PATH="$PATH:$HOME/claude-bunker"

# Navigate to any project and run Claude
cd ~/projects/my-app
claude-bunker
```

That's it. The sandbox builds on first run (takes a few minutes, cached after that), starts automatically, and tears down when Claude exits. Configuration changes are detected and trigger a rebuild automatically.

```bash
# Skip permission prompts (safe — the container is firewalled)
claude-bunker --dangerously-skip-permissions

# Run with a direct prompt
claude-bunker -p "fix the failing tests"

# All flags pass through to the claude CLI
claude-bunker --model sonnet -p "add tests for auth.ts"
```

## Commands

| Command | Description |
|---------|-------------|
| `claude-bunker [flags]` | Run Claude. All flags pass through to the `claude` CLI. Sandbox starts/stops automatically |
| `claude-bunker shell` | Open a zsh shell in the sandbox (for debugging or manual work) |
| `claude-bunker prune` | List and remove orphaned Docker volumes from old containers |
| `claude-bunker help` | Show usage information |

## How it works

### Architecture

```
Host machine                         Docker container
+---------------------------+        +--------------------------------+
|                           |        |  iptables firewall (DROP all)  |
|  ~/projects/my-app/  ----bind----> |  /workspace/                   |
|                           | mount  |                                |
|  git push, git commit     |        |  Claude Code (as node user)    |
|  (done on host)           |        |  bubblewrap sandbox            |
|                           |        |  zsh, tmux, git-delta, jq ...  |
+---------------------------+        +--------------------------------+
```

The container provides **infrastructure isolation** (network, process, filesystem). Your project provides **Claude Code configuration** (permissions, hooks, settings).

### What happens when you run `claude-bunker`

1. The devcontainer CLI builds the Docker image (cached after first build)
2. Your project directory is bind-mounted to `/workspace`
3. `init-firewall.sh` runs — configures iptables rules, resolves allowed IPs, verifies the firewall works
4. `inject-sandbox-defaults.sh` runs — ensures your project has Claude Code sandbox settings
5. Claude Code launches
6. When Claude exits, the container is stopped and removed (volumes persist for auth/history)

If the container is already running (from another terminal), Claude connects to it directly. The container only tears down when the last session exits.

### Automatic rebuild detection

claude-bunker fingerprints its configuration files. When you modify the Dockerfile, devcontainer.json, or `.claude-bunker.json`, the next run detects the change and rebuilds automatically. No manual `rebuild` command needed.

### Network firewall

The firewall uses a default-deny policy with an allowlist. Only these destinations are reachable:

| Service | Why |
|---------|-----|
| `api.anthropic.com` | Claude Code API |
| `statsig.anthropic.com`, `statsig.com` | Claude Code telemetry |
| `sentry.io` | Claude Code error reporting |
| `github.com` (all GitHub IP ranges) | Git operations, `gh` CLI |
| `registry.npmjs.org` | npm package installs |
| `marketplace.visualstudio.com`, `vscode.blob.core.windows.net`, `update.code.visualstudio.com` | VS Code Remote Containers support |

All other outbound traffic is rejected immediately (ICMP admin-prohibited). IPv6 is fully blocked to prevent firewall bypass.

On startup, the firewall verifies itself:
- Confirms `example.com` is **blocked**
- Confirms `api.github.com` is **allowed**

If either check fails, the container refuses to start.

## Claude Code settings

claude-bunker deliberately does not bake Claude Code settings into the container image. Settings are a project-level concern — different projects have different needs.

### Settings hierarchy

Claude Code reads settings from multiple files, in order of precedence (highest first):

1. **`.claude/settings.local.json`** — personal overrides, never committed
2. **`.claude/settings.json`** — shared project settings, meant to be committed
3. **`~/.claude/settings.json`** — user-level defaults (inside the container, this is a Docker volume)

Same key in a higher-precedence file completely overrides the lower one.

### Automatic sandbox injection

On every container start, `inject-sandbox-defaults.sh` checks your project's `.claude/settings.json`:

- **File doesn't exist** — creates it with sandbox defaults
- **File exists but has no `sandbox` key** — merges sandbox defaults into the existing file, preserving all other settings
- **File already has `sandbox` settings** — does nothing

The injected defaults:

```json
{
  "sandbox": {
    "enabled": true,
    "enableWeakerNestedSandbox": true,
    "network": {
      "allowedDomains": [
        "api.anthropic.com",
        "statsig.anthropic.com",
        "statsig.com",
        "sentry.io",
        "github.com",
        "*.github.com",
        "registry.npmjs.org"
      ]
    }
  }
}
```

This enables Claude Code's inner bubblewrap sandbox as a second defense layer on top of the iptables firewall. The `enableWeakerNestedSandbox` flag is necessary because Docker containers don't have `CAP_SYS_ADMIN`, which full bubblewrap requires.

The script only touches `.claude/settings.json` (the shared, committable file). It never modifies `.claude/settings.local.json`. You can always override the injected sandbox settings via your local file.

### Configuring your project

Add any Claude Code settings to your project's `.claude/settings.json`. Common examples:

**Permission deny list** — block dangerous commands:
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

**Permission allow list** — pre-approve safe commands:
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

**PreToolUse hooks** — run custom scripts before Claude executes tools:
```json
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Bash",
        "hooks": [
          {
            "type": "command",
            "command": "/path/to/your/guard-script.sh"
          }
        ]
      }
    ]
  }
}
```

**Agent teams:**
```json
{
  "env": {
    "CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS": "1"
  },
  "teammateMode": "auto"
}
```

See the [Claude Code settings documentation](https://docs.anthropic.com/en/docs/claude-code/settings) for all available options.

### Personal overrides

Create `.claude/settings.local.json` (gitignored) for machine-specific settings that shouldn't affect your team:

```json
{
  "sandbox": {
    "enabled": false
  }
}
```

This takes precedence over `.claude/settings.json` for your machine only.

## Authentication

Credentials are stored in a Docker volume mounted at `~/.claude` inside the container. No API keys or tokens are passed through environment variables.

On first launch, Claude will prompt you to log in. Credentials persist across sessions — you only authenticate once per project.

Each project gets its own Docker volume (keyed to the workspace directory name), so different projects can use different credentials. To reset credentials, use `claude-bunker prune` to remove Docker volumes.

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

The container includes tmux with a pre-configured setup for Claude Code's agent teams feature. Tmux prefix is `Ctrl-a`.

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

## Project config (.claude-bunker.json)

Place a `.claude-bunker.json` in your project root to customize container behavior per-project. This file controls the **container** — what gets mounted, what's reachable, where Claude starts. For **Claude Code** settings (permissions, hooks, sandbox overrides), use `.claude/settings.json` instead.

```json
{
  "workspace": "./packages/backend",
  "exclude": ["secrets/", ".env.production"],
  "allowDomains": ["private-registry.company.com", "artifactory.internal.io"]
}
```

All fields are optional.

### workspace

Sets the working directory inside the container. The full project is still mounted at `/workspace` (preserving git context), but Claude and your shell start in the specified subdirectory.

```json
{ "workspace": "./packages/backend" }
```

Useful for monorepos where you want Claude focused on one package. Claude can still navigate to other directories if needed — this is a convenience default, not a security boundary.

### exclude

Hides paths inside the container using tmpfs overlays. The files are genuinely invisible — not just permission-blocked, but replaced with empty filesystems.

```json
{ "exclude": ["secrets/", ".env.production", "credentials/"] }
```

Paths are relative to the project root. This is a strong isolation mechanism: even with `--dangerously-skip-permissions`, Claude cannot see excluded files because they don't exist in the container's filesystem.

### allowDomains

Adds domains to both the iptables firewall allowlist and the Claude Code sandbox network settings. These are resolved at container startup and added alongside the built-in domains.

```json
{ "allowDomains": ["private-registry.company.com", "pypi.org"] }
```

If a domain fails to resolve, the container still starts (warning is logged, domain is skipped). This is intentionally non-fatal since extra domains are user-provided.

### How the config layers work

There are three layers of configuration, each handling a different concern:

| File | Scope | Controls |
|------|-------|----------|
| `.claude-bunker.json` | Container | What's mounted, what's reachable, working directory |
| `.claude/settings.json` | Claude Code (shared) | Permissions, hooks, sandbox config |
| `.claude/settings.local.json` | Claude Code (personal) | Per-developer overrides |

These don't overlap. `.claude-bunker.json` is read by the launcher before the container starts. Claude Code settings are read by Claude Code inside the container.

## Extending

### Adding tools to the container

Fork the repo and edit `.devcontainer/Dockerfile` to add packages:

```dockerfile
# Example: add Python and pip
RUN apt-get update && apt-get install -y --no-install-recommends \
  python3 \
  python3-pip \
  && apt-get clean && rm -rf /var/lib/apt/lists/*
```

The next `claude-bunker` run will detect the Dockerfile change and rebuild automatically.

### Adding allowed domains

**Per-project (recommended):** Add `allowDomains` to your project's `.claude-bunker.json`:

```json
{ "allowDomains": ["private-registry.company.com"] }
```

This adds domains to both the firewall and sandbox settings automatically. No forking required.

**Globally (all projects):** Edit `.devcontainer/init-firewall.sh` and add domains to the resolution loop. You should also update `.devcontainer/inject-sandbox-defaults.sh` to match.

### Pinning the Claude Code version

Set the build arg in `.devcontainer/devcontainer.json`:

```json
{
  "build": {
    "args": {
      "CLAUDE_CODE_VERSION": "1.0.5"
    }
  }
}
```

### Using with VS Code Remote Containers

The `.devcontainer/devcontainer.json` is a standard devcontainer config. You can open any project in VS Code, point it at claude-bunker's devcontainer.json, and get the same sandboxed environment with the Claude Code extension pre-installed.

## Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `CLAUDE_BUNKER_DIR` | Script directory | Path to the claude-bunker repo |
| `CLAUDE_BUNKER_WS` | `$PWD` | Workspace directory to mount |
| `TZ` | `America/Los_Angeles` | Container timezone |
| `CLAUDE_CODE_VERSION` | `latest` | Claude Code version to install (build arg) |

## Platform support

| Platform | Status | Notes |
|----------|--------|-------|
| macOS | Supported | `consistency=delegated` mount for performance |
| Linux | Supported | Primary target |
| Windows (Git Bash / MSYS2) | Supported | Path normalization handled automatically |
| WSL2 | Supported | Works via Docker Desktop or native Docker |

## Security model

### What the container provides

- **Network isolation** — iptables default-deny with IPv4 allowlist and full IPv6 block
- **Process isolation** — Docker container boundary, non-root `node` user
- **Inner sandbox** — bubblewrap (weak mode) enabled via injected settings
- **No credential passthrough** — auth tokens stored in isolated Docker volume, never in environment variables
- **Firewall self-test** — container refuses to start if the firewall doesn't work

### What the container does NOT provide

- **File isolation** — your project is bind-mounted read-write. Claude can modify any file in the project. This is by design — Claude needs to edit your code.
- **Git push protection** — Claude can `git commit` inside the container (it has git). Pushing requires credentials, which depend on your setup. For maximum safety, push from your host.
- **Infallible command filtering** — the firewall is the real security boundary. There is no regex-based command filter because those are inherently bypassable. Use Claude Code's [permission deny lists](https://docs.anthropic.com/en/docs/claude-code/settings) in your project settings instead.

### Known trade-offs

- **GitHub access is broad** — the firewall allows all GitHub IP ranges (web + api + git). A prompt injection could theoretically exfiltrate data to a public GitHub repo. This is necessary for git operations to work. If your workflow keeps git operations on the host, you could remove GitHub IPs from the firewall.
- **DNS resolution at startup** — domain IPs are resolved once when the container starts. If an IP changes while the container is running, the new IP won't be reachable until restart.

## Troubleshooting

**Container fails to start with DNS errors**
The firewall script resolves domain names at startup. If your network is slow or a DNS server is temporarily unreachable, startup will fail. Run `claude-bunker` again to retry.

**"Permission denied" running claude-bunker.sh**
```bash
chmod +x /path/to/claude-bunker/claude-bunker.sh
```

**Claude can't reach the API**
The firewall might have stale IPs. Exit Claude and run `claude-bunker` again — the container is recreated fresh each time.

**Credentials lost**
Credentials are stored in Docker volumes. They persist across sessions. If you ran `claude-bunker prune`, volumes were deleted and you need to log in again.

**Windows path errors**
If you see paths like `c:\c\Users\...` in error messages, ensure you're running from Git Bash without manually setting `MSYS_NO_PATHCONV`. The script handles path conversion automatically.

## Project structure

```
claude-bunker/
  claude-bunker.sh                    # CLI launcher — the main entry point
  .devcontainer/
    devcontainer.json                 # Container config (mounts, env, startup)
    Dockerfile                        # Image definition (tools, Claude Code)
    init-firewall.sh                  # iptables firewall setup + verification
    inject-sandbox-defaults.sh        # Sandbox settings injection
    .tmux.conf                        # tmux configuration for agent teams
  .gitattributes                      # LF line endings for shell scripts
  .gitignore                          # Ignores .env, .claude/, node_modules/
  package.json                        # npm metadata for distribution

# In your project (created automatically or by you):
your-project/
  .claude-bunker.json                 # Optional — container overrides
  .claude/
    settings.json                     # Claude Code project settings (sandbox injected here)
    settings.local.json               # Personal overrides (gitignored)
```

## License

MIT
