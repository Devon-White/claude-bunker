#!/bin/bash
set -euo pipefail
IFS=$'\n\t'

# Abort the entire script if it runs longer than 60 seconds.
if [ "${_FW_TIMEOUT_SET:-}" != "1" ]; then
    export _FW_TIMEOUT_SET=1
    exec timeout 60 "$0" "$@"
fi

# Source shared DNS resolution and ipset helpers.
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
. "$SCRIPT_DIR/firewall-common.sh"

# Flush filter and mangle rules. Leave the nat table intact so Docker's
# embedded DNS routing (127.0.0.11 NAT interception) keeps working without
# the fragile save/grep/restore dance the upstream script requires.
iptables -F
iptables -X
iptables -t mangle -F
iptables -t mangle -X

# ---------------------------------------------------------------------------
# FAIL-CLOSED: set DROP policies BEFORE any network operations so that if
# anything below fails, the container is locked down rather than left open.
# ---------------------------------------------------------------------------
iptables -P INPUT DROP
iptables -P FORWARD DROP
iptables -P OUTPUT DROP

# Sanity check: verify policies took effect
SANITY_OUTPUT=$(iptables -S OUTPUT 2>/dev/null || true)
if ! echo "$SANITY_OUTPUT" | grep -q -- "-P OUTPUT DROP"; then
    echo "FATAL: iptables -P OUTPUT DROP did not take effect"
    echo "iptables version: $(iptables --version 2>&1)"
    echo "iptables -S OUTPUT: $SANITY_OUTPUT"
    exit 1
fi

# Allow outbound DNS to Docker's embedded resolver only (127.0.0.11).
# Restricting to the Docker resolver prevents DNS tunneling exfiltration to
# attacker-controlled nameservers while preserving all normal name resolution.
iptables -A OUTPUT -p udp -d 127.0.0.11 --dport 53 -j ACCEPT
iptables -A OUTPUT -p tcp -d 127.0.0.11 --dport 53 -j ACCEPT

# Allow localhost
iptables -A INPUT -i lo -j ACCEPT
iptables -A OUTPUT -o lo -j ACCEPT

# Get host IP from default route
HOST_IP=$(ip route show default | awk 'NR==1 {print $3}')
if [ -z "$HOST_IP" ]; then
    echo "ERROR: Failed to detect host IP"
    exit 1
fi
if ! is_ipv4 "$HOST_IP"; then
    echo "ERROR: HOST_IP '$HOST_IP' does not look like an IPv4 address"
    exit 1
fi
HOST_NETWORK=$(ip_to_24 "$HOST_IP")
echo "Host network detected as: $HOST_NETWORK"

# Allow host network (needed for Docker API and host-mapped services)
iptables -A INPUT -s "$HOST_NETWORK" -j ACCEPT
iptables -A OUTPUT -d "$HOST_NETWORK" -j ACCEPT

# Allow established/related connections
iptables -A INPUT -m state --state ESTABLISHED,RELATED -j ACCEPT
iptables -A OUTPUT -m state --state ESTABLISHED,RELATED -j ACCEPT

# ---------------------------------------------------------------------------
# ipset: create the allowed-IP set. A single iptables rule references it.
# Populated via DNS resolution below; atomically refreshed by the companion
# refresh-firewall.sh daemon to handle CDN/cloud IP rotation.
# ---------------------------------------------------------------------------
IPSET_LIVE="$IPSET_NAME"
ipset create "$IPSET_LIVE" hash:net 2>/dev/null || ipset flush "$IPSET_LIVE"
iptables -A OUTPUT -m set --match-set "$IPSET_LIVE" dst -j ACCEPT

# ---------------------------------------------------------------------------
# IPv6: default-deny immediately to prevent bypassing the IPv4 firewall
# ---------------------------------------------------------------------------
echo "Configuring IPv6 firewall (default-deny)..."
if ip6tables -L -n >/dev/null 2>&1; then
    ip6tables -F 2>/dev/null || true
    ip6tables -X 2>/dev/null || true
    ip6tables -A INPUT  -i lo -j ACCEPT
    ip6tables -A OUTPUT -o lo -j ACCEPT
    ip6tables -P INPUT   DROP
    ip6tables -P FORWARD DROP
    ip6tables -P OUTPUT  DROP
else
    echo "ip6tables not available — IPv6 already disabled by Docker"
fi

