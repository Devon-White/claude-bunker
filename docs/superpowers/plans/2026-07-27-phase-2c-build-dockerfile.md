# Phase 2c — Repackage VS Code Portability as `build.dockerfile` (replace OCI Features)

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development. Steps use `- [ ]`.

**Goal:** Replace the OCI-Features packaging (Phase 2b) with the industry-standard **committed `.devcontainer/Dockerfile` + scripts + `runArgs`** approach that Anthropic's own reference devcontainer uses. This is publish-free (no ghcr), avoids the Feature-overwrite bug class, and reuses bunker's canonical base template as the single source. bunker's NATIVE build is untouched.

**Architecture:** `init` writes a committed `.devcontainer/` bundle — `Dockerfile` (= `container.GenerateBaseDockerfile()`, the same rendered base template bunker's native build uses), the three firewall scripts, an `allowed-domains.txt` (builtins + the project's allowDomains), and `seccomp.json`. The generated `devcontainer.json` uses `"build": {"dockerfile": "Dockerfile"}` (not `image` + Features), plus `capAdd`, `runArgs` (seccomp), `remoteUser`, and a `postStartCommand` that runs the firewall via `sudo`. bunker's native build keeps using its in-memory Dockerfile + runtime firewall/seccomp; it strips claude-code as before and ignores `runArgs`. User deps stay as the **apt-packages Feature** (layered on top of the built Dockerfile by both bunker and VS Code — unchanged from the deps phase). The OCI Feature packages, the publish workflow, and common-utils are deleted.

**Tech Stack:** Go 1.26; the existing `base.dockerfile.tmpl`; the Dev Container `build.dockerfile` mechanism. No new deps.

## Global Constraints

