#!/bin/bash
set -euo pipefail  # Exit on error, undefined vars, and pipeline failures
IFS=$'\n\t'       # Stricter word splitting

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
# anything below fails (curl, dig, aggregate), the container is locked down
# rather than left wide open.
# ---------------------------------------------------------------------------

# Allow Docker embedded DNS (127.0.0.11) so DNS resolution works under DROP
iptables -A OUTPUT -d 127.0.0.11/32 -p udp --dport 53 -j ACCEPT
iptables -A OUTPUT -d 127.0.0.11/32 -p tcp --dport 53 -j ACCEPT

# Allow outbound DNS (matches official — works with any Docker DNS config)
iptables -A OUTPUT -p udp --dport 53 -j ACCEPT

# Allow localhost
iptables -A INPUT -i lo -j ACCEPT
iptables -A OUTPUT -o lo -j ACCEPT

# Get host IP from default route (no network needed, just reads routing table)
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

# Allow established/related connections (so responses to our curl/dig come back)
iptables -A INPUT -m state --state ESTABLISHED,RELATED -j ACCEPT
iptables -A OUTPUT -m state --state ESTABLISHED,RELATED -j ACCEPT

# Temporarily allow outbound HTTPS for bootstrap (curl to api.github.com, dig)
# This rule will be REMOVED after the allowlist is populated.
iptables -A OUTPUT -p tcp --dport 443 -j ACCEPT

# NOW set default policies to DROP — any failure below leaves us locked down
iptables -P INPUT DROP
iptables -P FORWARD DROP
iptables -P OUTPUT DROP

# Sanity check: verify policies took effect immediately
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
    echo "ip6tables not available (kernel module missing) — IPv6 already disabled by Docker"
fi

# ---------------------------------------------------------------------------
# resolve_domain: Resolves a domain to IPs with retry logic.
# Returns IPs via stdout, one per line. Returns 0 on success, 1 on failure.
# DNS can be flaky in fresh containers (Docker DNS warmup, SERVFAIL on CDN
# domains, etc.), so we retry up to 2 times with a short delay.
#
# Timeouts are kept tight (+time=2, +tries=2) because domains resolve in
# parallel — we don't want one failing domain to hold up `wait` for 30s+.
# Worst-case per domain: 2 attempts × (2 tries × 2s) + 1s sleep = ~9s.
# ---------------------------------------------------------------------------
resolve_domain() {
    local domain="$1"
    local attempt
    for attempt in 1 2; do
        local result
        # || true prevents set -e from killing the script if dig fails
        result=$(dig +noall +answer +tries=2 +time=2 A "$domain" 2>/dev/null | awk '$4 == "A" {print $5}' || true)
        if [ -n "$result" ]; then
            echo "$result"
            return 0
        fi
        if [ "$attempt" -lt 2 ]; then
            sleep 1
        fi
    done
    return 1
}

# ---------------------------------------------------------------------------
# Populate the allowlist using individual iptables rules.
# This avoids the ipset/xt_set kernel module dependency, which can fail on
# first container start in Docker Desktop (WSL2) when the module isn't loaded.
# Failures here are safe — container stays locked down.
#
# OPTIMIZATION: All DNS resolution and the GitHub API call run in parallel.
# Previously these were sequential (~8 dig calls + 1 curl = 5-15s). Now
# they overlap, reducing wall-clock time to the slowest single resolution.
# ---------------------------------------------------------------------------

RESOLVE_DIR=$(mktemp -d /tmp/.fw-resolve.XXXXXX)
trap 'rm -rf "$RESOLVE_DIR"' EXIT

# --- Launch all network operations in parallel ---

