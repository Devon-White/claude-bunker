#!/bin/bash
# firewall-common.sh — shared functions for init-firewall.sh and refresh-firewall.sh.
# Sourced (not executed directly) by both scripts.

# Canonical ipset name used by both initial setup and periodic refresh.
IPSET_NAME="bunker-allowed"

# resolve_domain: Resolves a domain to A-record IPv4 addresses.
# Outputs one IP per line on stdout. Empty output means resolution failed.
resolve_domain() {
    dig +noall +answer +tries=2 +time=3 A "$1" 2>/dev/null | awk '$4 == "A" {print $5}'
}

# is_ipv4: Returns 0 if the argument looks like an IPv4 address.
is_ipv4() {
    [[ "$1" =~ ^[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}$ ]]
}

# add_ips_to_set: Resolves a domain and adds valid IPs to the named ipset.
# Outputs "Allowing <ip> for <domain>" when verbose=1.
# Returns 0 if at least one IP was added, 1 otherwise.
add_ips_to_set() {
    local domain="$1" setname="$2" verbose="${3:-0}"
    local ips count=0

    ips=$(resolve_domain "$domain" || true)
    [ -z "$ips" ] && return 1

    while read -r ip; do
        if is_ipv4 "$ip"; then
            [ "$verbose" = "1" ] && echo "Allowing $ip for $domain"
            ipset add "$setname" "$ip" 2>/dev/null || true
            count=$((count + 1))
        fi
    done <<< "$ips"

    [ "$count" -gt 0 ] && return 0 || return 1
}
