#!/bin/bash
# Injects sandbox defaults into the project's .claude/settings.json if the
# "sandbox" key is not already present. This ensures Claude Code's inner
# sandbox layer is enabled when running inside the container.
#
# Only touches .claude/settings.json (shared project settings, meant to be
# committed). Never modifies .claude/settings.local.json (personal overrides).
# Claude Code's precedence: local > project > user — so developers can always
# override these defaults via their settings.local.json.
set -euo pipefail

SETTINGS_FILE="/workspace/.claude/settings.json"

# Base allowed domains for Claude Code's sandbox
BASE_DOMAINS='["api.anthropic.com","statsig.anthropic.com","statsig.com","sentry.io","github.com","*.github.com","registry.npmjs.org"]'

# Include extra domains from .claude-bunker.json if present
ALLOWED_DOMAINS="$BASE_DOMAINS"
EXTRA_DOMAINS=$(cat /tmp/.bunker-extra-domains 2>/dev/null || true)
if [ -n "$EXTRA_DOMAINS" ]; then
    ALLOWED_DOMAINS=$(echo "$EXTRA_DOMAINS" | tr ',' '\n' | while IFS= read -r d; do
        d=$(echo "$d" | tr -d '[:space:]')
        [ -n "$d" ] && echo "$d"
    done | jq -R . | jq -s --argjson base "$BASE_DOMAINS" '$base + .')
fi

# Build sandbox defaults JSON with the (possibly extended) domain list
SANDBOX_DEFAULTS=$(jq -n --argjson domains "$ALLOWED_DOMAINS" '{
  sandbox: {
    enabled: true,
    enableWeakerNestedSandbox: true,
    network: {
      allowedDomains: $domains
    }
  }
}')

mkdir -p "$(dirname "$SETTINGS_FILE")"

if [ ! -f "$SETTINGS_FILE" ]; then
    echo "[sandbox] Creating $SETTINGS_FILE with sandbox defaults..."
    echo "$SANDBOX_DEFAULTS" | jq . > "$SETTINGS_FILE"
elif ! jq -e '.sandbox' "$SETTINGS_FILE" >/dev/null 2>&1; then
    echo "[sandbox] Injecting sandbox defaults into existing $SETTINGS_FILE..."
    jq --argjson defaults "$SANDBOX_DEFAULTS" '. + $defaults' "$SETTINGS_FILE" > "${SETTINGS_FILE}.tmp"
    mv "${SETTINGS_FILE}.tmp" "$SETTINGS_FILE"
else
    echo "[sandbox] Sandbox settings already present, skipping injection."
fi
