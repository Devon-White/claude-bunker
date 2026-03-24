#!/bin/sh
# bunker-hook.sh — Claude Code hook dispatcher for claude-bunker.
#
# This script is invoked by Claude Code for lifecycle events (Stop, SessionStart,
# SessionEnd, SubagentStart, SubagentStop). It signals the host-side watcher via
# a Unix domain socket on the workspace bind mount, enabling event-driven session
# monitoring without polling.
#
# The script receives JSON on stdin from Claude Code's hook system. It extracts
# the event name, session ID, and (for Stop events) the current custom-title
# from the session JSONL file, then sends a compact JSON payload to the socket.
#
# Design constraints:
#   - Uses grep/sed for JSON parsing (no jq dependency)
#   - socat for socket communication (installed in base image)
#   - Always exits 0 — hook failures must never break Claude Code
#   - Async-safe: called with async: true for Stop/SubagentStart/SubagentStop
#
# WORKAROUND: Title reading from JSONL is a workaround for the lack of an
# official Claude Code session rename API. See:
#   https://github.com/anthropics/claude-code/issues/32150
#   https://github.com/anthropics/claude-code/issues/33165
# When Claude Code ships a proper title API, the title extraction logic in
# the Stop handler can be removed.

set -e

SOCKET="/workspace/.bunker.sock"

# Read all stdin (Claude Code pipes JSON).
INPUT="$(cat)"

# Extract fields using lightweight JSON parsing.
# These are simple top-level string values so regex is reliable.
EVENT=$(printf '%s' "$INPUT" | grep -o '"hook_event_name"[[:space:]]*:[[:space:]]*"[^"]*"' | head -1 | sed 's/.*:[[:space:]]*"\([^"]*\)".*/\1/')
SESSION_ID=$(printf '%s' "$INPUT" | grep -o '"session_id"[[:space:]]*:[[:space:]]*"[^"]*"' | head -1 | sed 's/.*:[[:space:]]*"\([^"]*\)".*/\1/')

# Bail silently if socket doesn't exist (host watcher not running).
if [ ! -S "$SOCKET" ]; then
    exit 0
fi

# Container hostname is the Docker container ID (truncated).
CONTAINER_ID=$(hostname)

# Per-event handling.
TITLE=""
case "$EVENT" in
    Stop)
        # On Stop, read the current custom-title from the session JSONL tail.
        # This catches titles set by Claude Code's /rename command.
        # We scan the last 64KB (matching Claude Code's own scanner window).
        TRANSCRIPT_PATH=$(printf '%s' "$INPUT" | grep -o '"transcript_path"[[:space:]]*:[[:space:]]*"[^"]*"' | head -1 | sed 's/.*:[[:space:]]*"\([^"]*\)".*/\1/')
        if [ -n "$TRANSCRIPT_PATH" ] && [ -f "$TRANSCRIPT_PATH" ]; then
            TITLE=$(tail -c 65536 "$TRANSCRIPT_PATH" 2>/dev/null \
                | grep '"type":"custom-title"' \
                | tail -1 \
                | grep -o '"customTitle"[[:space:]]*:[[:space:]]*"[^"]*"' \
                | sed 's/.*:[[:space:]]*"\([^"]*\)".*/\1/' \
                || true)
        fi
        ;;
    SessionStart|SessionEnd|SubagentStart|SubagentStop)
        # Signal only — watcher will FetchSnapshot to get full state.
        ;;
    *)
        # Unknown event — still signal for safety.
        ;;
esac

# Escape JSON-special characters in the title to prevent malformed payloads.
# Backslashes first (to avoid double-escaping), then double-quotes.
SAFE_TITLE=$(printf '%s' "$TITLE" | sed 's/\\/\\\\/g; s/"/\\"/g' | tr -d '\n')

# Send event payload to the host watcher via Unix socket.
# socat with -t1 (1-second timeout) prevents blocking Claude Code
# if the host listener is slow or dead.
PAYLOAD="{\"event\":\"${EVENT}\",\"session_id\":\"${SESSION_ID}\",\"title\":\"${SAFE_TITLE}\",\"container_id\":\"${CONTAINER_ID}\"}"
printf '%s\n' "$PAYLOAD" | socat -t1 - UNIX-CONNECT:"$SOCKET" 2>/dev/null || true

exit 0
