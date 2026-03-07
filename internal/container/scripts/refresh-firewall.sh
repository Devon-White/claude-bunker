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
. "$(dirname "$0")/firewall-common.sh"

DOMAINS_FILE="${1:-/tmp/.bunker-domains}"
INTERVAL="${2:-300}"   # Default: 5 minutes

IPSET_LIVE="$IPSET_NAME"
IPSET_TMP="${IPSET_NAME}-new"

refresh() {
    # Create temp set, or flush if it lingered from a previous failed run.
    ipset create "$IPSET_TMP" hash:net 2>/dev/null || ipset flush "$IPSET_TMP"

    local count=0
    while IFS= read -r domain || [ -n "$domain" ]; do
        domain="${domain%$'\r'}"
        [ -z "$domain" ] && continue

        if add_ips_to_set "$domain" "$IPSET_TMP" 0; then
            count=$((count + 1))
        fi
    done < "$DOMAINS_FILE"

    # Safety: if DNS completely failed, keep the existing live set rather than
    # swapping in an empty set (which would kill all allowed traffic).
    if [ "$count" -eq 0 ]; then
        ipset destroy "$IPSET_TMP" 2>/dev/null || true
        return
    fi

    # Atomic swap: the live set instantly gets the freshly resolved IPs.
    # No window where allowed traffic is briefly blocked.
    ipset swap "$IPSET_TMP" "$IPSET_LIVE"
    # Destroy the now-stale entries (in the tmp slot after the swap).
    ipset destroy "$IPSET_TMP"
}

while true; do
    sleep "$INTERVAL"
    refresh 2>/dev/null || true
done
