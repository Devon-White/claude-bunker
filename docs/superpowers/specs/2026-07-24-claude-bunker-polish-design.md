# claude-bunker Polish & Dev Container Adoption — Design

**Date:** 2026-07-24
**Status:** Draft for review (revised after adversarial spec review + `claude agents` spike)
**Author:** Devon + Claude (brainstorming session)

## 1. Purpose

claude-bunker runs Claude Code inside a hardened Docker container so a prompt-injected
agent cannot exfiltrate credentials or damage the host. The engine is competent, but a
large session-management subsystem and the container convention grew without best-practice
guardrails, and a March 2026 audit's findings were mostly never applied. This redesign makes
it a **polished, professional CLI** that **adopts the Dev Container spec** for portability
instead of a purely private convention, **fixes the session/rename bugs** by rebasing on
Claude Code's now-official session surface, hardens the security layers to **fail closed**,
and brings CI, tests, and docs up to standard.

Breaking changes are acceptable — the tool has a single user (the author) today, so there is
**no backward-compatibility or migration contract**. `claude-bunker init` writes the new
layout directly; the legacy `.claude/.claude-bunker/config.json` is simply dropped (no
`migrate` command).

## 2. Goals

1. **Dev Container adoption for portability.** claude-bunker generates a `.devcontainer/` so a
   bunkered repo also opens in VS Code / Codespaces / JetBrains with equivalent hardening, and
   can read an existing `devcontainer.json`. No manual dev-container authoring is required.
2. **Reliable session management.** Session identity and names come from Claude Code's official
   surface — verified in this design to exist: `claude agents --json` (authoritative session
   list with user-set names) and `claude -n <name>` (launch-time naming). The `titles.go` JSONL
   workaround, PID heuristics, and the workspace socket are deleted.
3. **Fail-closed security.** Every layer that currently degrades silently becomes a hard stop
   with an explicit opt-out. Untrusted repo-supplied commands cannot read injected secrets.
4. **Professional CLI UX.** Visible errors with remediation, real flag architecture, correct
   stdout/stderr discipline, machine-readable output, TTY/NO_COLOR handling, a documented
   exit-code catalog, `doctor`, and proper distribution (completions, man pages, Homebrew).
5. **A test and CI safety net** that would have caught the shipped bugs, plus documentation that
   matches the code.

## 3. Non-goals

- Not adopting the one-container-per-session (worktree) model. Shared container per project is
  retained; teardown/attach are fixed rather than re-architected.
- Not shelling out to the Node `@devcontainers/cli`. The engine stays a single static Go binary.
- Not maintaining backward compatibility with the legacy `config.json`; no `migrate` command.
- Not introducing a host-side Node/Python Agent-SDK runtime. Session state comes from shelling to
  the `claude` CLI already inside the container.
- Not redesigning the iptables firewall (verified fail-closed, ipset-based, IPv6-deny, resolver-
  restricted DNS, self-testing).
- Not delivering full first-class VS Code portability in the core effort. Publishing standalone
  OCI Features and validating the VS Code/Codespaces path is **Phase 2b, a separable follow-on**
  (§6.7, §11). The core effort generates a `.devcontainer` referencing those Features by tag.

## 4. Resolved decisions

| # | Decision | Choice |
|---|----------|--------|
| Engine | Dev Container adoption strategy | Native Go single binary: read/generate `devcontainer.json`, build via the moby client, keep a slim bunker base image as the fast path; publish security as OCI Features for portability (Phase 2b). |
| Session model | Sessions per project | Shared container per project; fix attach/teardown guards. |
| Generated files | Ownership of generated `.devcontainer` | Two explicit modes with a header-marker discriminator (§6.1). Bunker owns/regenerates files it stamped; user-authored files are augmented, never rewritten in place. |
| Platform | Windows support | First-class (macOS, Linux, Windows via Git Bash/WSL2), tested. |
| Migration | Legacy config | Clean break — no compatibility layer, no `migrate` command. |
| Session events | How host learns of session/title changes | Pure polling of `claude agents --json` (verified) + Docker event stream for lifecycle. The `/workspace/.bunker.sock` and all in-container hook transport are removed. |
| Failure posture | Config/seed/inspect errors | Fail closed (`die`), with explicit per-condition override flags (§8.1). |

## 5. Target architecture

