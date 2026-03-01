#!/bin/bash
set -euo pipefail
IFS=$'\n\t'

# Abort the entire script if it runs longer than 60 seconds.
if [ "${_FW_TIMEOUT_SET:-}" != "1" ]; then
    export _FW_TIMEOUT_SET=1
    exec timeout 60 "$0" "$@"
fi

# 1. Extract Docker DNS info BEFORE any flushing
DOCKER_DNS_RULES=$(iptables-save -t nat | grep "127\.0\.0\.11" || true)

# Flush existing rules
iptables -F
iptables -X
iptables -t nat -F
iptables -t nat -X
iptables -t mangle -F
iptables -t mangle -X

# 2. Selectively restore ONLY internal Docker DNS resolution
if [ -n "$DOCKER_DNS_RULES" ]; then
    echo "Restoring Docker DNS rules..."
    iptables -t nat -N DOCKER_OUTPUT 2>/dev/null || true
    iptables -t nat -N DOCKER_POSTROUTING 2>/dev/null || true
    echo "$DOCKER_DNS_RULES" | xargs -L 1 iptables -t nat
else
    echo "No Docker DNS rules to restore"
fi

# ---------------------------------------------------------------------------
# FAIL-CLOSED: set DROP policies BEFORE any network operations so that if
# anything below fails, the container is locked down rather than left open.
# ---------------------------------------------------------------------------

# Allow Docker embedded DNS (127.0.0.11) so DNS resolution works under DROP
iptables -A OUTPUT -d 127.0.0.11/32 -p udp --dport 53 -j ACCEPT
iptables -A OUTPUT -d 127.0.0.11/32 -p tcp --dport 53 -j ACCEPT

# Allow outbound DNS
iptables -A OUTPUT -p udp --dport 53 -j ACCEPT

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
# ---------------------------------------------------------------------------

BUILTIN_DOMAINS=(
    "github.com"
    "api.github.com"
    "registry.npmjs.org"
    "api.anthropic.com"
    "sentry.io"
    "statsig.anthropic.com"
    "statsig.com"
    "marketplace.visualstudio.com"
    "vscode.blob.core.windows.net"
    "update.code.visualstudio.com"
)

echo "Resolving ${#BUILTIN_DOMAINS[@]} domains..."

# api.anthropic.com is critical — failure is fatal
for domain in "${BUILTIN_DOMAINS[@]}"; do
    if [ "$domain" = "api.anthropic.com" ]; then
        resolve_and_allow "$domain" 1
    else
        resolve_and_allow "$domain" 0
    fi
done

# Extra domains from .claude-bunker config
EXTRA_DOMAINS=$(cat /tmp/.bunker-extra-domains 2>/dev/null || true)
if [ -n "$EXTRA_DOMAINS" ]; then
    while IFS= read -r domain; do
        domain=$(echo "$domain" | tr -d '[:space:]')
        [ -z "$domain" ] && continue
        echo "Resolving extra domain: $domain..."
        resolve_and_allow "$domain" 0
    done < <(echo "$EXTRA_DOMAINS" | tr ',' '\n')
fi

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

if [ "${BUNKER_VERBOSE:-0}" = "1" ]; then
    echo "Running verbose network verification..."
    if curl --connect-timeout 5 https://example.com >/dev/null 2>&1; then
        echo "ERROR: Firewall verification failed - was able to reach https://example.com"
        exit 1
    else
        echo "Verified: unable to reach https://example.com (blocked)"
    fi
fi
