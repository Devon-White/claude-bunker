#!/bin/bash
set -euo pipefail
IFS=$'\n\t'

# Abort the entire script if it runs longer than 60 seconds.
if [ "${_FW_TIMEOUT_SET:-}" != "1" ]; then
    export _FW_TIMEOUT_SET=1
    exec timeout 60 "$0" "$@"
fi

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

# Allow outbound DNS (matches upstream Claude Code devcontainer).
# The IP allowlist already prevents connections to unauthorized destinations,
# making destination-restricted DNS unnecessary in a devcontainer context.
iptables -A OUTPUT -p udp --dport 53 -j ACCEPT
iptables -A OUTPUT -p tcp --dport 53 -j ACCEPT

# Allow localhost
iptables -A INPUT -i lo -j ACCEPT
iptables -A OUTPUT -o lo -j ACCEPT

# Get host IP from default route
HOST_IP=$(ip route | grep default | cut -d" " -f3)
if [ -z "$HOST_IP" ]; then
    echo "ERROR: Failed to detect host IP"
    exit 1
fi
HOST_NETWORK=$(echo "$HOST_IP" | sed "s/\.[0-9]*$/.0\/24/")
echo "Host network detected as: $HOST_NETWORK"

# Allow host network (needed for Docker API and host-mapped services)
iptables -A INPUT -s "$HOST_NETWORK" -j ACCEPT
iptables -A OUTPUT -d "$HOST_NETWORK" -j ACCEPT

# Allow established/related connections
iptables -A INPUT -m state --state ESTABLISHED,RELATED -j ACCEPT
iptables -A OUTPUT -m state --state ESTABLISHED,RELATED -j ACCEPT

# Temporarily allow outbound HTTPS for bootstrap (dig needs it for some setups)
# This rule will be REMOVED after the allowlist is populated.
iptables -A OUTPUT -p tcp --dport 443 -j ACCEPT

# NOW set default policies to DROP
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
# resolve_and_allow: Resolves a domain to IPs and adds iptables rules.
# Retries once on failure. Non-critical domains warn and continue;
# critical domains (api.anthropic.com) cause a fatal exit.
# ---------------------------------------------------------------------------
resolve_and_allow() {
    local domain="$1"
    local critical="${2:-0}"
    local attempt ips

    for attempt in 1 2; do
        ips=$(dig +noall +answer +tries=2 +time=3 A "$domain" 2>/dev/null | awk '$4 == "A" {print $5}' || true)
        if [ -n "$ips" ]; then
            while read -r ip; do
                if [[ "$ip" =~ ^[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}$ ]]; then
                    echo "Allowing $ip for $domain"
                    iptables -A OUTPUT -d "$ip" -j ACCEPT
                fi
            done <<< "$ips"
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
# Populate the allowlist by resolving each domain sequentially.
# The domain list path is passed as $1 by the Go binary (one domain per
# line, builtin + user extras). This keeps Go as the single source of truth.
# ---------------------------------------------------------------------------

CRITICAL_DOMAIN="api.anthropic.com"

# The domains file path is passed as $1 by the Go binary, keeping Go as the
# single source of truth. Fall back to the conventional path for manual runs.
DOMAINS_FILE="${1:-/tmp/.bunker-domains}"
if [ ! -f "$DOMAINS_FILE" ]; then
    echo "FATAL: $DOMAINS_FILE not found"
    exit 1
fi

DOMAIN_COUNT=0
while IFS= read -r domain || [ -n "$domain" ]; do
    # Strip trailing \r (windows line endings) — Go already trims whitespace,
    # so no subprocess needed for general whitespace stripping.
    domain="${domain%$'\r'}"
    [ -z "$domain" ] && continue
    DOMAIN_COUNT=$((DOMAIN_COUNT + 1))
    if [ "$domain" = "$CRITICAL_DOMAIN" ]; then
        resolve_and_allow "$domain" 1
    else
        resolve_and_allow "$domain" 0
    fi
done < "$DOMAINS_FILE"
echo "Resolved $DOMAIN_COUNT domains"

# Remove the temporary bootstrap HTTPS rule — no longer needed
iptables -D OUTPUT -p tcp --dport 443 -j ACCEPT

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
if curl --connect-timeout 2 --max-time 3 https://example.com >/dev/null 2>&1; then
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