```
claude-bunker (single Go binary)
  cmd/            Cobra CLI. Root registers documented flags; `--` forwards the rest to claude.
  internal/
    devcontainer/ NEW. Parse/generate devcontainer.json (JSONC, ${localEnv} substitution);
                  resolve features (option defaults, digest pinning, contributed create-opts);
                  map spec → container create opts; run lifecycle hooks at the correct stage.
    container/    Docker API wrapper (moby client): build, create, exec, copy, firewall wiring.
    sessions/     Session state from `claude agents --json` (exec'd in-container) + Docker events.
                  Per-session title store (key = sessionId) + per-container display-name label.
    security/     Fail-closed helpers; just-in-time secret injection for the claude exec.
    config/       customizations["claude-bunker"] extras + fingerprinting of the resolved spec.
    sandbox/      Runtime managed-settings overlay (resolved domains, hooks, plugin flags).
    platform/     TTY/resize/paths (Windows path bugs fixed).
    cli/          NEW. Output streams, color/TTY detection, --json rendering, confirm prompts.
features/         Phase 2b — publishable OCI Features (firewall, hardening) wrapping the SAME
                  single-source scripts in internal/container/scripts/.
```

### Two execution paths (the load-bearing distinction)

- **Bunker path** (the CLI, the fast path): bunker builds/pulls its own image (slim base +
  Features baked at build), runs it with `Cmd=["sleep","infinity"]`, then **Go orchestrates**:
  root-execs firewall init (writing the fully-resolved allowlist to `/etc/bunker/domains`),
  overlays the runtime `managed-settings.json`, then execs `claude` as the unprivileged user.
  Go is the source of truth for all runtime-resolved values (proxy/plugin domains, hooks).
- **Portable path** (VS Code/Codespaces open the generated `.devcontainer`): there is no Go
  orchestrator, so the **Features'** own entrypoint/lifecycle run the firewall + hardening using
  the **generation-time** `allowDomains` option. Runtime-resolved conveniences (proxy detection,
  plugin-MCP domain scanning) are bunker-path-only and intentionally absent here. This is an
  accepted, documented limitation, not a bug.

The Go engine's core responsibility: read the spec, resolve features, build, apply the
`claude-bunker` customizations + runtime injection, and manage sessions.

## 6. Section 1 — Container engine → Dev Container spec (native Go)

### 6.1 Generated devcontainer.json: two modes + ownership rules

**Mode discriminator:** a file is *bunker-generated* iff its first line is the exact marker
`// GENERATED by claude-bunker — do not edit`. Otherwise it is *user-authored*.

- **Bunker-generated file:** bunker owns it and rewrites it wholesale from the resolved config +
  fingerprint on each relevant change. User intent for this mode is expressed in the bunker
  config / `customizations["claude-bunker"]`, which bunker reads back and re-emits.
