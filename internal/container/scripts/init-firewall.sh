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

# Allow outbound DNS to Docker's configured resolver(s) only.
# On Linux Docker Engine this is typically 127.0.0.11 (embedded DNS);
# on Docker Desktop (macOS/Windows) it's a gateway IP like 192.168.65.7.
# Restricting to only the configured resolvers prevents DNS tunneling
# exfiltration to attacker-controlled nameservers.
DNS_SERVERS=$(awk '/^nameserver/ {print $2}' /etc/resolv.conf)
if [ -z "$DNS_SERVERS" ]; then
    # Fallback to Docker's embedded DNS if resolv.conf is empty
    DNS_SERVERS="127.0.0.11"
fi
for dns in $DNS_SERVERS; do
    iptables -A OUTPUT -p udp -d "$dns" --dport 53 -j ACCEPT
    iptables -A OUTPUT -p tcp -d "$dns" --dport 53 -j ACCEPT
done

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

# The domains file path is passed as $1 by the Go binary, keeping Go as the
# single source of truth. Fall back to the conventional path for manual runs.
# Determined here (rather than at first use below) because the egress proxy
# launched next also needs it for its own --allowlist.
DOMAINS_FILE="${1:-/tmp/.bunker-domains}"
if [ ! -f "$DOMAINS_FILE" ]; then
    echo "FATAL: $DOMAINS_FILE not found"
    exit 1
fi

# ---------------------------------------------------------------------------
# SNI-aware egress proxy: start it, then transparently REDIRECT agent TCP/443
# to it. The proxy re-checks the allowlist by TLS SNI (closing the domain-
# fronting gap the ipset /24 tier cannot see) and, when a masking config is
# present, terminates the auth hosts to inject real credentials. The proxy's
# own egress (uid 1001) skips the REDIRECT and is filtered by the ipset tier.
#
# Skipped entirely when an upstream corporate proxy is configured (HTTPS_PROXY/
# https_proxy) — the agent's TLS traffic is already going through that proxy,
# so redirecting 443 into our own SNI proxy here would double-proxy it.
# ---------------------------------------------------------------------------
PROXY_BIN="/usr/local/bin/egress-proxy"
PROXY_PORT="15443"
PROXY_UID="1001"
MASKING_CONFIG="/etc/claude-bunker/proxy/masking.json"
PROXY_CA_DIR="/etc/claude-bunker/proxy/ca"
PROXY_CA_CERT="/etc/claude-bunker/proxy/ca/ca.pem"
SNI_PROXY_ACTIVE=0

if [ -z "${HTTPS_PROXY:-}${https_proxy:-}" ] && [ -x "$PROXY_BIN" ]; then
    mkdir -p "$PROXY_CA_DIR"
    chown -R "$PROXY_UID:$PROXY_UID" /etc/claude-bunker/proxy 2>/dev/null || true

    # Built as an array (not a space-joined string) because this script sets
    # IFS=$'\n\t' globally — unquoted expansion of a plain string would not
    # word-split on spaces and the proxy would receive one giant argument.
    PROXY_ARGS=(--listen "127.0.0.1:$PROXY_PORT" --allowlist "$DOMAINS_FILE" --ca-dir "$PROXY_CA_DIR")
    if [ -f "$MASKING_CONFIG" ]; then
        PROXY_ARGS+=(--masking "$MASKING_CONFIG")
    fi

    # Launch as the dedicated non-root proxy user.
    runuser -u bunker-proxy -- env nohup "$PROXY_BIN" "${PROXY_ARGS[@]}" >/tmp/egress-proxy.log 2>&1 &

    # Wait up to 5s for the listener.
    for _ in $(seq 1 50); do
        if (exec 3<>/dev/tcp/127.0.0.1/$PROXY_PORT) 2>/dev/null; then
            exec 3>&- 2>/dev/null || true
            break
        fi
        sleep 0.1
    done

    # If masking is active, install the proxy CA so terminated hosts are trusted.
    if [ -f "$MASKING_CONFIG" ] && [ -f "$PROXY_CA_CERT" ]; then
        cp "$PROXY_CA_CERT" /usr/local/share/ca-certificates/bunker-egress-ca.crt
        update-ca-certificates >/dev/null 2>&1 || true
    fi

    # Transparent REDIRECT: proxy's own egress (owner) returns; everything else
    # on 443 is redirected to the local proxy.
    iptables -t nat -A OUTPUT -p tcp --dport 443 -m owner --uid-owner 1001 -j RETURN
    iptables -t nat -A OUTPUT -p tcp --dport 443 -j REDIRECT --to-ports 15443
    SNI_PROXY_ACTIVE=1
