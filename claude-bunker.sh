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

# Convert to native paths on MSYS2/Git Bash so devcontainer CLI and Docker
# receive proper Windows paths instead of /c/Users/... which gets double-mangled.
if [[ "${OSTYPE:-}" == msys* ]] || [[ "${MSYSTEM:-}" != "" ]]; then
    WORKSPACE="$(cygpath -m "$WORKSPACE" 2>/dev/null || echo "$WORKSPACE")"
    DEVCONTAINER_DIR="$(cygpath -m "$DEVCONTAINER_DIR" 2>/dev/null || echo "$DEVCONTAINER_DIR")"
fi

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

is_msys() {
    [[ "${OSTYPE:-}" == msys* ]] || [[ "${MSYSTEM:-}" != "" ]]
}

docker_exec_it() {
    # MSYS_NO_PATHCONV prevents Git Bash from mangling container paths
    # (e.g., -w /workspace → -w C:\workspace which breaks docker exec)
    if is_msys; then
        # VS Code provides its own PTY (conpty) — winpty would double-wrap
        # and garble arguments. Only use winpty in standalone terminals (mintty).
        if has_cmd winpty && [ "${TERM_PROGRAM:-}" != "vscode" ]; then
            MSYS_NO_PATHCONV=1 winpty docker exec -it "$@"
        else
            MSYS_NO_PATHCONV=1 docker exec -it "$@"
        fi
    else
        docker exec -it "$@"
    fi
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

generate_config() {
    local tmp_dir
    tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/claude-bunker-XXXXXX")
    local tmp_config="$tmp_dir/devcontainer.json"

    BASE_JSON=$(cat "$DEVCONTAINER_DIR/devcontainer.json") \
    BUILD_CONTEXT="$DEVCONTAINER_DIR" \
    CONTAINER_ID="$CONTAINER_NAME" \
    WS_SUB="$BUNKER_WORKSPACE" \
    EXCLUDES="$BUNKER_EXCLUDE" \
    DOMAINS="$BUNKER_ALLOW_DOMAINS" \
    node -e '
        const config = JSON.parse(process.env.BASE_JSON);

        // The generated config lives in /tmp, so relative paths would resolve
        // there instead of .devcontainer/. Fix by making both absolute.
        const ctx = process.env.BUILD_CONTEXT;
        config.build = config.build || {};
        config.build.context = ctx;
        const df = config.build.dockerfile || "Dockerfile";
        config.build.dockerfile = ctx + "/" + df.replace(/^\.\//, "");

        // Normalize volume names to use CONTAINER_NAME instead of ${devcontainerId}.
        // devcontainerId is a hash of the workspace path, which differs across
        // Git Bash, WSL, and VS Code — causing separate volumes (and repeated
        // logins) for the same project. CONTAINER_NAME is derived from the
        // directory basename, which is consistent everywhere.
        const cid = process.env.CONTAINER_ID;
        if (cid && config.mounts) {
            config.mounts = config.mounts.map(m =>
                typeof m === "string"
                    ? m.replace(/\$\{devcontainerId\}/g, cid)
                    : m
            );
        }

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
        // Isolate /workspace/.claude so writes never reach the bind-mounted host dir
        config.mounts = config.mounts || [];
        config.mounts.push("type=tmpfs,destination=/workspace/.claude");

        const domains = process.env.DOMAINS || "";
        if (domains) {
            config.containerEnv = config.containerEnv || {};
            config.containerEnv.CLAUDE_BUNKER_EXTRA_DOMAINS = domains;
        }
        process.stdout.write(JSON.stringify(config, null, 2) + "\n");
    ' > "$tmp_config" || { rm -rf "$tmp_dir"; die "Failed to generate config"; }

    echo "$tmp_config"
}

effective_workdir() {
    local dir="/workspace"
    if [ -n "$BUNKER_WORKSPACE" ] && [ "$BUNKER_WORKSPACE" != "." ]; then
        dir="/workspace/$(echo "$BUNKER_WORKSPACE" | sed 's|^\./||')"
    fi
    # Double leading slash prevents MSYS2 from mangling the container path.
    # Linux/Docker treats //workspace identically to /workspace.
    if is_msys; then echo "/$dir"; else echo "$dir"; fi
}

# ---------------------------------------------------------------------------
# Seed .claude settings into the container's isolated tmpfs
# ---------------------------------------------------------------------------
seed_claude_settings() {
    local cname="$1"
    local host_claude_dir="$WORKSPACE/.claude"

    # Copy host's .claude/*.json into the container's tmpfs overlay
    if [ -d "$host_claude_dir" ]; then
        for f in "$host_claude_dir"/*.json; do
            [ -f "$f" ] || continue
            local basename
            basename=$(basename "$f")
            # docker cp needs MSYS2-safe paths — $f is already native from
            # WORKSPACE conversion above; container path needs // prefix on MSYS2
            local dest
            if is_msys; then
                dest="$cname://workspace/.claude/$basename"
            else
                dest="$cname:/workspace/.claude/$basename"
            fi
            docker cp "$f" "$dest" 2>/dev/null || true
        done
    fi

    # Layer sandbox defaults on top (creates settings.json if missing)
    # MSYS_NO_PATHCONV prevents Git Bash from mangling the container path
    MSYS_NO_PATHCONV=1 docker exec -u node "$cname" /usr/local/bin/inject-sandbox-defaults.sh 2>/dev/null || true
}

# ---------------------------------------------------------------------------
# Config fingerprinting — detect when a rebuild is needed
# ---------------------------------------------------------------------------
config_fingerprint() {
    # cat returns non-zero if BUNKER_CONFIG doesn't exist (most projects).
    # Without || true, set -o pipefail would kill the script silently.
    { cat "$DEVCONTAINER_DIR"/* "$BUNKER_CONFIG" 2>/dev/null || true; } | \
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

    # Always generate config to normalize volume names across terminal types
    # (Git Bash, WSL, VS Code all produce different workspace paths, which
    # devcontainerId hashes differently — causing separate volumes per terminal).
    _TMP_CONFIG=$(generate_config)
    local config_file="$_TMP_CONFIG"

    # Decide UX: first build (verbose) vs quick start (quiet)
    if fingerprint_matches; then
        # Image is cached, config unchanged — quick start
        info "Starting sandbox..."
        local log_file
        log_file=$(mktemp "${TMPDIR:-/tmp}/claude-bunker-log-XXXXXX")
        if ! devcontainer up \
            --workspace-folder "$WORKSPACE" \
            --config "$config_file" \
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
            --config "$config_file" \
            --id-label "claude-bunker=${CONTAINER_NAME}"; then
            die "Failed to build sandbox."
        fi
    fi

    save_fingerprint
}

cleanup() {
    set +e  # Don't exit on errors during cleanup
    trap - EXIT INT TERM HUP

    # Clean up temp config directory
    [ -n "${_TMP_CONFIG:-}" ] && rm -rf "$(dirname "$_TMP_CONFIG")"

    local cname
    cname=$(resolve_container 2>/dev/null) || return

    # Don't tear down if other user sessions are still active in the container.
    # Look for interactive processes (claude/zsh) — NOT pgrep -cu node, which
    # would match the container's own init loop (sleep) and never tear down.
    if docker exec "$cname" pgrep -f "claude|zsh" >/dev/null 2>&1; then
        return
    fi

    info "Stopping sandbox..."
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

    if [ -z "$cname" ]; then
        die "Could not find container after startup."
    fi

    if ! container_running; then
        die "Container exited unexpectedly after startup."
    fi

    seed_claude_settings "$cname"

    # Verify claude is installed in the container
    if ! docker exec "$cname" which claude >/dev/null 2>&1; then
        die "Claude CLI not found in container."
    fi

    info "Launching Claude..."
    local rc=0
    docker_exec_it -u node -w "$workdir" "$cname" claude "$@" || rc=$?

    if [ $rc -ne 0 ] && [ $rc -ne 130 ]; then
        warn "Claude exited with code $rc"
    fi

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

    seed_claude_settings "$cname"

    info "Opening shell..."
    docker_exec_it -u node -w "$workdir" "$cname" zsh
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