- **User-authored file:** bunker **never rewrites it in place**. It reads it as a config source
  and augments the *in-memory* resolved spec used for the build; on disk the user's file is
  untouched. (Reading a file to build from it is not "editing" it — the earlier draft's "never
  parse user edits" clause is removed as incoherent.)

**Field ownership (applies to the in-memory resolved spec; on disk only a generated file is
written):**

| Field | Behavior |
|-------|----------|
| `image` / `build` / `dockerfile` | User value preserved; bunker supplies a default only when generating from scratch. |
| `features` | **Union.** Bunker's `claude-code`, `firewall`, `hardening` are always added; user features merged in. |
| `capAdd` | **Union + forced.** `NET_ADMIN`, `NET_RAW` are forced regardless of user value (security-critical). |
| `securityOpt`, `mounts` | **Union** (bunker adds seccomp/tmpfs-exclude entries; user entries kept). |
| `remoteUser` / `containerUser` | **Forced** to the bunker user (security-critical). |
| `containerEnv` / `remoteEnv` | User values preserved; bunker adds its own keys. |
| `customizations["claude-bunker"]` | Bunker-owned namespace (read-back + re-emit). |
| Any other standard field | Preserved untouched. |

Conflict precedence: security-critical forced fields (capAdd NET_ADMIN/NET_RAW, remoteUser,
seccomp) always win over user values; everything else prefers the user value and unions where
the field is a set.

### 6.2 customizations and option placement (no duplication)

- `allowDomains` lives **only** as the `firewall` Feature option, populated by bunker from the
  resolved config. It is **not** a `customizations` field. (Fixes the earlier double-placement.)
- `exclude`, `seedHistory`, `plugins`, `ghToken` live in `customizations["claude-bunker"]`.
- Example:

```jsonc
// GENERATED by claude-bunker — do not edit
{
  "name": "my-app (bunkered)",
  "image": "<bunker base image ref>",
  "features": {
    "ghcr.io/anthropics/devcontainer-features/claude-code:1": {},   // see §6.6 verification
    "ghcr.io/Devon-White/claude-bunker/firewall:1":  { "allowDomains": "private.example.com" },
    "ghcr.io/Devon-White/claude-bunker/hardening:1": {}
  },
  "capAdd": ["NET_ADMIN", "NET_RAW"],
  "remoteUser": "claude-bunker",
  "customizations": {
    "claude-bunker": {
      "exclude": ["secrets/", ".env.production"],
      "seedHistory": true,
      "plugins": "",
      "ghToken": ""   // usually supplied via flag/env, not committed
    }
  }
}
```

### 6.3 Base image, apt, and embedded scripts (what survives)

The custom pipeline **survives, slimmed**:

- **`base.dockerfile.tmpl` is retained** to create the `claude-bunker` user and install OS-level
  tooling (git, tmux, socat, curl, iptables, ipset, ca-certificates) and set the timezone.
- **Claude Code install** moves to the `claude-code` Feature (replacing the `claude.ai/install.sh`
  line), subject to §6.6 verification, with the bunker-installs-it fallback.
- **The firewall/hardening scripts remain embedded** (`go:embed`) for the bunker-path build and
  are **the single source** that the Phase 2b Features package. A CI check asserts the embedded
  copies and the `features/` copies are byte-identical.
- **User `apt` packages:** installed in a generated layer on top of the base. The apt bug is
  re-diagnosed in §6.5.

### 6.4 Feature resolution: defaults, ordering, and contributed create-time options

- **Option defaults** from each `devcontainer-feature.json` are applied (currently ignored).
- **Install ordering** honors `installsAfter` / `dependsOn` (topological); today `generate.go`
  has no ordering. Scope: single-level `installsAfter`; deep dependency graphs are out of scope
  and logged if encountered.
- **Contributed create-time options.** Feature metadata parsing is extended to capture
  `capAdd`, `securityOpt`, `mounts`, `entrypoint`, and lifecycle hooks (today only
  `id`/`installsAfter`/`containerEnv` are parsed; `CapAdd` is hardcoded at `lifecycle.go:293`).
  These are **collected during resolution and passed to the container-create call**, not baked as
  image layers, and unioned/deduped across three sources: the generated top-level fields, each
  resolved Feature, and any merged external spec.
- **seccomp** is shipped **with the Go binary** and applied via `securityOpt` at container-create
  (an in-container `install.sh` cannot place a host seccomp profile). The `hardening` Feature
  carries the same profile for the portable path.
- **capAdd duplication:** the generated top-level `capAdd` (§6.1) and the `firewall` Feature's
  contribution are deliberately belt-and-suspenders — the union dedupes them; documented as
  intentional so the bunker path is safe even if a user strips the Feature.

### 6.5 Lifecycle hooks: correct stage + reuse semantics

Hooks run **in-container as the remote user** at the correct stage, with concrete triggers:

| Hook | Trigger |
|------|---------|
| `onCreateCommand` | Exactly once, on first container creation. Skipped on reuse. |
| `postCreateCommand` (currently absent) | Exactly once, on first creation. Skipped on reuse. |
| `postStartCommand` | Every start/attach. |

"First creation" is detected by a marker (a container label `claude-bunker.provisioned=1` set
after onCreate/postCreate succeed); reuse checks the marker and skips. Today `onCreateCommand`
is wrongly emitted as a root build-time `RUN` with no `/workspace`.

### 6.6 apt-lists build bug (re-diagnosed)

The real defect is in **`generate.go` (~lines 64–68)**: it skips `apt-get update` for the
user-apt layer via a `strings.Contains(BaseDockerfile, "apt-get update")` heuristic, even though
the base `RUN` already deleted `/var/lib/apt/lists/*`. So user `apt` installs fail with "Unable
to locate package." Fix: **always `apt-get update` in the generated user-apt layer** (drop the
substring heuristic). Add an integration smoke build that installs a user apt package.

