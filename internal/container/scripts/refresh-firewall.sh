#!/bin/bash
set -euo pipefail
IFS=$'\n\t'

# Periodic firewall refresh daemon.
#
# Re-resolves allowed domains every INTERVAL seconds and atomically swaps
# the ipset, so CDN and cloud IP rotations (e.g. Google's proxy.golang.org)
# don't break connections after the initial container startup.
#
# Usage: refresh-firewall.sh [domains-file] [interval-seconds]
# Started by claude-bunker as a detached background process after init-firewall.sh.

# Source shared DNS resolution and ipset helpers.
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
. "$SCRIPT_DIR/firewall-common.sh"

DOMAINS_FILE="${1:-/tmp/.bunker-domains}"
INTERVAL="${2:-300}"   # Default: 5 minutes

[[ "$INTERVAL" =~ ^[0-9]+$ ]] || { echo "Invalid interval: $INTERVAL" >&2; exit 1; }

IPSET_LIVE="$IPSET_NAME"
IPSET_TMP="${IPSET_NAME}-new"

REFRESH_LOG="/tmp/refresh-firewall.log"

refresh() {
    # Create temp set, or flush if it lingered from a previous failed run.
    ipset create "$IPSET_TMP" hash:net 2>/dev/null || ipset flush "$IPSET_TMP"

    local count=0
    while IFS= read -r domain || [ -n "$domain" ]; do
        domain="${domain%$'\r'}"
        [ -z "$domain" ] && continue
        # Strip leading '!' critical marker before resolution.
        domain="${domain#!}"
        [ -z "$domain" ] && continue

        if add_ips_to_set "$domain" "$IPSET_TMP" 0; then
            count=$((count + 1))
        fi
    done < "$DOMAINS_FILE"

    # Safety: if DNS completely failed for all domains, keep the existing live
    # set rather than swapping in an empty set (which would kill all allowed traffic).
    if [ "$count" -eq 0 ]; then
        echo "WARNING: No domains resolved, skipping swap" >&2
        ipset destroy "$IPSET_TMP" 2>/dev/null || true
        return 1
    fi

    # Safety: verify the new ipset actually contains IP entries, not just that
    # DNS calls returned without error.
    local ip_count
    ip_count=$(ipset list "$IPSET_TMP" 2>/dev/null | grep -c "^[0-9]" || echo 0)
    if [[ "$ip_count" -eq 0 ]]; then
        echo "WARNING: No IPs in new ipset, skipping swap" >&2
        ipset destroy "$IPSET_TMP" 2>/dev/null || true
        return 1
    fi

    # Atomic swap: the live set instantly gets the freshly resolved IPs.
    # No window where allowed traffic is briefly blocked.
    ipset swap "$IPSET_TMP" "$IPSET_LIVE"
    # Destroy the now-stale entries (in the tmp slot after the swap).
    ipset destroy "$IPSET_TMP"
}

while true; do
    sleep "$INTERVAL"
    # Truncate log if it exceeds 1 MB to avoid filling disk.
    if [ -f "$REFRESH_LOG" ] && [ "$(wc -c < "$REFRESH_LOG")" -gt 1048576 ]; then
        : > "$REFRESH_LOG"
    fi
    refresh 2>>"$REFRESH_LOG" || true
done
