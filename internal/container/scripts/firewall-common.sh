#!/bin/bash
# firewall-common.sh — shared functions for init-firewall.sh and refresh-firewall.sh.
# Sourced (not executed directly) by both scripts.
# NOTE: This file must be sourced by a script with set -euo pipefail

# Canonical ipset name used by both initial setup and periodic refresh.
# Uses hash:net to support /24 subnet entries for CDN IP rotation resilience.
IPSET_NAME="bunker-allowed"

# resolve_domain: Resolves a domain to A-record IPv4 addresses.
# Outputs one IP per line on stdout. Empty output means resolution failed.
resolve_domain() {
    local domain="$1"
    {
        dig +noall +answer +tries=2 +time=3 A "$domain" 2>/dev/null | awk '$4 == "A" {print $5}'
        getent ahostsv4 "$domain" 2>/dev/null | awk '{print $1}'
    } | sort -u
}

# is_ipv4: Returns 0 if the argument is a valid IPv4 address (each octet 0-255).
is_ipv4() {
    local IFS='.'
    local -a octets
    read -ra octets <<< "$1"
    [[ ${#octets[@]} -eq 4 ]] || return 1
    local o
    for o in "${octets[@]}"; do
        [[ "$o" =~ ^[0-9]+$ ]] || return 1
        (( o >= 0 && o <= 255 )) || return 1
    done
    return 0
}

# ip_to_24: Converts an IPv4 address to its /24 CIDR block.
# e.g. 140.82.112.4 → 140.82.112.0/24
ip_to_24() {
    echo "${1%.*}.0/24"
}

# add_ips_to_set: Resolves a domain and adds /24 subnets to the named ipset.
# If the input is already an IPv4 address, adds its /24 subnet directly.
# Using /24 subnets instead of individual IPs handles CDN round-robin DNS
# where the same domain may resolve to different IPs in the same subnet
# at different times (e.g. GitHub, Statsig, Sentry).
# Outputs "Allowing <subnet> for <domain>" when verbose=1.
# Returns 0 if at least one subnet was added, 1 otherwise.
add_ips_to_set() {
    local domain="$1" setname="$2" verbose="${3:-0}"
    local ips count=0

    # If already an IPv4 address, add its /24 subnet directly
    if is_ipv4 "$domain"; then
        local subnet
        subnet=$(ip_to_24 "$domain")
        [ "$verbose" = "1" ] && echo "Allowing $subnet (from direct IP $domain)"
        ipset add "$setname" "$subnet" 2>/dev/null || true
        return 0
    fi

    ips=$(resolve_domain "$domain" || true)
    [ -z "$ips" ] && return 1

    while read -r ip; do
        if is_ipv4 "$ip"; then
            local subnet
            subnet=$(ip_to_24 "$ip")
            [ "$verbose" = "1" ] && echo "Allowing $subnet for $domain"
            ipset add "$setname" "$subnet" 2>/dev/null || true
            count=$((count + 1))
        fi
    done <<< "$ips"

    [ "$count" -gt 0 ] && return 0 || return 1
}