# ---------------------------------------------------------------------------
# resolve_and_allow: Resolves a domain to IPs and adds them to the ipset.
# Retries once on failure. Non-critical domains warn and continue;
# critical domains (api.anthropic.com) cause a fatal exit.
# ---------------------------------------------------------------------------
resolve_and_allow() {
    local domain="$1"
    local critical="${2:-0}"
    local attempt

    for attempt in 1 2; do
        if add_ips_to_set "$domain" "$IPSET_LIVE" 1; then
            return 0
        fi
        [ "$attempt" -lt 2 ] && sleep 1
    done

    if [ "$critical" = "1" ]; then
        echo "ERROR: Failed to resolve critical domain $domain"
        exit 1
    fi
    echo "WARNING: Failed to resolve $domain (skipping — non-critical)"
    return 0
}

# ---------------------------------------------------------------------------
# Populate the ipset by resolving each domain sequentially.
# The domain list path is passed as $1 by the Go binary (one domain per
# line, builtin + user extras). This keeps Go as the single source of truth.
# Domains prefixed with '!' are treated as critical; the prefix is stripped
# before resolution. Falls back to api.anthropic.com as the default critical
# domain if no '!'-prefixed domain is found in the file.
# ---------------------------------------------------------------------------

# The domains file path is passed as $1 by the Go binary, keeping Go as the
# single source of truth. Fall back to the conventional path for manual runs.
DOMAINS_FILE="${1:-/tmp/.bunker-domains}"
if [ ! -f "$DOMAINS_FILE" ]; then
    echo "FATAL: $DOMAINS_FILE not found"
    exit 1
fi

# Determine critical domain: first '!'-prefixed entry in the domains file,
# or api.anthropic.com as a safe fallback.
CRITICAL_DOMAIN="api.anthropic.com"
while IFS= read -r _line || [ -n "$_line" ]; do
    _line="${_line%$'\r'}"
    if [[ "$_line" == "!"* ]]; then
        CRITICAL_DOMAIN="${_line#!}"
        break
    fi
done < "$DOMAINS_FILE"

DOMAIN_COUNT=0
while IFS= read -r domain || [ -n "$domain" ]; do
    # Strip trailing \r (windows line endings) — Go already trims whitespace,
    # so no subprocess needed for general whitespace stripping.
    domain="${domain%$'\r'}"
    [ -z "$domain" ] && continue
    # Strip leading '!' critical marker before resolution.
    domain="${domain#!}"
    [ -z "$domain" ] && continue
    DOMAIN_COUNT=$((DOMAIN_COUNT + 1))
    if [ "$domain" = "$CRITICAL_DOMAIN" ]; then
        resolve_and_allow "$domain" 1
    else
        resolve_and_allow "$domain" 0
    fi
done < "$DOMAINS_FILE"
echo "Resolved $DOMAIN_COUNT domains"

# Explicitly REJECT all other outbound traffic for immediate feedback
iptables -A OUTPUT -j REJECT --reject-with icmp-admin-prohibited

echo "Firewall configuration complete"

# ---------------------------------------------------------------------------
# Verification: confirm DROP policy is set.
# ---------------------------------------------------------------------------
echo "Verifying firewall policies..."
VERIFY_OUTPUT=$(iptables -S OUTPUT 2>/dev/null || true)
if ! echo "$VERIFY_OUTPUT" | grep -q -- "-P OUTPUT DROP"; then
    echo "ERROR: OUTPUT chain default policy is not DROP"
    echo "DEBUG: iptables -S OUTPUT:"
    echo "$VERIFY_OUTPUT"
    exit 1
fi
echo "Firewall verification passed — OUTPUT policy is DROP"

# Verify that non-allowlisted traffic is actually blocked.
# This runs in all modes to ensure the firewall is working correctly.
if curl --connect-timeout 5 --max-time 3 https://example.com >/dev/null 2>&1; then
    echo "ERROR: Firewall verification failed - was able to reach https://example.com"
    exit 1
else
    echo "Verified: non-allowlisted traffic is blocked"
fi

if [ "${BUNKER_VERBOSE:-0}" = "1" ]; then
    echo "Running verbose network verification..."
    if curl --connect-timeout 5 https://api.anthropic.com >/dev/null 2>&1; then
        echo "Verified: api.anthropic.com is reachable"
    else
        echo "WARNING: api.anthropic.com is not reachable (may be a transient DNS issue)"
    fi
fi
