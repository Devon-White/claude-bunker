# claude-bunker firewall

## id: `firewall`

A default-deny egress firewall (iptables + ipset) with a domain allowlist,
IPv6 blocked, and a startup self-test. This Feature ports **claude-bunker's
native firewall** to the standard Dev Container Feature spec, so the same
protection is available on the VS Code / Codespaces path — not just when
running through the `claude-bunker` CLI directly.

## Single source of truth

The three scripts in this directory (`firewall-common.sh`, `init-firewall.sh`,
`refresh-firewall.sh`) and `builtin-domains.txt` are **not hand-maintained**.
They are generated verbatim from claude-bunker's canonical sources —
`internal/container/scripts/*.sh` and `container.BuiltinDomains()` — by:

```bash
go run ./cmd/genfeatures
# or: go generate ./...
```

`features/firewall_drift_test.go` asserts the packaged copies are
byte-identical to the canonical sources and fails CI if they ever diverge.
This means the portable Feature can never silently fall behind, or become
weaker than, bunker's native Docker firewall — a change to the real firewall
logic always has to flow through the generator before this package is
considered up to date.

Do not edit the four generated files directly; edit
`internal/container/scripts/*.sh` or `internal/container/domains.go` instead
and regenerate.

## What it does

- **Build time** (`install.sh`, runs as root): installs `iptables`, `ipset`,
  `dnsutils` (for `dig`), `iproute2`, and `curl`; switches to the
  `iptables-legacy` backend; copies the firewall scripts and the builtin
  domain allowlist into `/usr/local/share/claude-bunker-firewall/`; generates
  a `run-firewall.sh` wrapper; and grants a scoped, passwordless `sudo` entry
  for that wrapper to the container's non-root remote user (if any).
- **Every container start** (`postStartCommand`, runs via `sudo`):
  `run-firewall.sh` assembles the domains file (builtin allowlist plus the
  `allowDomains` option), locks it down (`chown root:root`, `chmod 444`) so a
  prompt-injected process cannot append to it later, runs
  `init-firewall.sh` to set default-deny iptables/ip6tables policies and
  allow only the resolved domains, then backgrounds `refresh-firewall.sh` to
  re-resolve domains every 5 minutes (handles CDN/cloud IP rotation).

## Options

| Option | Type | Default | Description |
|---|---|---|---|
| `allowDomains` | string | `""` | Comma-separated extra domains to allow (added to the built-in allowlist). |

Example:

```jsonc
"features": {
  "ghcr.io/Devon-White/claude-bunker/firewall:1": {
    "allowDomains": "registry.npmjs.org,pypi.org"
  }
}
```

## capAdd rationale

This Feature declares:

```json
"capAdd": ["NET_ADMIN", "NET_RAW"]
```

- `NET_ADMIN` is required to modify iptables/ip6tables rules and manage
  ipset sets.
- `NET_RAW` is required for some iptables match extensions and for `ping`
  during troubleshooting.

Both are strictly narrower than `--privileged`, and neither grants access
beyond network configuration inside the container's own network namespace.

## Requirements

- A remote user with (or granted) passwordless `sudo` for the firewall
  script — `install.sh` sets this up itself via `_REMOTE_USER` if the base
  image doesn't already provide it.
- `installsAfter: ["ghcr.io/devcontainers/features/common-utils"]` so, when
  present, `common-utils`'s user/sudo setup runs first.

## Relationship to claude-bunker's native firewall

When you run a project through the `claude-bunker` CLI directly (Docker
path), the firewall is set up natively by `internal/container` — this
Feature is not involved. This Feature exists purely for the OCI Dev
Container Feature / VS Code / GitHub Codespaces path, where a project wants
the same egress protection without going through the `claude-bunker` binary.
Both paths are driven by the exact same scripts and domain list.