### 6.7 Anthropic claude-code Feature — verification (Phase 2 prerequisite)

The design references `ghcr.io/anthropics/devcontainer-features/claude-code`. Its existence,
published tags, options, and `remoteUser` expectations are **UNVERIFIED** and gate Phase 2.
Pre-Phase-2 spike: confirm the Feature and interface. **Fallback:** if unavailable or
incompatible, the bunker base installs Claude Code itself (as today). First-party bunker Features
are digest-pinned; the upstream Feature is tracked by tag but still enters the fingerprint (§6.8)
so its changes trigger a rebuild.

### 6.8 Fingerprint model re-partition

Replacing `config.json` with the resolved spec requires re-partitioning the two fingerprints.
`fingerprint.go` today hashes only local `ProjectConfig`; it must hash the resolved spec.

| Field | Scope |
|-------|-------|
| `image`/`build`/`dockerfile`, base Dockerfile, embedded scripts, user `apt`, `features` map + **pinned digests**, feature option values, resolved feature `containerEnv`, onCreate/postCreate | **Image** |
| `capAdd`, `securityOpt`, `mounts`, `remoteUser`/UID, top-level `containerEnv`, `allowDomains` (resolved), `exclude`, `postStartCommand`, workspace/subdir | **Container** |

Rules: pinned Feature **digest strings** enter the image hash (not fetched layers), so hashing is
deterministic and **offline-safe** — no network fetch of feature metadata is required to compute
the fingerprint (metadata needed for resolution is fetched at build, but the hash uses the pinned
digest). §6.1 moved env to `containerEnv` semantics, so env is container-scoped (no longer forces
an image rebuild).

### 6.9 Builder API

Keep the non-deprecated moby `ImageBuild` call. Enabling BuildKit via the daemon build option is
a nice-to-have but **not** required; the design will **not** add a buildkit-client dependency or
shell out to `buildx` (both violate the single-static-binary / no-CLI-shell-out constraints). The
"legacy builder" concern is minor and rescoped to "use the current moby ImageBuild API correctly."

## 7. Section 2 — Session management (shared container, official surfaces)

### 7.1 Entity model: two distinct stores (explicit)

There are **two separate identities**, matching the two things a user names:

1. **Per-session title.** Store key = Claude `sessionId` → title. This is the name shown per
   Claude session in the TUI/`sessions list`. Source of truth is **`claude agents --json`**
   (the `name` field — verified to reflect user renames); bunker's host store is a cache/fallback
   for stopped containers.
2. **Per-container display name.** Store key = container ID → display name; mirrored to the Docker
   label `claude-bunker.displayname`. This is the "rename the sandbox" feature
   (`sessions rename`). It is **not** per-session and is never conflated with session titles.

