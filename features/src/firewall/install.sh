#!/bin/sh
# claude-bunker firewall — Dev Container Feature install script.
#
# Installs a default-deny egress firewall (iptables + ipset) that mirrors
# claude-bunker's native Docker firewall. The packaged scripts
# (firewall-common.sh, init-firewall.sh, refresh-firewall.sh) and
# builtin-domains.txt are generated verbatim from claude-bunker's canonical
# sources (internal/container) by `go run ./cmd/genfeatures` — see
# features/firewall_drift_test.go, which fails CI if they ever diverge.
#
# Runs as root at image build time. Feature options are exposed as
# UPPERCASED env vars (per the Dev Container Feature spec): the
# "allowDomains" option arrives here as $ALLOWDOMAINS.
set -e

DEST=/usr/local/share/claude-bunker-firewall

# --- Build-time packages ----------------------------------------------------
# iptables/ipset/iproute2: firewall + ipset management.
# dnsutils: provides `dig`, used by firewall-common.sh's resolve_domain
#   (falls back to `getent` if dig is unavailable).
# curl: used by init-firewall.sh's optional verbose network self-test.
apt-get update
apt-get install -y --no-install-recommends iptables ipset dnsutils iproute2 curl

# Use the legacy iptables backend — nf_tables isn't reliably available in
# unprivileged/rootless container runtimes and this matches claude-bunker's
# native image.
update-alternatives --set iptables /usr/sbin/iptables-legacy 2>/dev/null || true

# --- Install the canonical firewall scripts + builtin domain allowlist -----
mkdir -p "$DEST"
cp firewall-common.sh init-firewall.sh refresh-firewall.sh builtin-domains.txt "$DEST/"
chmod +x "$DEST"/firewall-common.sh "$DEST"/init-firewall.sh "$DEST"/refresh-firewall.sh

# --- run-firewall.sh: assemble the domains file (builtins + allowDomains --
# --- option), then run init-firewall.sh and background the refresh daemon.
# Invoked every container start via postStartCommand (devcontainer-feature.json).
cat > "$DEST/run-firewall.sh" <<'EOF'
#!/bin/bash
set -euo pipefail
DEST=/usr/local/share/claude-bunker-firewall
DOMAINS=/run/claude-bunker-domains

# Feature option env vars (e.g. ALLOWDOMAINS) only exist during install.sh at
# build time, not at postStart, so the value was persisted to a file at
# build time (see allow-domains.env below) and is sourced here instead.
if [ -f "$DEST/allow-domains.env" ]; then
    . "$DEST/allow-domains.env"
fi

cp "$DEST/builtin-domains.txt" "$DOMAINS"
if [ -n "${ALLOWDOMAINS:-}" ]; then
    echo "$ALLOWDOMAINS" | tr ',' '\n' | sed '/^$/d' >> "$DOMAINS"
fi

# Lock the domains file down so a prompt-injected process running inside the
# container cannot append domains to bypass the allowlist later — the
# refresh daemon re-reads this file on every cycle. Mirrors claude-bunker's
# native firewall setup (see internal/container/lifecycle.go RunPostStart).
chown root:root "$DOMAINS"
chmod 444 "$DOMAINS"

"$DEST/init-firewall.sh" "$DOMAINS"

# Launch the refresh daemon (backgrounded) so CDN/cloud IP rotation doesn't
# break connections after the initial resolution.
nohup "$DEST/refresh-firewall.sh" "$DOMAINS" >/dev/null 2>&1 &
EOF
chmod +x "$DEST/run-firewall.sh"

# Persist the allowDomains option value so run-firewall.sh (postStart) can
# read it — build-time option env vars are not present at postStart.
echo "ALLOWDOMAINS='${ALLOWDOMAINS:-}'" > "$DEST/allow-domains.env"
chmod 0644 "$DEST/allow-domains.env"

# --- sudo for postStartCommand ----------------------------------------------
# postStartCommand invokes run-firewall.sh via `sudo`: NET_ADMIN/NET_RAW
# operations require root, and the devcontainer's remoteUser is typically
# non-root. _REMOTE_USER is a special variable the Dev Container CLI exposes
# to every Feature's install.sh for exactly this purpose. Grant a scoped,
# passwordless sudoers entry for this script only — not blanket root access.
REMOTE_USER="${_REMOTE_USER:-root}"
if [ "$REMOTE_USER" != "root" ]; then
    if ! command -v sudo >/dev/null 2>&1; then
        apt-get install -y --no-install-recommends sudo
    fi
    if id -u "$REMOTE_USER" >/dev/null 2>&1; then
        echo "$REMOTE_USER ALL=(root) NOPASSWD: $DEST/run-firewall.sh" > /etc/sudoers.d/claude-bunker-firewall
        chmod 0440 /etc/sudoers.d/claude-bunker-firewall
    fi
fi

apt-get clean
rm -rf /var/lib/apt/lists/*