- Go 1.26; keep `go build ./...`, `go vet ./...`, `go test ./...` green and `gofmt -l .` empty after each task.
- **bunker's native build/run MUST stay behavior-identical.** It renders `base.dockerfile.tmpl` in-memory and applies firewall/seccomp/apparmor/tmpfs at runtime. This phase only ADDS a sudo layer to the base template (harmless — native runs the firewall as root, not via sudo) and writes extra committed files; it does not change bunker's build/run path.
- **Single source of truth:** the committed `.devcontainer/Dockerfile` = `container.GenerateBaseDockerfile()` (byte-identical), the scripts = the canonical `internal/container/scripts/*` (via `container.BuildContextScripts()`), the domains = `container.BuiltinDomains()` + `cfg.AllowDomains`, the seccomp = `container.SeccompProfileJSON()`. Consistency tests assert each.
- **Ownership:** `.devcontainer/{Dockerfile,*.sh,allowed-domains.txt,seccomp.json}` + the hardening wiring in `devcontainer.json` are bunker-OWNED (GENERATED, regenerated — users don't hand-edit). User deps live in `devcontainer.json`'s `features` (apt-packages) — user-owned, preserved across regen.
- Fail-closed firewall preserved. No secrets in committed files.
- The generated `devcontainer.json` build context defaults to `.devcontainer/`, so the firewall scripts must be written there (the Dockerfile `COPY init-firewall.sh` resolves against that context).

## Task Order

1. **Task 1 — base template: add scoped `sudo`** so the VS Code `postStartCommand` can run the firewall as the non-root user. Native unaffected.
2. **Task 2 — `init` writes the committed `.devcontainer/` bundle** (Dockerfile + scripts + allowed-domains.txt + seccomp.json), with consistency tests.
3. **Task 3 — rework `Generate`**: `build.dockerfile` + `postStartCommand` + `runArgs`; drop the firewall/hardening/common-utils/claude-code feature refs; keep apt-packages; update `bunkerManagedFeaturePrefixes`.
4. **Task 4 — delete the OCI Features machinery** (feature packages, genfeatures firewall-copy, firewall_drift_test, publish-features.yml).
5. **Task 5 — docs** (README/CLAUDE.md: the Dockerfile-based portability model, no Features, no publishing).

---

### Task 1: Add scoped `sudo` to the base Dockerfile template

**Files:** Modify `internal/container/scripts/base.dockerfile.tmpl`; Test `internal/container/baseimage_test.go` (or a new test).

**Interfaces:** Produces a base Dockerfile that installs `sudo` and grants the `{{.User}}` a NOPASSWD sudoers entry scoped to the firewall scripts, so `sudo init-firewall.sh` works for the VS Code postStart. bunker's native path (runs the firewall as root via exec) ignores it.

- [ ] **Step 1: Failing test.** Assert `container.GenerateBaseDockerfile()` output contains a `sudo` install and a scoped sudoers grant for the firewall path (e.g. `RUN echo '<user> ALL=(root) NOPASSWD: <FirewallPath>, <RefreshFirewallPath>' > /etc/sudoers.d/claude-bunker-firewall`). Read `internal/container/baseimage.go` (`GenerateBaseDockerfile`, `baseTemplateData`) + the template first for the exact var names/paths.
- [ ] **Step 2: Run → FAIL.**
- [ ] **Step 3: Implement.** In `base.dockerfile.tmpl`: add `sudo` to the apt install list; after the firewall-script COPY, add a `USER root` `RUN` that writes `/etc/sudoers.d/claude-bunker-firewall` = `{{.User}} ALL=(root) NOPASSWD: {{.FirewallPath}}, {{.RefreshFirewallPath}}` and `chmod 0440` it. (Scoped to exactly the firewall scripts — NOT blanket sudo. This mirrors Anthropic's reference.)
- [ ] **Step 4: Tests + build.** `go test ./internal/container/... -run 'Base|Dockerfile' -v`; `go build ./...`; `go test ./...` green; `gofmt -l .` empty. Confirm bunker's native build still works (the sudo layer is additive; native firewall exec is unchanged).
- [ ] **Step 5: Commit** — `git add internal/container/scripts/base.dockerfile.tmpl internal/container/*_test.go && git commit -m "feat(base): add scoped NOPASSWD sudo for the firewall (enables the VS Code postStart path; native unaffected)"`

---

### Task 2: `init` writes the committed `.devcontainer/` bundle

**Files:** Modify `cmd/init.go` (`writeDevContainer`); Test `cmd/init_test.go`.

**Interfaces:**
- Consumes: `container.GenerateBaseDockerfile()`, `container.BuildContextScripts()` (the 3 canonical scripts), `container.BuiltinDomains()`, `container.SeccompProfileJSON()`, `cfg.AllowDomains`.
- Produces: alongside `devcontainer.json`, `writeDevContainer` also writes `.devcontainer/Dockerfile`, `.devcontainer/{init-firewall.sh,refresh-firewall.sh,firewall-common.sh}` (0755), `.devcontainer/allowed-domains.txt` (0644), `.devcontainer/seccomp.json` (0644). All derived from bunker's canonical sources.
- **SECURITY (the committed Dockerfile bakes the allowlist ROOT-OWNED):** the Dockerfile = `container.GenerateBaseDockerfile()` **plus a suffix** that `COPY allowed-domains.txt /etc/claude-bunker/allowed-domains.txt` (COPY makes it root:root) and `chmod 0444`. This is why the firewall's arg-pinned sudo (Task 1) targets `/etc/claude-bunker/allowed-domains.txt`: the agent cannot edit that baked, root-owned file (it's NOT the agent-writable workspace copy), and the arg-pin means it can't point the firewall at a different file. bunker's native build does NOT bake domains (it writes `/tmp/.bunker-domains` at runtime and keeps domains in the CONTAINER fingerprint, not the image — so a domain change doesn't bust the image cache); this suffix is VS-Code-only.

- [ ] **Step 1: Failing tests.** In `cmd/init_test.go`: after `writeDevContainer`, assert the temp workspace's `.devcontainer/` contains: `Dockerfile` == `container.GenerateBaseDockerfile()` + the domains-COPY suffix (assert it STARTS WITH `GenerateBaseDockerfile()` and CONTAINS `COPY allowed-domains.txt /etc/claude-bunker/allowed-domains.txt` + a `chmod 0444` on it); the 3 scripts byte-equal to the canonical `container.BuildContextScripts()` entries; `allowed-domains.txt` == `strings.Join(container.BuiltinDomains()+cfg.AllowDomains, "\n")+"\n"`; `seccomp.json` == `container.SeccompProfileJSON()`. Read the existing `writeDevContainer` (it already writes seccomp.json from Task-4-of-2b) + `cmd/init_test.go` harness first.
- [ ] **Step 2: Run → FAIL.**
- [ ] **Step 3: Implement.** Extend `writeDevContainer` to write the additional artifacts into `filepath.Dir(devcontainerPath)` = `.devcontainer/`. Use the canonical accessors (no hardcoded copies). The Dockerfile = `container.GenerateBaseDockerfile()` + a suffix block:
```
\n# Portable firewall allowlist (VS Code path): root-owned + read-only so the\n# sandboxed agent cannot widen it. The arg-pinned firewall sudo targets this path.\nCOPY allowed-domains.txt /etc/claude-bunker/allowed-domains.txt\nUSER root\nRUN chmod 0444 /etc/claude-bunker/allowed-domains.txt\n
```
(Use the `container.AllowedDomainsPath` constant if exposed; else the literal `/etc/claude-bunker/allowed-domains.txt`. COPY'd files are root:root by default, so no chown needed — just the read-only chmod.) `allowed-domains.txt` = builtins + `cfg.AllowDomains` (one per line). Respect the dry-run guard (write nothing under `--dry-run`, mirror the existing seccomp.json handling). Confirm `cmd` already imports `internal/container`.
- [ ] **Step 4: Tests + build.** `go test ./cmd/ -run 'WriteDevContainer|Init' -v`; `go build ./...`; `go test ./...` green; `gofmt -l .` empty.
- [ ] **Step 5: Manual smoke.** `go build -o /tmp/cb2c . && WS=$(mktemp -d) && CLAUDE_BUNKER_WS=$WS /tmp/cb2c init --defaults` → `ls $WS/.devcontainer/` shows Dockerfile + 3 scripts + allowed-domains.txt + seccomp.json + devcontainer.json. `head $WS/.devcontainer/Dockerfile` shows the base (FROM debian, user, installs). Report.
- [ ] **Step 6: Commit** — `git add cmd/init.go cmd/init_test.go && git commit -m "feat(init): write the committed .devcontainer Dockerfile+scripts+domains+seccomp bundle (derived from canonical sources)"`

---

### Task 3: Rework `Generate` to use `build.dockerfile` (drop the hardening Features)

**Files:** Modify `internal/devcontainer/generate.go`, `cmd/init.go` (the `Generate` call), `internal/devcontainer/load.go` (`bunkerManagedFeaturePrefixes`); Test `internal/devcontainer/generate_test.go`, `load_test.go`.

**Interfaces:**
- Produces: a `devcontainer.json` with `"build": {"dockerfile": "Dockerfile"}` (no `image`), `capAdd: forcedCaps`, `remoteUser: bunkerUser`, `runArgs: ["--security-opt", "seccomp=${localWorkspaceFolder}/.devcontainer/seccomp.json"]`, a `postStartCommand` that runs `sudo <FirewallPath> ${containerWorkspaceFolder}/.devcontainer/allowed-domains.txt` then backgrounds the refresh daemon, and `features` = ONLY the user's apt-packages (no firewall/hardening/common-utils/claude-code). The Dockerfile installs Claude Code, so no claude-code Feature is needed.
- Consumes: `container.ContainerFirewallPath`/equivalent constant for the firewall script path (grep constants.go); `cfg.Features` (user features, e.g. apt-packages).

- [ ] **Step 1: Failing tests.** `Generate` output: has `dc["build"] == {"dockerfile": "Dockerfile"}`, NO `image` key, NO firewall/hardening/common-utils/claude-code refs in `features`, `dc["postStartCommand"]` runs the firewall via sudo against `allowed-domains.txt`, `runArgs` present, user apt-packages feature preserved in `features`. And `stripBunkerFeatures`: firewall/hardening/common-utils no longer need to be stripped (they're not emitted) — decide whether to keep them in the strip list defensively (recommend: keep claude-code + firewall + hardening prefixes in the list as a defensive no-op so a hand-added ref is still stripped, but that's optional — the test should assert whatever you choose). Read the current `Generate` + `GenerateOpts` + `load_test.go` first.
- [ ] **Step 2: Run → FAIL.**
- [ ] **Step 3: Implement `generate.go`.** Remove `ClaudeCodeFeature`/`FirewallFeature`/`HardeningFeature`/`CommonUtilsFeature` handling that emits refs (or keep the opts but stop defaulting them in init). Replace the `image` emission with `dc["build"] = map[string]any{"dockerfile": "Dockerfile"}`. Keep `capAdd`/`remoteUser`/`runArgs`. Add:
```go
fw := container.FirewallScriptPath        // "/usr/local/bin/init-firewall.sh"
refresh := container.RefreshFirewallScriptPath
domains := container.AllowedDomainsPath   // "/etc/claude-bunker/allowed-domains.txt" — the BAKED root-owned path
dc["postStartCommand"] = fmt.Sprintf("sudo %s %s && sudo %s %s >/dev/null 2>&1 &", fw, domains, refresh, domains)
```
**SECURITY — use the BAKED `container.AllowedDomainsPath` (`/etc/claude-bunker/allowed-domains.txt`), NOT the workspace copy.** The firewall must read the root-owned, agent-unwritable baked file, and this exact command must match the arg-pinned sudoers grant from Task 1 (same script path + same domains path). Using `${containerWorkspaceFolder}/.devcontainer/allowed-domains.txt` would be a security hole (the agent can edit the workspace copy) AND wouldn't match the arg-pinned sudoers (sudo would deny it). Confirm the paths against `internal/container/constants.go`. If a user `cfg.PostStartCommand` also exists, append it AFTER the firewall command. Keep emitting `cfg.Features` (apt-packages) into `features`.
- [ ] **Step 4: `cmd/init.go`.** Stop passing the firewall/hardening/common-utils/claude-code feature refs to `Generate` (they're gone). Leave the apt-packages-feature path (from the deps phase) intact.
- [ ] **Step 5: `load.go`.** The generated file no longer references the bunker features, but KEEP `claude-code`/`firewall`/`hardening`/`common-utils` in `bunkerManagedFeaturePrefixes` as a defensive strip (harmless — strips them if a user hand-adds one; bunker does all natively). Note this in a comment.
- [ ] **Step 6: Verify + smoke.** Tests pass; build/vet/suite green; gofmt clean. `init --defaults` → `devcontainer.json` has `build.dockerfile`, no `image`, no feature refs except any user apt-packages, `runArgs`, and a firewall `postStartCommand`. Report the generated file.
- [ ] **Step 7: Commit** — `git add internal/devcontainer/generate.go cmd/init.go internal/devcontainer/load.go internal/devcontainer/*_test.go && git commit -m "feat(init): generate build.dockerfile devcontainer.json (firewall via postStart, seccomp via runArgs) — drops the OCI hardening Features"`

---

### Task 4: Delete the OCI Features machinery

**Files:** Delete `features/src/firewall/`, `features/src/hardening/`, `features/firewall_drift_test.go`, `.github/workflows/publish-features.yml`; Modify `cmd/genfeatures/main.go` (remove the firewall-feature derivation — the scripts/domains now go to `.devcontainer/` via init, not a feature package) OR delete `cmd/genfeatures` + `generate.go`'s `go:generate` if nothing else uses it.

**Interfaces:** Produces a tree with no firewall/hardening Feature packages, no feature-publish workflow, no genfeatures firewall-copy path. bunker's native firewall/hardening + the new committed `.devcontainer/` bundle are the only surfaces.

- [ ] **Step 1: Confirm removal scope.** Grep for references to `features/src/firewall`, `features/src/hardening`, `genfeatures`, `firewall_drift_test`, `publish-features` across the repo. Confirm nothing in the live code path (init/run/build) depends on the feature packages (they were only referenced by the now-removed feature refs in Generate).
- [ ] **Step 2: Delete.** Remove `features/src/firewall/`, `features/src/hardening/`, `features/firewall_drift_test.go` (and `features/` if now empty), `.github/workflows/publish-features.yml`. For `cmd/genfeatures`: if it only derived the firewall feature package, delete it + its `go:generate` directive in `generate.go`. If `generate.go` becomes empty, delete it. Do NOT delete `cmd/genbuild` (unrelated).
- [ ] **Step 3: Verify.** `go build ./...`; `go vet ./...`; `go test ./...` green; `gofmt -l .` empty. `git status` shows only deletions (+ any `generate.go`/genfeatures cleanup). Confirm `release.yml`/`ci.yml`/`base-image.yml` untouched.
- [ ] **Step 4: Commit** — `git add -A && git commit -m "chore: remove OCI Feature packages + publish workflow (superseded by build.dockerfile)"`

---

### Task 5: Docs — the Dockerfile-based portability model

**Files:** Modify `README.md`, `CLAUDE.md`.

- [ ] **Step 1: README.** Rewrite the "Portability to VS Code / Codespaces" section: a bunkered repo opens in VS Code and builds `.devcontainer/Dockerfile` (bunker's hardening: firewall tooling, bubblewrap, non-root user, Claude Code) + applies `runArgs` seccomp + `postStartCommand` firewall + `capAdd`. NO ghcr publishing, NO OCI Features. State the ownership model (Dockerfile + scripts + seccomp.json + hardening wiring = bunker-owned/regenerated, don't hand-edit; add deps via the apt-packages feature in `features`). Note the tmpfs-excludes/secrets are still bunker-only. Keep the honest "live VS Code open not yet exercised" caveat.
- [ ] **Step 2: CLAUDE.md.** Update the architecture note: replace the "OCI Features (firewall/hardening/common-utils)" description with the build.dockerfile model (init writes `.devcontainer/Dockerfile`+scripts+domains+seccomp from canonical sources; `build.dockerfile` + runArgs + postStart firewall; no Features/publishing). Remove mentions of the deleted feature packages / publish workflow.
- [ ] **Step 3: Verify.** Grep README/CLAUDE.md: no remaining claim of firewall/hardening OCI Features or a publish workflow; the build.dockerfile + apt-packages-for-deps model is documented; ownership model present.
- [ ] **Step 4: Commit** — `git add README.md CLAUDE.md && git commit -m "docs: VS Code portability via build.dockerfile (replaces the OCI Features model)"`

---

## Self-Review Checklist

- bunker's native build/run is behavior-identical (only the additive sudo layer + extra committed files; native firewall/seccomp/tmpfs unchanged).
- The committed `.devcontainer/Dockerfile`/scripts/domains/seccomp are all DERIVED from canonical sources (consistency-tested), never hand-maintained copies.
- Generated `devcontainer.json`: `build.dockerfile` (no `image`), `runArgs` seccomp, firewall `postStartCommand`, `capAdd`, `remoteUser`, and `features` = user apt-packages only.
- No ghcr publishing anywhere; the OCI Feature packages + publish workflow are gone.
- Deps still work via the apt-packages feature (layered on the built Dockerfile).
- The `runArgs` `${localWorkspaceFolder}` and `postStartCommand` `${containerWorkspaceFolder}` substitutions are the correct VS Code forms.