Both host stores use **file locking** (fixes TUI-vs-`sessions list` concurrent clobber). Session
titles are **not** mirrored to a single container label (N sessions can't map to one label);
they live in the sessionId store + come live from `claude agents --json`.

**Reconcile precedence for a session title:** `claude agents --json` (running container) >
sessionId store (cache / stopped container) > default display name.

### 7.2 Identity from the official surface (verified schema)

`claude agents --json` run inside the container (`docker exec ... claude agents --json --cwd
/workspace`) returns a JSON array; verified fields:

```
sessionId (uuid), pid (container-namespace when exec'd inside), cwd, kind (interactive|background),
name (user-set display name; omitted if unnamed), status (idle|waiting|...), waitingFor, state,
startedAt, id (short id for background agents)
```

This replaces: `docker top` PID parsing for session identity, the
`~/.claude/sessions/<pid>.json` dependency, the PID-sort alignment heuristic, `sessionIDCache`,
and `titles.go` entirely. Background agents/subagents appear as `kind: background`, so subagent
display no longer needs process-tree walking. `docker top` may be retained only for a coarse
"is the container busy" check if needed, but is no longer the identity source.

**Requires-exec limitation:** `claude agents --json` needs a running container. For **stopped**
containers (which `sessions list`/`status`/TUI still enumerate), titles/names come from the host
stores + Docker label only; state is "stopped." Defined per-state, no exec attempted on stopped
containers.

### 7.3 Bidirectional rename (bounded by the verified CLI surface)

- **Claude → bunker:** poll `claude agents --json` on the interval in §7.4. Picks up `/rename`
  and Ctrl+R authoritatively (the `name` field updates). Fully supported.
- **bunker → Claude:** `claude -n <name>` sets the name **at launch** (verified flag). There is
  **no `claude agents rename` verb**, so bunker **cannot** live-rename a running Claude session.
  A `sessions rename` invoked while a session runs updates bunker's per-container display name +
  label (and, for a session title, the host cache), and is reconciled back to Claude's value on
  the next poll. This is the honest bound and is reflected in the success criteria (§13).

### 7.4 Event model: polling + Docker events, no socket

`/workspace/.bunker.sock` and all in-container hook transport (`bunker-hook.sh` socket send) are
**removed** — the socket fails on Docker Desktop, is forgeable (chmod 0666, no auth), and pollutes
the working tree. Replacement:

- **Docker event stream** — container lifecycle (start/stop/die/destroy), event-driven.
- **`claude agents --json` poll** — session/title/state, **every 3 seconds while a TUI is
  attached** (fixed default; `--interval` overridable), one-shot for non-TUI CLI commands.

No `SessionStart`/hook payloads are relied upon (there is no remaining host transport for them),
resolving the earlier contradiction. If a lightweight bunker hook is kept at all, it is only to
nudge a refresh via a mechanism the host already polls — not a socket — and is optional.

### 7.5 Attach/teardown fixes

- `sessions attach` and the TUI attach path **guard on `HasOtherActiveSessions`** before stopping
  (today they `docker kill` unconditionally, killing sibling sessions).
- **Graceful stop:** SIGTERM, then SIGKILL after **10 s**, so `SessionEnd` hooks fire and final
  state flushes.
- Record `execID` **before** the self-count check so signal-time cleanup doesn't count its own
  exec as "other" and orphan the container.
- Use the **moby client**, not a `docker` CLI shell-out (fixes colima / `DOCKER_HOST` / daemon-
  only hosts).
- `--rebuild` **respects active sessions** (today it bypasses the guard).

### 7.6 Session-history seeding

Add a newer-file guard so seeding host session files over the persistent volume can't roll back
in-container progress. Fix the Windows `encodeProjectPath` bug (`filepath.Abs("/workspace")` →
`C:\workspace` → `C--workspace`), which seeds history to a directory Claude Code never reads.

## 8. Section 3 — Security hardening (fail-closed)

### 8.1 Fail-open → fail-closed, with mapped overrides

| Condition | Behavior | Override |
|-----------|----------|----------|
| Malformed `config` / `devcontainer.json` | `die` | `--force` (run with best-effort parse) |
| `SeedSettings` / managed-settings injection fails | `die` (no sandbox enforcement is unacceptable) | `--no-sandbox` |
| `HasOtherActiveSessions` inspect error | treat as "sessions may be active" → refuse teardown | `--force` |

`--force` and `--no-sandbox` are **registered root flags** (§9) and land in **Phase 0** alongside
the fail-closed change, so there is never a window where a `die()` has no escape hatch.

### 8.2 Just-in-time secret injection (shared-container model)

Invariant: **repo-supplied lifecycle commands never run while auth secrets are present.**

- Secrets are written to the `/run/secrets` tmpfs via a single root exec **immediately before**
  `ExecInteractive` of `claude`, and the auth wrapper exports them for that process only.
- All repo lifecycle commands (`postStartCommand`, etc.) run **before** any secret is materialized.
- On a later **attach** to the shared container, `postStartCommand` is not re-run (it's a
  first-creation-adjacent concern; only `postStart` semantics apply — and secrets for the existing
  session are already scoped to that session's exec, not globally on tmpfs for new execs to read).
  The design writes secrets per-exec, not as long-lived global tmpfs files, so a new attach cannot
  read another session's secrets.
- For reused/`--keep` containers, **verify the `/run/secrets` tmpfs mount exists before writing**;
  refuse otherwise so tokens never land on the writable overlay (fixes the current
  `reinjectAuthSecrets`-to-overlay bug). Auth presence enters neither fingerprint by design;
  secrets are a runtime concern, re-applied every start.
- The auth wrapper script is retained.

### 8.3 Other hardening

- **Windows exclude overlays fixed** (path-vs-filepath): hidden files stay hidden on Windows.
- **Supply chain:** pin Feature digests + fingerprint them (§6.8); build the release base image
  from the **tagged ref** (`--ref ${GITHUB_REF_NAME}`), not master HEAD.
- **Verify the pulled base** before fast-pathing it: bunker computes a digest over its embedded
  scripts and compares against a label baked into the base image at build (`claude-bunker.scripts
  =<digest>`); mismatch fails closed. (If §6.7's fallback removes some embedded scripts, rescope
  accordingly.)
- `managed-mcp.json` gets the same chmod-444 hardening as `managed-settings.json`.
- seccomp profile tightened to block `io_uring` (applied via `securityOpt`, §6.4).
- **Keep the firewall** (verified strong).

## 9. Section 4 — CLI polish (clig.dev / gh conventions)

- **Visible errors (critical fix).** Drop global `SilenceErrors`; surface `Execute()`'s error in
  `main.go` with a non-zero exit; print to stderr with remediation.
- **Root flag architecture.** Register + document `--keep`, `--rebuild`, `--gh-token`,
  `--api-key`, `--oauth-token`, `--verbose`, `--quiet`, **`--force`**, **`--no-sandbox`**,
  **`--no-color`**, **`--interval`** so they appear in `--help`. A `--` terminator with an explicit
  "everything after `--` is forwarded to claude" contract. Missing-value credential flags **error**.
- **Stream discipline.** status/info/verbose → stderr; payload → stdout. `version`/`--json` clean
  when piped.
- **Machine-readable output.** `--json` on `status`, `version`, `prune` (additive-only), matching
  `sessions list --json`.
- **TTY / color.** `isStdoutTTY`/`isStderrTTY` (not stdin-only); honor `NO_COLOR`/`CLICOLOR` +
  `--no-color`; wire lipgloss to respect them.
- **Interactivity.** Non-interactive confirms fail with a `--yes` hint instead of silently
  no-op'ing (`sessions stop`, `prune`). `init --defaults` for scripted use; `init` abort leaves
  the file untouched (fixes the `{}`-overwrite data-loss bug).
- **New affordances.** `claude-bunker doctor` (docker reachable + version, firewall self-test);
  `--dry-run` on mutating commands; docker-ping **timeout** (no indefinite hang); a **build/create
  lock** (§9.1); documented exit-code catalog (§9.2); first-run onboarding hint.

### 9.1 Concurrency lock (scoped)

The lock covers **only the image-build + container-create critical section** (never the
interactive session or attach), closing the `resolveContainer` check-then-create TOCTOU
(`run.go:306–358`) without serializing the multi-session concurrency §7.5 preserves. It is a
per-project file lock with a timeout and stale-lock detection (PID + mtime), distinct from the
name-store lock (§7.1); location under the OS runtime/cache dir, not the workspace.

### 9.2 Exit-code catalog

| Class | Code |
|-------|------|
| Success (or forwarded claude success) | 0 |
| Generic bunker error (stderr message) | 1 |
| User cancelled a bunker confirm/prompt | 2 |
| Docker unavailable / not running | 4 |
| Config/devcontainer parse, feature pull, firewall self-test, seed, workspace-escape | 1 (with a remediation line) |
| `run` path: claude's own exit code | propagated verbatim (so Ctrl+C in claude → 130, not 2) |

Precedence: for the `run`/`shell` path, claude's exit code always wins. Code 2 applies only to
bunker's own prompt cancellations. `die()` and the signal handler are updated to route non-1 codes
(today both hardcode 1). Every non-zero exit writes a non-empty stderr message.

## 10. Section 5 — Testing, CI, distribution, docs

- **CI runs `go test ./...`** on PR and tag (today: none), plus an integration smoke build that
  installs a user apt package (catches §6.6) and asserts the firewall self-test passes.
- **Test isolation.** Tests currently wipe the developer's real `~/.claude/session-names.json`
  (`PruneStaleNames` against the real home). Override `HOME`/store path in tests. Add coverage
  for: the two name stores + rename (both directions, mocked `claude agents --json`), flag parsing
  + `--` passthrough, teardown/attach guards, devcontainer.json parse/generate/merge + field
  ownership, and fingerprint scope partitioning.
- **goreleaser.** Add shell completions (command exists), man pages, a Homebrew tap, and a
  version string augmented with **commit + date** (current ldflags injects only `cmd.Version`).
  Checksums are **already configured** — no change there.
- **Docs.** Rewrite README/CLAUDE.md to match reality: the firewall allowlist table (the current
  npm/pypi "allowed by default" claim is wrong), settings-injection behavior, the sessions
  commands, the new devcontainer flow, and the removal of the legacy config.

## 11. Phasing

- **Phase 0 — Stop the bleeding.** Error visibility (`SilenceErrors` + `main.go` + exit-code
  routing), fail-closed posture **with `--force`/`--no-sandbox` registered**, `init` data-loss
  fix, test HOME isolation. Small, unblocks diagnosis.
- **Phase 1 — Sessions.** Delete `titles.go`/PID heuristics/socket; rebase on `claude agents
  --json` (schema verified) + Docker events; two file-locked stores + display-name label; fix
  attach/teardown guards; history-seed newer-file guard + Windows path fix.
- **Phase 2 — Dev Container spec (native).** `internal/devcontainer/` parse+generate+merge with
  the §6.1 ownership rules; feature resolution (defaults, ordering, contributed create-opts);
  lifecycle-stage + reuse semantics; fingerprint re-partition (§6.8); apt fix (§6.6); digest
  pinning. **Gated by the §6.7 Anthropic-Feature spike.**
- **Phase 2b — Portability (separable).** Publish `firewall` + `hardening` OCI Features (single-
  source-verified against embedded scripts), pin their digests in generated output, validate the
  VS Code/Codespaces path. Own repo/release lifecycle; own open questions (§12).
- **Phase 3 — CLI polish.** Flag architecture, stream discipline, `--json`, TTY/NO_COLOR,
  `doctor`, `--dry-run`, lock, exit-code catalog.
- **Phase 4 — CI, tests, distribution, docs.**

## 12. Open questions for planning (do not block this spec)

1. **[Phase-2 gate]** Does `ghcr.io/anthropics/devcontainer-features/claude-code` exist with a
   compatible option/`remoteUser` interface? Fallback: bunker installs Claude Code itself (§6.7).
2. Is `claude agents --json` reliably available and correct **inside** the sandboxed container
   (vs. on the host, where it is verified)? If the in-container daemon differs, confirm the schema
   there. Fallback: host name store + "stopped/unknown" state only.
3. **[Phase-2b]** Publish location/versioning for the two OCI Features (dedicated repo vs.
   `features/` dir), local-dev reference path (unpinned tag/local path until first publish), and
   whether to list them on containers.dev.
4. Does the `hardening` Feature fully replace `sandbox/seed.go` runtime injection, or do they
   coexist (bunker path uses Go runtime overlay, portable path uses the Feature)? Current design
   assumes coexist (§5 two paths).

## 13. Success criteria (measurable)

- **Portability parity (exec-checkable, not GUI):** a container from the bunker path and one from
  the generated `.devcontainer` both satisfy: `NET_ADMIN` present; firewall self-test exits 0
  (default-deny confirmed); `/etc/claude-code/managed-settings.json` exists with mode `444`.
- **Rename (Claude → bunker):** a `/rename` inside Claude is reflected in `sessions list`/TUI
  within **one 3 s poll (~3 s)**, on macOS/Linux/Windows.
- **Rename (bunker → Claude):** `claude -n` at launch sets the Claude-side name; a live
  `sessions rename` updates bunker's display name + label immediately and does **not** claim to
  rename a running Claude session (no such CLI verb). (Goal 2's "both directions" is qualified
  accordingly.)
- **No sibling kill:** exiting one attached session never stops a container with other live
  sessions (asserted by a test with a mocked multi-session `HasOtherActiveSessions`).
- **Error contract (testable):** every non-zero exit writes a non-empty stderr message and returns
  a code from §9.2; no command silently exits 1 with no output.
- **Fail-closed:** a malformed config or failed hardening refuses to launch unless the mapped
  override flag (§8.1) is given.
- **CI:** `go test ./...` passes on PR and tag; the release base image is built from the tagged
  ref; the smoke build installs a user apt package successfully.
- **Test hygiene:** running the suite does not touch the developer's real `~/.claude`.