# GitHub meta API (background)
echo "Fetching GitHub IP ranges..."
(curl -s https://api.github.com/meta > "$RESOLVE_DIR/github-meta" 2>/dev/null) &
GH_PID=$!

# Built-in domains (background)
BUILTIN_DOMAINS=(
    "registry.npmjs.org"
    "api.anthropic.com"
    "sentry.io"
    "statsig.anthropic.com"
    "statsig.com"
    "marketplace.visualstudio.com"
    "vscode.blob.core.windows.net"
    "update.code.visualstudio.com"
)

echo "Resolving ${#BUILTIN_DOMAINS[@]} domains in parallel..."
for domain in "${BUILTIN_DOMAINS[@]}"; do
    (resolve_domain "$domain" > "$RESOLVE_DIR/$domain" 2>/dev/null) &
done

# Extra domains from .claude-bunker.json (background)
EXTRA_DOMAINS=$(cat /tmp/.bunker-extra-domains 2>/dev/null || true)
EXTRA_DOMAIN_LIST=()
if [ -n "$EXTRA_DOMAINS" ]; then
    while IFS= read -r domain; do
        domain=$(echo "$domain" | tr -d '[:space:]')
        [ -z "$domain" ] && continue
        EXTRA_DOMAIN_LIST+=("$domain")
        echo "Resolving extra domain: $domain..."
        (resolve_domain "$domain" > "$RESOLVE_DIR/extra-$domain" 2>/dev/null) &
    done < <(echo "$EXTRA_DOMAINS" | tr ',' '\n')
fi

# --- Wait for all background jobs to complete ---
wait

# --- Process GitHub results ---
gh_ranges=$(cat "$RESOLVE_DIR/github-meta" 2>/dev/null || true)
if [ -z "$gh_ranges" ]; then
    echo "ERROR: Failed to fetch GitHub IP ranges"
    exit 1
fi

if ! echo "$gh_ranges" | jq -e '.web and .api and .git' >/dev/null; then
    echo "ERROR: GitHub API response missing required fields"
    exit 1
fi

echo "Processing GitHub IPs..."
while read -r cidr; do
    if [[ ! "$cidr" =~ ^[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}/[0-9]{1,2}$ ]]; then
        echo "ERROR: Invalid CIDR range from GitHub meta: $cidr"
        exit 1
    fi
    echo "Allowing GitHub range $cidr"
    iptables -A OUTPUT -d "$cidr" -j ACCEPT
done < <(echo "$gh_ranges" | jq -r '(.web + .api + .git)[]' | aggregate -q)

# ---------------------------------------------------------------------------
# DNS RESOLUTION LIMITATION: Domain IPs are resolved once at container start.
# CDN-backed services (Anthropic, npm, Sentry, etc.) rotate IPs over time.
# In long-running containers, resolved IPs may become stale, causing allowed
# connections to fail. This is defense-in-depth — Claude Code's sandbox layer
# performs domain-level filtering that survives IP rotation. If connections to
# allowed domains start failing, restart the container to re-resolve IPs.
#
# A DNS-aware proxy or periodic re-resolution cron could fix this, but the
# added complexity is not currently justified given the sandbox backup layer.
# ---------------------------------------------------------------------------

# --- Process built-in domain results ---
# api.anthropic.com is critical (Claude can't work without it).
# Others are non-critical — warn and continue if resolution fails.
CRITICAL_DOMAINS="api.anthropic.com"
FAILED_DOMAINS=""

for domain in "${BUILTIN_DOMAINS[@]}"; do
    ips=$(cat "$RESOLVE_DIR/$domain" 2>/dev/null || true)

    if [ -z "$ips" ]; then
        if echo "$CRITICAL_DOMAINS" | grep -qw "$domain"; then
            echo "ERROR: Failed to resolve critical domain $domain"
            exit 1
        fi
        echo "WARNING: Failed to resolve $domain (skipping — non-critical)"
        FAILED_DOMAINS="${FAILED_DOMAINS:+$FAILED_DOMAINS, }$domain"
        continue
    fi

    while read -r ip; do
        if [[ ! "$ip" =~ ^[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}$ ]]; then
            echo "ERROR: Invalid IP from DNS for $domain: $ip"
            exit 1
        fi
        echo "Allowing $ip for $domain"
        iptables -A OUTPUT -d "$ip" -j ACCEPT
    done < <(echo "$ips")
done

# --- Process extra domain results ---
for domain in "${EXTRA_DOMAIN_LIST[@]}"; do
    ips=$(cat "$RESOLVE_DIR/extra-$domain" 2>/dev/null || true)
    if [ -z "$ips" ]; then
        echo "WARNING: Failed to resolve extra domain $domain (skipping)"
        continue
    fi
    while read -r ip; do
        if [[ "$ip" =~ ^[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}$ ]]; then
            echo "Allowing $ip for $domain"
            iptables -A OUTPUT -d "$ip" -j ACCEPT
        fi
    done < <(echo "$ips")
done

# Remove the temporary bootstrap HTTPS rule — no longer needed
iptables -D OUTPUT -p tcp --dport 443 -j ACCEPT

# Explicitly REJECT all other outbound traffic for immediate feedback
iptables -A OUTPUT -j REJECT --reject-with icmp-admin-prohibited

echo "Firewall configuration complete"
if [ -n "$FAILED_DOMAINS" ]; then
    echo "NOTE: Some non-critical domains failed to resolve: $FAILED_DOMAINS"
    echo "These services won't be reachable. Restart container to retry."
fi

# ---------------------------------------------------------------------------
# Verification: fast iptables policy check (always) + curl probes (verbose only).
# The policy check is instant and confirms DROP is set. The curl probes add
# ~5-10s and are only useful for debugging — gated behind BUNKER_VERBOSE.
# ---------------------------------------------------------------------------
echo "Verifying firewall policies..."
# Capture output first, then grep — avoids pipefail causing false failures
# when iptables returns non-zero transiently (lock contention, stderr warnings).
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

    if ! curl --connect-timeout 5 https://api.github.com/zen >/dev/null 2>&1; then
        echo "ERROR: Firewall verification failed - unable to reach https://api.github.com"
        exit 1
    else
        echo "Verified: able to reach https://api.github.com (allowed)"
    fi
fi
