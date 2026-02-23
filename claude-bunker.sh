#!/usr/bin/env bash
#
# claude-bunker — Run Claude Code in a sandboxed container.
#
# Usage:
#   claude-bunker [flags]     Run Claude (all flags pass through)
#   claude-bunker shell       Open a shell in the sandbox
#   claude-bunker prune       Remove orphaned Docker volumes
#   claude-bunker help        Show usage
#
# Setup — add to your .bashrc / .zshrc:
#   export PATH="$PATH:/path/to/claude-bunker"
#
set -euo pipefail

# ---------------------------------------------------------------------------
# Resolve directories
# ---------------------------------------------------------------------------
resolve_script_dir() {
    local source="${BASH_SOURCE[0]}"
    while [ -L "$source" ]; do
        local dir
        dir="$(cd -P "$(dirname "$source")" && pwd)"
        source="$(readlink "$source")"
        [[ "$source" != /* ]] && source="$dir/$source"
    done
    cd -P "$(dirname "$source")" && pwd
}

CLAUDE_BUNKER_DIR="${CLAUDE_BUNKER_DIR:-$(resolve_script_dir)}"
DEVCONTAINER_DIR="$CLAUDE_BUNKER_DIR/.devcontainer"
WORKSPACE="${CLAUDE_BUNKER_WS:-$(pwd)}"

# Unique container name per workspace directory
CONTAINER_NAME="claude-bunker-$(basename "$WORKSPACE" | tr '[:upper:]' '[:lower:]' | tr -cs '[:alnum:]-' '-' | sed 's/^-//;s/-$//')"

# Fingerprint cache for rebuild detection
CACHE_DIR="${HOME}/.cache/claude-bunker"

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------
info()  { echo "[claude-bunker] $*"; }
warn()  { echo "[claude-bunker] WARNING: $*" >&2; }
die()   { echo "[claude-bunker] ERROR: $*" >&2; exit 1; }

has_cmd() { command -v "$1" &>/dev/null; }

require_docker() {
    has_cmd docker || die "Docker is not installed or not in PATH."
    docker info &>/dev/null || die "Docker daemon is not running."
}

require_devcontainer() {
    if has_cmd devcontainer; then return 0; fi
    info "Installing @devcontainers/cli..."
    npm install -g @devcontainers/cli || die "Failed to install @devcontainers/cli. Ensure npm is available."
    has_cmd devcontainer || die "devcontainer CLI not found after install."
}

resolve_container() {
    docker ps -a --filter "label=claude-bunker=${CONTAINER_NAME}" --format '{{.Names}}' | head -1
}

container_exists() {
    docker ps -a --filter "label=claude-bunker=${CONTAINER_NAME}" --format '{{.Names}}' | grep -q .
}

container_running() {
    docker ps --filter "label=claude-bunker=${CONTAINER_NAME}" --format '{{.Names}}' | grep -q .
}

# ---------------------------------------------------------------------------
# Project config (.claude-bunker.json)
# ---------------------------------------------------------------------------
BUNKER_CONFIG="$WORKSPACE/.claude-bunker.json"
BUNKER_WORKSPACE=""
BUNKER_EXCLUDE="[]"
BUNKER_ALLOW_DOMAINS=""
_CONFIG_READ=false

read_project_config() {
    if $_CONFIG_READ; then return; fi
    _CONFIG_READ=true
    if [ ! -f "$BUNKER_CONFIG" ]; then return; fi

    local parsed
    parsed=$(node -e '
        const c = JSON.parse(require("fs").readFileSync(0, "utf8"));
        console.log(c.workspace || "");
        console.log(JSON.stringify(c.exclude || []));
        console.log((c.allowDomains || []).join(","));
    ' < "$BUNKER_CONFIG" 2>/dev/null) || { warn "Failed to parse .claude-bunker.json"; return; }

    BUNKER_WORKSPACE=$(echo "$parsed" | sed -n '1p')
    BUNKER_EXCLUDE=$(echo "$parsed" | sed -n '2p')
    BUNKER_ALLOW_DOMAINS=$(echo "$parsed" | sed -n '3p')
}

has_config_overrides() {
    [ -n "$BUNKER_WORKSPACE" ] || [ "$BUNKER_EXCLUDE" != "[]" ] || [ -n "$BUNKER_ALLOW_DOMAINS" ]
}

generate_config() {
    local tmp_config
    tmp_config=$(mktemp "${TMPDIR:-/tmp}/claude-bunker-XXXXXX.json")

    BASE_JSON=$(cat "$DEVCONTAINER_DIR/devcontainer.json") \
    WS_SUB="$BUNKER_WORKSPACE" \
    EXCLUDES="$BUNKER_EXCLUDE" \
    DOMAINS="$BUNKER_ALLOW_DOMAINS" \
    node -e '
        const config = JSON.parse(process.env.BASE_JSON);
        const sub = process.env.WS_SUB || "";
        if (sub && sub !== ".") {
            config.workspaceFolder = "/workspace/" + sub.replace(/^\.\//, "");
        }
        const excludes = JSON.parse(process.env.EXCLUDES || "[]");
        for (const p of excludes) {
            const clean = p.replace(/^\.\//, "").replace(/\/$/, "");
            config.mounts = config.mounts || [];
            config.mounts.push("type=tmpfs,destination=/workspace/" + clean);
        }
        const domains = process.env.DOMAINS || "";
        if (domains) {
            config.containerEnv = config.containerEnv || {};
            config.containerEnv.CLAUDE_BUNKER_EXTRA_DOMAINS = domains;
        }
        process.stdout.write(JSON.stringify(config, null, 2) + "\n");
    ' > "$tmp_config" || { rm -f "$tmp_config"; die "Failed to generate config"; }

    echo "$tmp_config"
}

effective_workdir() {
    if [ -n "$BUNKER_WORKSPACE" ] && [ "$BUNKER_WORKSPACE" != "." ]; then
        echo "/workspace/$(echo "$BUNKER_WORKSPACE" | sed 's|^\./||')"
    else
        echo "/workspace"
    fi
}

# ---------------------------------------------------------------------------
# Config fingerprinting — detect when a rebuild is needed
# ---------------------------------------------------------------------------
config_fingerprint() {
    cat "$DEVCONTAINER_DIR"/* "$BUNKER_CONFIG" 2>/dev/null | \
    node -e "
        const c = require('crypto'); let d = '';
        process.stdin.on('data', b => d += b);
        process.stdin.on('end', () => console.log(c.createHash('sha256').update(d).digest('hex')));
    "
}

fingerprint_matches() {
    local fp_file="$CACHE_DIR/${CONTAINER_NAME}.fp"
    [ -f "$fp_file" ] && [ "$(cat "$fp_file")" = "$(config_fingerprint)" ]
}

save_fingerprint() {
    mkdir -p "$CACHE_DIR"
    config_fingerprint > "$CACHE_DIR/${CONTAINER_NAME}.fp"
}

# ---------------------------------------------------------------------------
# Container lifecycle
# ---------------------------------------------------------------------------
_STARTED_BY_US=false
_TMP_CONFIG=""

ensure_container() {
    read_project_config

    # Fast path: container is already running with current config
    if container_running; then
        if fingerprint_matches; then
            return 0
        fi
        # Config changed — tear down stale container and recreate
        info "Configuration changed — rebuilding sandbox..."
        local cname
        cname=$(resolve_container)
        docker stop "$cname" >/dev/null 2>&1 || true
        docker rm "$cname" >/dev/null 2>&1 || true
    fi

    # Remove stale stopped container
    if container_exists; then
        docker rm "$(resolve_container)" >/dev/null 2>&1 || true
    fi

    _STARTED_BY_US=true

    # Generate config with overrides if .claude-bunker.json has any
    local config_file="$DEVCONTAINER_DIR/devcontainer.json"
    if has_config_overrides; then
        _TMP_CONFIG=$(generate_config)
        config_file="$_TMP_CONFIG"
    fi

    # Decide UX: first build (verbose) vs quick start (quiet)
    if fingerprint_matches; then
        # Image is cached, config unchanged — quick start
        info "Starting sandbox..."
        local log_file
        log_file=$(mktemp "${TMPDIR:-/tmp}/claude-bunker-log-XXXXXX")
        if ! devcontainer up \
            --workspace-folder "$WORKSPACE" \
            --override-config "$config_file" \
            --id-label "claude-bunker=${CONTAINER_NAME}" \
            > "$log_file" 2>&1; then
            echo "" >&2
            cat "$log_file" >&2
            rm -f "$log_file"
            die "Failed to start sandbox."
        fi
        rm -f "$log_file"
    else
        # First build or config changed — show progress
        info "Building sandbox..."
        if ! devcontainer up \
            --workspace-folder "$WORKSPACE" \
            --override-config "$config_file" \
            --id-label "claude-bunker=${CONTAINER_NAME}"; then
            die "Failed to build sandbox."
        fi
    fi

    save_fingerprint
}

cleanup() {
    set +e  # Don't exit on errors during cleanup
    trap - EXIT INT TERM HUP

    # Clean up temp config file
    [ -n "${_TMP_CONFIG:-}" ] && rm -f "$_TMP_CONFIG"

    # Only tear down if we started the container
    if ! ${_STARTED_BY_US:-false}; then return; fi

    local cname
    cname=$(resolve_container 2>/dev/null) || return

    # Don't tear down if other user sessions are still active in the container.
    # Look for interactive processes (claude/zsh) — NOT pgrep -cu node, which
    # would match the container's own init loop (sleep) and never tear down.
    if docker exec "$cname" pgrep -f "claude|zsh" >/dev/null 2>&1; then
        return
    fi

    docker stop "$cname" >/dev/null 2>&1 || true
    docker rm "$cname" >/dev/null 2>&1 || true
}

# ---------------------------------------------------------------------------
# Commands
# ---------------------------------------------------------------------------

# Default: run Claude Code inside the sandbox
cmd_default() {
    require_docker
    require_devcontainer
    trap cleanup EXIT INT TERM HUP

    ensure_container

    local cname
    cname=$(resolve_container)
    local workdir
    workdir=$(effective_workdir)

    local rc=0
    docker exec -it -u node -w "$workdir" "$cname" claude "$@" || rc=$?

    exit $rc
}

# Open a shell in the sandbox (for debugging, manual work)
cmd_shell() {
    require_docker
    require_devcontainer
    trap cleanup EXIT INT TERM HUP

    ensure_container

    local cname
    cname=$(resolve_container)
    local workdir
    workdir=$(effective_workdir)

    docker exec -it -u node -w "$workdir" "$cname" zsh
}

cmd_prune() {
    require_docker

    local volumes
    volumes=$(docker volume ls --filter "name=claude-bunker-" --format '{{.Name}}' 2>/dev/null || true)

    if [ -z "$volumes" ]; then
        info "No claude-bunker volumes found."
        return 0
    fi

    info "Found claude-bunker volumes:"
    echo "$volumes" | sed 's/^/  /'

    echo ""
    read -rp "[claude-bunker] Remove all of the above volumes? [y/N] " answer
    case "$answer" in
        [yY]|[yY][eE][sS])
            echo "$volumes" | while read -r vol; do
                docker volume rm "$vol" >/dev/null 2>&1 \
                    && info "Removed: $vol" \
                    || warn "Could not remove $vol (may be in use)."
            done
            info "Prune complete."
            ;;
        *)
            info "Aborted."
            ;;
    esac
}

cmd_help() {
    cat <<'HELP'
claude-bunker — Run Claude Code in a sandboxed container.

USAGE
    claude-bunker [flags]        Run Claude (all flags pass through)
    claude-bunker shell          Open a shell in the sandbox
    claude-bunker prune          Remove orphaned Docker volumes
    claude-bunker help           Show this message

  The sandbox starts automatically and tears down when Claude exits.
  On first run the container image is built (takes a few minutes, cached
  after that). Configuration changes are detected automatically.

PROJECT CONFIG (.claude-bunker.json)
    Place in your project root to customize per-project:

        {
            "workspace": "./packages/backend",
            "exclude": ["secrets/", ".env.production"],
            "allowDomains": ["private-registry.company.com"]
        }

    workspace      Subdirectory to use as working directory.
    exclude        Paths hidden inside the container via tmpfs overlays.
    allowDomains   Extra domains added to the firewall allowlist.

ENVIRONMENT
    CLAUDE_BUNKER_DIR   Path to the claude-bunker repo (default: script dir).
    CLAUDE_BUNKER_WS    Workspace to mount (default: $PWD).
    TZ                  Container timezone (default: America/Los_Angeles).

AUTHENTICATION
    On first launch Claude will prompt you to log in. Credentials are
    stored in a Docker volume and persist across sessions.

EXAMPLES
    claude-bunker                                 # Interactive session
    claude-bunker --dangerously-skip-permissions  # Skip prompts (safe)
    claude-bunker -p "fix the failing tests"      # Direct prompt
    claude-bunker shell                           # Debugging shell
HELP
}

# ---------------------------------------------------------------------------
# Main — treat everything as Claude flags unless it's a known subcommand
# ---------------------------------------------------------------------------
main() {
    case "${1:-}" in
        shell)       shift; cmd_shell "$@" ;;
        prune)       shift; cmd_prune "$@" ;;
        help|-h|--help) cmd_help ;;
        *)           cmd_default "$@" ;;
    esac
}

main "$@"