else
    echo "egress proxy skipped (upstream proxy set or binary missing)"
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
# Populate the ipset by resolving domains from the file passed as $1.
# The Go binary writes one domain per line (builtin + user extras).
# Domains prefixed with '!' are critical; the prefix is stripped before
# resolution. Falls back to api.anthropic.com as the default critical domain.
#
# Non-critical domains are resolved in parallel (background jobs) to reduce
# startup latency. ipset add is safe to call concurrently — duplicates are
# silently ignored by the kernel. The critical domain is resolved first
# (sequentially) to fail fast on fundamental network issues.
# ---------------------------------------------------------------------------

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

# Phase 1: Resolve the critical domain sequentially (fail-fast).
resolve_and_allow "$CRITICAL_DOMAIN" 1

# Phase 2: Resolve all other domains in parallel using background jobs.
# ipset add is atomic and duplicate-safe, so concurrent adds are fine.
BG_PIDS=()
while IFS= read -r domain || [ -n "$domain" ]; do
    domain="${domain%$'\r'}"
    [ -z "$domain" ] && continue
    domain="${domain#!}"
    [ -z "$domain" ] && continue
    [ "$domain" = "$CRITICAL_DOMAIN" ] && continue
    resolve_and_allow "$domain" 0 &
    BG_PIDS+=($!)
done < "$DOMAINS_FILE"

# Wait for all background DNS resolutions to complete.
for pid in "${BG_PIDS[@]}"; do
    wait "$pid" 2>/dev/null || true
done

# Count resolved domains from the ipset (background jobs can't update shell vars).
RESOLVED_COUNT=$(ipset list "$IPSET_LIVE" 2>/dev/null | grep -c "^[0-9]" || echo 0)
echo "Resolved domains — $RESOLVED_COUNT subnet(s) in ipset"

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

# Verify that non-allowlisted traffic would be blocked by checking that
# example.com's IP is NOT in the allowed ipset. This is instant compared to
# the previous curl-based check that always waited for a timeout (~2s).
TEST_IP="93.184.216.34"  # example.com
if ipset test "$IPSET_NAME" "$TEST_IP" 2>/dev/null; then
    echo "ERROR: Firewall verification failed — example.com IP ($TEST_IP) is in the allowed set"
    exit 1
else
    echo "Verified: non-allowlisted traffic is blocked (example.com IP not in allowed set)"
fi

if [ "${BUNKER_VERBOSE:-0}" = "1" ]; then
    echo "Running verbose network verification..."
    if curl --connect-timeout 2 https://api.anthropic.com >/dev/null 2>&1; then
        echo "Verified: api.anthropic.com is reachable"
    else
        echo "WARNING: api.anthropic.com is not reachable (may be a transient DNS issue)"
    fi
fi

# ---------------------------------------------------------------------------
# SNI self-test: prove the proxy blocks a non-allowlisted SNI even when the
# destination IP is inside an allowlisted /24. Force-resolve a bogus hostname
# onto an allowlisted IP; the proxy must refuse it (curl fails). Only runs
# when the SNI proxy tier was actually brought up above (skipped when an
# upstream proxy was configured or the binary is missing).
# ---------------------------------------------------------------------------
if [ "$SNI_PROXY_ACTIVE" = "1" ] && [ -x "$PROXY_BIN" ]; then
    ALLOWED_IP=$(ipset list "$IPSET_NAME" 2>/dev/null | awk '/^[0-9]/{print $1; exit}' | cut -d/ -f1) || true
    if [ -n "$ALLOWED_IP" ]; then
        if curl --connect-timeout 3 --max-time 5 --resolve "fronting.invalid:443:$ALLOWED_IP" \
            https://fronting.invalid/ >/dev/null 2>&1; then
            echo "ERROR: SNI self-test failed — domain-fronting to $ALLOWED_IP was NOT blocked"
            exit 1
        fi
        echo "Verified: domain-fronting blocked by the SNI proxy"
    fi
fi
