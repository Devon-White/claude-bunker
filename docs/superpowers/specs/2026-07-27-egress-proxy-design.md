# SNI-Aware Egress Proxy + Credential Masking — Design

**Date:** 2026-07-27
**Status:** Design (awaiting user review before planning)
**Supersedes/extends:** the L3/L4 iptables/ipset firewall (`init-firewall.sh`) — this adds an L7 SNI/hostname tier on top; the ipset tier stays as the fail-closed backstop.

## Problem

The container's egress firewall (`internal/container/scripts/init-firewall.sh`) is **L3/L4 only**: it resolves allowed domains to IPs and allows them via an ipset. Two consequences:

1. **Domain-fronting / CDN-IP exfil.** ipset can't see the TLS SNI. Worse, the ipset stores **`/24` subnets** (CDN round-robin resilience), so *any host in an allowlisted `/24`* is reachable at L3. A prompt-injected agent can open a TLS connection to an allowlisted CDN IP but send SNI for an attacker host co-located on that CDN → data leaves. Claude Code's own default firewall shares this exact gap; [Anthropic's sandboxing docs](https://code.claude.com/docs/en/sandboxing) name it explicitly and offer a TLS-terminating proxy as the fix.

2. **The agent holds real credentials.** Today `InjectAuthSecrets` writes the real `api_key` / `oauth_token` / `gh_token` to `/run/secrets/` **owned by the agent uid (1000)** and the auth wrapper exports them into Claude Code's env. A compromised agent can read the real tokens and — even with egress locked to allowlisted hosts — leak them *through* an allowed channel (post them to api.anthropic.com in a message, push a gist to github.com, etc.).

## Goal

Add a hostname-aware egress proxy that (**Tier 1**) allowlists by SNI and fails closed, closing domain-fronting; and (**Tier 2**) terminates TLS for the 1–3 credential hosts and swaps a per-session **sentinel** for the real secret, so the real credential never exists at the agent's privilege level.

## Threat model & what each tier closes

| Threat | Before | Tier 1 | Tier 2 |
|---|---|---|---|
| Egress to a non-allowlisted host by IP | blocked (ipset) | blocked | blocked |
| Egress to a non-allowlisted host **fronted on an allowlisted CDN IP** | **open** | **blocked** (SNI mismatch → connection closed) | blocked |
| Agent reads real token from its own env/tmpfs | **open** | open | **closed** (agent only ever has a sentinel) |
| Agent leaks token *through* an allowlisted host | **open** | open | **closed** (sentinel is worthless off-host; real value injected only proxy-side) |
| Plain-HTTP `:80` fronting within an allowlisted `/24` | open | open (documented residual) | open (documented residual) |

## Architecture

```
             ┌──────────────────────── container ────────────────────────┐
 agent       │  agent (uid 1000)  ──TCP :443──▶ [ nat REDIRECT ]          │
 (Claude     │      env: ANTHROPIC_API_KEY = <sentinel>                   │
  Code)      │                                    │                       │
             │                                    ▼                       │
             │                        egress-proxy (uid bunker-proxy)     │
             │                        · reads ClientHello SNI             │
             │             ┌──────────┼───────────────────────┐          │
             │   SNI ∉ allowlist   SNI ∈ allowlist,     SNI ∈ allowlist,  │
             │        │            not an auth host      an auth host      │
             │        ▼                 │                     │           │
             │   close (fail-       splice (raw TLS       TERMINATE:      │
             │    closed)            byte relay, no       leaf cert from  │
             │                       plaintext seen)      per-container CA│
             │                          │                  swap sentinel→ │
             │                          │                  real secret,   │
             │                          │                  re-encrypt     │
             │                          ▼                     ▼           │
             │              proxy egress (owner-match RETURN, skips        │
             │              REDIRECT) ──▶ filter OUTPUT ──▶ ipset ACCEPT   │
             └──────────────────────────┼────────────────────────────────┘
                                        ▼
                              real host (api.anthropic.com / github.com / CDN)
```

**Key invariant:** the proxy's *own* outbound connections still traverse the existing ipset L3 filter. The two tiers are complementary — ipset stops non-TLS/other-port egress and pins destinations to allowlisted `/24`s; the SNI tier stops same-IP-different-host abuse for the TLS traffic ipset must let through by IP.

### Component: `internal/egressproxy/` (new Go package) + `cmd/egressproxyd/` (thin main)

Stdlib-only (`crypto/tls`, `crypto/x509`, `net`, `net/http`, `bufio`) — the compiled binary has **zero external dependencies** (static, `CGO_ENABLED=0`). Kept in the main module so it's unit-tested by the normal `go test ./...` suite. Compiled **for the container arch via a multi-stage Docker build** (`FROM golang AS proxybuild` → `COPY` the module source → `go mod download` (image build has host network) → `CGO_ENABLED=0 go build ./cmd/egressproxyd` → final stage `COPY --from=proxybuild` the static binary). The `golang` builder stage is discarded from the final image.

Files (boundaries; exact signatures are the plan's job):
- `allowlist.go` — load `allowed-domains.txt`, match an SNI against it (supports `*.host` wildcards via suffix match). Returns allow/deny.
- `sniread.go` — peek the TLS ClientHello, extract SNI without consuming the stream (buffered peek, then replay on splice).
- `splice.go` — raw bidirectional `io.Copy` between agent conn and a fresh upstream TCP conn to `SNI:443`. No decryption.
- `terminate.go` — for auth hosts: present a leaf cert (generated on demand, signed by the per-container CA, cached per host), read the HTTP/1.1 request, hand headers to `mask.go`, dial a real TLS conn to the upstream, forward, stream response.
- `mask.go` — given the masking config, replace sentinels with real secrets in the relevant auth headers: `x-api-key: <sentinel>` and `Authorization: Bearer <sentinel>` → plain value swap; `Authorization: Basic base64(user:<sentinel>)` (git PAT) → decode, swap password, re-encode.
- `ca.go` — generate/load the per-container CA and mint per-host leaf certs.
- `config.go` — proxy config: listen port, allowlist path, optional masking config path (absent ⇒ pure Tier 1), CA dir.

**Proxy runs as a dedicated non-root `bunker-proxy` uid** (privilege separation). Its config, CA private key, and the real-secret files are owned by `bunker-proxy` mode `0400`/`0600` — the agent (`claude-bunker` uid) cannot read them. The proxy needs no root.

### Enforcement: transparent iptables REDIRECT (in `init-firewall.sh`, runs as root)

Add to the **nat** table (runs before filter OUTPUT for locally-generated packets):
```
iptables -t nat -A OUTPUT -p tcp --dport 443 -m owner --uid-owner <bunker-proxy-uid> -j RETURN
iptables -t nat -A OUTPUT -p tcp --dport 443 -j REDIRECT --to-ports <PROXY_PORT>
```
- Agent TCP/443 → REDIRECT → `127.0.0.1:<PROXY_PORT>`. **Transparent, not env-based** — a compromised agent cannot bypass by unsetting `HTTPS_PROXY`.
- The proxy's egress (uid `bunker-proxy`) matches the RETURN rule → not redirected → hits filter OUTPUT → ipset ACCEPT.
- The agent cannot become `bunker-proxy` (non-root). Ordering: the proxy is started **before** the REDIRECT is applied; if the proxy is down, redirected 443 hits a dead port → connection refused → fail-closed.

### Config & single source of truth

- **SNI allowlist** = the *same* root-owned `allowed-domains.txt` (`container.AllowedDomainsPath`, baked `0444` in Phase 2c) the ipset already reads. One source; the agent can't widen it.
- **Masking config** (Tier 2, native only) = a `bunker-proxy`-owned file (e.g. `/run/secrets/proxy/masking.json`) mapping sentinel → real-secret-file + the auth hosts to terminate. Written by the bunker CLI at container setup. Absent in the portable path ⇒ proxy runs pure Tier 1 (everything allowlisted is spliced; nothing terminated; no CA needed).

### Tier 2 auth-flow rewrite (native path)

`InjectAuthSecrets` (and the re-inject path) change so that, **when masking is active**:
1. bunker generates a per-session **sentinel** per secret, format-preserving (`sk-ant-…`, `ghp_…`, opaque Bearer) so clients don't reject it on shape.
2. **Real** secrets are written to `bunker-proxy`-owned files (agent can't read).
3. **Sentinels** are written to the agent-readable `/run/secrets/` and exported by the auth wrapper exactly as today (`ANTHROPIC_API_KEY` / `CLAUDE_CODE_OAUTH_TOKEN` / `GITHUB_PERSONAL_ACCESS_TOKEN` and the git cred-helper password all become sentinels).
4. The masking config points each sentinel at its real-secret file + the host(s) where it may be injected.

**Why no token-refresh interception is needed:** bunker injects *static env-var credentials* (`ANTHROPIC_API_KEY`, `CLAUDE_CODE_OAUTH_TOKEN` from `claude setup-token`, a GitHub PAT) — not the refreshing `~/.claude/.credentials.json` OAuth session. Claude Code uses env-var creds as-is; there is no refresh POST to intercept. A plain header swap suffices.

### CA trust (Tier 2 only)

At startup (root, before starting the proxy as `bunker-proxy`):
1. Generate a per-container CA (ephemeral; new each container start) into a `bunker-proxy`-owned dir.
2. Install the CA **cert** into the system store: copy to `/usr/local/share/ca-certificates/bunker-egress-ca.crt` + `update-ca-certificates` (covers curl, git, python-`requests` via the system bundle).
3. Set `NODE_EXTRA_CA_CERTS=<ca.pem>` for Node/Claude Code (Node ignores the system store).
4. The proxy mints per-host leaf certs on demand, signed by the CA (key stays `bunker-proxy`-only, so the agent can't sign).

Plaintext is decrypted **only** for the 1–3 auth hosts; every other allowlisted host is spliced (no plaintext seen). New blast radius = those auth hosts only.

### Fail-closed & self-test (extends the existing `init-firewall.sh` verification)

After the proxy is up and rules are applied:
1. **Domain-fronting blocked (the assertion no existing project makes):** `curl --resolve fronting.invalid:443:<an-allowed-ip> https://fronting.invalid/` must **fail** — the SNI is not allowlisted, proxy closes the connection. Proves the new tier closes the gap the ipset tier cannot.
2. **Allowed splice reachable:** a real allowlisted non-auth host connects through the proxy.
3. **Terminate + CA works (Tier 2):** `curl https://api.anthropic.com` completes the TLS handshake against the bunker leaf cert (any HTTP status proves the terminate + trust path).

Masking correctness (sentinel→real swap, incl. Basic-auth decode) is covered by **Go unit tests** in `internal/egressproxy` against a fake upstream — it can't be self-tested in-container without a cooperating echo host.

### Interop & residuals

- **Upstream corporate proxy:** if `HTTPS_PROXY`/`HTTP_PROXY` is set (`sandbox/proxy.go`), egress already funnels through it. The SNI REDIRECT + masking **stand down** in that case (no double-proxy). Documented; the two are mutually exclusive.
- **Portable VS Code path = Tier 1 only.** No host-side broker exists to split real/sentinel, so the portable path runs the proxy with no masking config → SNI allowlist + splice + fail-closed + self-test (the domain-fronting fix, fully portable). Credentials there follow the platform's own model (VS Code/Codespaces secrets). Tier 2 masking is a bunker-CLI feature.
- **Plain HTTP `:80`** within an allowlisted `/24` still uses ipset only (Tier 1/2 target the TLS SNI gap). Documented residual; a future Host-header filter for `:80` could close it.
- **Non-HTTP TLS to auth hosts** (gRPC, websockets): terminate assumes HTTP/1.1 header semantics for the swap. Auth hosts here are REST/HTTPS; if a host needs opaque tunneling it belongs on the splice list, not the terminate list.

## Native vs portable — where each piece runs

| Piece | Native (bunker CLI) | Portable (VS Code) |
|---|---|---|
| Proxy binary baked in image (multi-stage) | ✅ | ✅ |
| SNI allowlist + splice + fail-closed + self-test | ✅ | ✅ |
| Transparent 443 REDIRECT | ✅ (Go-orchestrated firewall) | ✅ (committed `postStartCommand`) |
| TLS terminate + credential masking | ✅ | ❌ (no broker) |
| Sentinel auth-flow, real-secret split | ✅ (`InjectAuthSecrets`) | ❌ |
| Per-container CA + `update-ca-certificates` + `NODE_EXTRA_CA_CERTS` | ✅ (only when masking active) | ❌ |

The same proxy binary; mode is data-driven by presence of the masking config.

## Component/file inventory (for the plan)

**New:**
- `internal/egressproxy/{allowlist,sniread,splice,terminate,mask,ca,config}.go` + `*_test.go`
- `cmd/egressproxyd/main.go`

**Modified:**
- `internal/container/scripts/base.dockerfile.tmpl` — multi-stage proxy build stage; create `bunker-proxy` uid; `COPY --from` the binary; install its start hook.
- **Build context** (`internal/container/build.go` / `embed.go` / `cmd/genbuild`) — the in-memory build context must now carry the Go module source (`go.mod`, `go.sum`, `internal/egressproxy/`, `cmd/egressproxyd/`) so the multi-stage stage can compile it. This is the one structural change to how the image build context is assembled.
- `internal/container/scripts/init-firewall.sh` — nat REDIRECT + owner RETURN; extend self-test with the domain-fronting assertion.
- a proxy-start step in the firewall bootstrap (both paths) — start proxy as `bunker-proxy` before applying REDIRECT; Tier 2 CA setup when masking active.
- `internal/container/lifecycle.go` + `internal/container/constants.go` — `bunker-proxy` uid/paths, proxy port, masking-config path constants; wire proxy start into native run.
- `internal/container/lifecycle.go` `InjectAuthSecrets` / `writeSecretFiles` / `createAuthWrapper` — sentinel/real split when masking active; write masking config + real-secret files.
- `internal/devcontainer/generate.go` — portable postStart starts the proxy (Tier 1) alongside the firewall.
- `cmd/init.go` `writeDevContainer` — bake proxy build + start into the committed Dockerfile; `NODE_EXTRA_CA_CERTS`/containerEnv only if we later add portable terminate (not now).
- Fingerprint inputs — the proxy source/binary + masking toggle fold into the image/container fingerprint as appropriate.

## Testing strategy

- **Go unit tests** (`internal/egressproxy`): SNI parse (incl. wildcard, missing SNI), allowlist match/deny, splice round-trip against a fake TLS upstream, terminate + `mask.go` swap for `x-api-key` / `Bearer` / git `Basic` (sentinel replaced, non-matching values untouched), CA leaf-cert generation validates against the CA.
- **Script self-test**: the three in-container assertions above (domain-fronting blocked is the load-bearing one).
- **Existing suites** stay green; native-path behavior for users who pass no secrets is unchanged (no masking config ⇒ Tier 1 only, same as portable).

## Out of scope (future)

- Portable-path credential masking (needs a broker; likely a small always-on sidecar — separate design).
- `:80` Host-header filtering.
- Per-path/method allowlisting on terminated hosts (only credential injection now).
- SSRF deny-CIDR at the proxy (metadata `169.254.169.254`/RFC1918) — cheap orthogonal add; ipset already blocks these at L3, so deferred.
