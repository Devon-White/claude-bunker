# Phase 2b — OCI Features + VS Code Portability (firewall/hardening Features + seccomp via runArgs)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use `- [ ]` checkboxes.

**Goal:** Make bunker's security *portable* — a bunkered repo opened in VS Code / Codespaces (without running `claude-bunker`) gets the real firewall + hardening posture. Author the `firewall` and `hardening` OCI Feature packages in-repo (unpublished), and ship the one thing a Feature legally cannot carry — bunker's custom seccomp profile — via `runArgs` + a committed profile file in the generated `devcontainer.json`. Every portable artifact is DERIVED from bunker's canonical Go/script source and CI-gated against drift.

**Architecture (the layering, per the Dev Container spec):** each concern lives in the layer where it is *portable*. Firewall → a Feature (`capAdd` + scripts + boot-time run; proven pattern). Bubblewrap + `apparmor=unconfined` → a Feature (build-time install + a named securityOpt). Custom seccomp profile → **not** a Feature (a `seccomp=<file>` is host-resolved; a Feature only ships into the image) → `runArgs` in the generated `devcontainer.json` referencing a committed `.devcontainer/seccomp.json`. Dynamic per-project tmpfs excludes / secrets → stay in bunker's Docker API path (can't be portable). bunker's OWN build is unaffected: it already strips `firewall`/`hardening` features (`bunkerManagedFeaturePrefixes`) and its read struct has no `runArgs` field, so it ignores both — bunker keeps applying firewall/seccomp/apparmor natively.

**Tech Stack:** Go 1.26; bash (Feature `install.sh`); the Dev Container Feature spec (`devcontainer-feature.json`). No new Go deps.

## Global Constraints

- Go 1.26; keep `go build ./...`, `go vet ./...`, `go test ./...` green and `gofmt -l .` empty after each task.
- **Feature packages** live at `features/src/firewall/` and `features/src/hardening/`, publishable to `ghcr.io/Devon-White/claude-bunker/firewall` and `.../hardening` (the refs ALREADY reserved in `bunkerManagedFeaturePrefixes`). **This phase does NOT publish** — it authors the packages + an opt-in publish workflow that does not auto-fire.
- **SINGLE SOURCE OF TRUTH + drift tests** for every portable artifact — this is the core discipline:
  - Firewall scripts (`init-firewall.sh`, `refresh-firewall.sh`, `firewall-common.sh`): canonical in `internal/container/scripts/`. The Feature's copies are DERIVED (via a generator) and a Go test FAILS if they differ.
  - Builtin firewall domains: canonical in `internal/container/domains.go` `BuiltinDomains()`. The Feature's baked domain list is DERIVED + drift-tested.
  - Seccomp profile: canonical as an exported Go function (Task 1). Both bunker's native apply AND the emitted `.devcontainer/seccomp.json` come from it; a test asserts equality.
- **bunker's native build must stay byte-for-byte behavior-identical.** It strips `firewall`/`hardening` features and ignores `runArgs`. No double-application of firewall/seccomp/apparmor. Verify with a test that `ToProjectConfig` strips both features and doesn't choke on `runArgs`.
- Fail-closed firewall semantics preserved (the scripts already set DROP policies first).
- No secrets in any committed file.

## Task Order & Dependencies

1. **Task 1 — Canonical seccomp source.** Export the seccomp profile as `container.SeccompProfileJSON()`; refactor `lifecycle.go` to use it. Foundational (Task 4 emits the file from it).
2. **Task 2 — `firewall` Feature package** + a generator that derives the scripts + builtin-domains into the package + a drift test.
3. **Task 3 — `hardening` Feature package** (bubblewrap + `apparmor=unconfined`).
4. **Task 4 — Wire `generate`/`init`.** Reference both features + `runArgs` seccomp; write `.devcontainer/seccomp.json`; verify bunker still strips + ignores.
5. **Task 5 — Opt-in publish workflow + docs.**

---

### Task 1: Export the seccomp profile as the canonical single source

**Files:**
- Modify: `internal/container/lifecycle.go` — extract the `seccompProfile` closure into an exported `SeccompProfileJSON()` (and keep `seccompProfile`/its use pointing at it).
- Test: `internal/container/lifecycle_test.go` (or a new `seccomp_test.go`).

**Interfaces:**
- Produces: `func SeccompProfileJSON() string` — returns the canonical seccomp profile as pretty-printed JSON (stable, deterministic). Used by (a) `lifecycle.go`'s native `securityOpt: seccomp=<...>` application and (b) Task 4's `.devcontainer/seccomp.json` writer.

- [ ] **Step 1: Write the failing test.**
```go
func TestSeccompProfileJSON(t *testing.T) {
	s := SeccompProfileJSON()
	var p struct {
		DefaultAction string `json:"defaultAction"`
		Syscalls      []struct {
			Names  []string `json:"names"`
			Action string   `json:"action"`
		} `json:"syscalls"`
	}
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		t.Fatalf("profile is not valid JSON: %v", err)
	}
	if p.DefaultAction != "SCMP_ACT_ALLOW" {
		t.Errorf("defaultAction = %q, want SCMP_ACT_ALLOW", p.DefaultAction)
	}
	// The denylist must block the dangerous syscalls.
	blocked := map[string]bool{}
	for _, sc := range p.Syscalls {
		if sc.Action == "SCMP_ACT_ERRNO" {
			for _, n := range sc.Names {
				blocked[n] = true
			}
		}
	}
	for _, must := range []string{"bpf", "kexec_load", "ptrace", "init_module", "reboot"} {
		if !blocked[must] {
			t.Errorf("%q must be in the seccomp denylist", must)
		}
	}
	// Deterministic: two calls return identical bytes (needed for drift stability).
	if SeccompProfileJSON() != s {
		t.Error("SeccompProfileJSON must be deterministic")
	}
}
```
- [ ] **Step 2: Run → FAIL** (`undefined: SeccompProfileJSON`). `go test ./internal/container/ -run TestSeccompProfileJSON -v`.
- [ ] **Step 3: Implement.** In `lifecycle.go`, turn the `seccompProfile` closure into an exported function that builds the same `seccompProfileDef` and returns `json.MarshalIndent(profile, "", "  ")` (pretty — this is what gets committed as `.devcontainer/seccomp.json`; Docker accepts pretty JSON). Keep the existing native use working — set `var seccompProfile = SeccompProfileJSON()` (or replace the securityOpt construction to call `SeccompProfileJSON()`). The syscall denylist + DefaultAction are UNCHANGED (same profile — this is a pure refactor to a single exported source).
- [ ] **Step 4: Tests + build.** `go test ./internal/container/ -run 'Seccomp|Lifecycle' -v`; `go build ./...`; `go test ./...`; `gofmt -l .` empty. Confirm the native seccomp application still produces the same profile (a test that `securityOpt` still contains `seccomp=` + the profile).
- [ ] **Step 5: Commit** — `git add internal/container/lifecycle.go internal/container/*_test.go && git commit -m "refactor(container): export SeccompProfileJSON() as the single source for native + portable use"`

---

### Task 2: `firewall` OCI Feature package + drift-tested derivation

**Files:**
- Create: `features/src/firewall/devcontainer-feature.json`
- Create: `features/src/firewall/install.sh`
- Create (DERIVED — generated, committed): `features/src/firewall/firewall-common.sh`, `features/src/firewall/init-firewall.sh`, `features/src/firewall/refresh-firewall.sh`, `features/src/firewall/builtin-domains.txt`
- Create: `features/src/firewall/README.md`
- Create: a generator `cmd/genfeatures/main.go` (or a `//go:generate` + a func) that copies the canonical scripts + writes `builtin-domains.txt` from `container.BuiltinDomains()` into the package.
- Test: `features/firewall_drift_test.go` (a Go test, package-external, that asserts the packaged scripts == `internal/container/scripts/*` and `builtin-domains.txt` == `container.BuiltinDomains()`).

**Interfaces:**
- Consumes: `container.BuiltinDomains() []string`; the canonical scripts in `internal/container/scripts/`; the `init-firewall.sh` contract (reads a domains file passed as `$1`; `!`-prefix = critical domain; one per line).
- Produces: a spec-conformant Feature at `ghcr.io/Devon-White/claude-bunker/firewall` (unpublished) that, for the VS Code path, installs iptables/ipset + the scripts, requests `NET_ADMIN`/`NET_RAW`, and runs the firewall on every boot with `builtins + option.allowDomains`.

- [ ] **Step 1: Write the drift test FIRST.**
```go
// features/firewall_drift_test.go
package features_test

import (
	"os"
	"strings"
	"testing"

	"github.com/Devon-White/claude-bunker/internal/container"
)

func TestFirewallFeatureScriptsMatchCanonical(t *testing.T) {
	for _, name := range []string{"init-firewall.sh", "refresh-firewall.sh", "firewall-common.sh"} {
		canonical, err := os.ReadFile("../internal/container/scripts/" + name)
		if err != nil { t.Fatal(err) }
		packaged, err := os.ReadFile("src/firewall/" + name)
		if err != nil { t.Fatalf("feature script missing (run the generator): %v", err) }
		if string(canonical) != string(packaged) {
			t.Errorf("%s drift: features/src/firewall/%s != internal/container/scripts/%s — regenerate", name, name, name)
		}
	}
}

func TestFirewallFeatureBuiltinDomainsMatchCanonical(t *testing.T) {
	packaged, err := os.ReadFile("src/firewall/builtin-domains.txt")
	if err != nil { t.Fatalf("builtin-domains.txt missing (run the generator): %v", err) }
	want := strings.Join(container.BuiltinDomains(), "\n") + "\n"
	if string(packaged) != want {
		t.Errorf("builtin-domains.txt drift vs container.BuiltinDomains() — regenerate")
	}
}
```
(Adjust the relative paths / `BuiltinDomains` signature to ground truth — read `internal/container/domains.go` first. If `BuiltinDomains` returns the `!`-prefixed critical form, mirror exactly what bunker writes to its domains file.)
- [ ] **Step 2: Run → FAIL** (files missing). `go test ./features/ -v`.
- [ ] **Step 3: Write the generator** `cmd/genfeatures/main.go` (mirror `cmd/genbuild`): reads the 3 canonical scripts from `internal/container/scripts/` and writes them verbatim into `features/src/firewall/`; writes `features/src/firewall/builtin-domains.txt` = `strings.Join(container.BuiltinDomains(), "\n")+"\n"`. Add a `//go:generate go run ./cmd/genfeatures` directive somewhere central (e.g. a `generate.go`). Run it to produce the files.
- [ ] **Step 4: Author `devcontainer-feature.json`.**
```json
{
  "id": "firewall",
  "version": "0.1.0",
  "name": "claude-bunker firewall",
  "description": "Default-deny egress firewall (iptables + ipset) with a domain allowlist, IPv6 blocked, and a startup self-test. Mirrors claude-bunker's native firewall for the VS Code / Codespaces path.",
  "options": {
    "allowDomains": {
      "type": "string",
      "default": "",
      "description": "Comma-separated extra domains to allow (added to the built-in allowlist)."
    }
  },
  "capAdd": ["NET_ADMIN", "NET_RAW"],
  "postStartCommand": "sudo /usr/local/share/claude-bunker-firewall/run-firewall.sh",
  "installsAfter": ["ghcr.io/devcontainers/features/common-utils"]
}
```
(`postStartCommand` from a Feature runs before user postStart, every boot — the proven pattern. Use `sudo` since the firewall needs root and the remoteUser is non-root; the install.sh must grant a passwordless sudoers entry for the firewall runner, OR document that the base image provides sudo. Verify the base image / common-utils provides `sudo` for the non-root user; if not, handle it.)
- [ ] **Step 5: Author `install.sh`** (runs as root at build):
```bash
#!/bin/sh
set -e
apt-get update
apt-get install -y --no-install-recommends iptables ipset dnsutils iproute2 curl
update-alternatives --set iptables /usr/sbin/iptables-legacy || true
DEST=/usr/local/share/claude-bunker-firewall
mkdir -p "$DEST"
cp firewall-common.sh init-firewall.sh refresh-firewall.sh builtin-domains.txt "$DEST/"
chmod +x "$DEST"/*.sh
# run-firewall.sh: assemble the domains file (builtins + the allowDomains option) then run init-firewall.sh.
cat > "$DEST/run-firewall.sh" <<'EOF'
#!/bin/bash
set -euo pipefail
DEST=/usr/local/share/claude-bunker-firewall
DOMAINS=/run/claude-bunker-domains
cp "$DEST/builtin-domains.txt" "$DOMAINS"
# ALLOWDOMAINS is baked in at build from the feature option (see below).
if [ -n "${ALLOWDOMAINS:-}" ]; then
  echo "$ALLOWDOMAINS" | tr ',' '\n' | sed '/^$/d' >> "$DOMAINS"
fi
"$DEST/init-firewall.sh" "$DOMAINS"
# Launch the refresh daemon (backgrounded) so CDN IP rotation is handled.
nohup "$DEST/refresh-firewall.sh" "$DOMAINS" >/dev/null 2>&1 &
EOF
chmod +x "$DEST/run-firewall.sh"
# Persist the option value so run-firewall.sh (postStart) can read it.
echo "ALLOWDOMAINS='${ALLOWDOMAINS}'" > "$DEST/allow-domains.env"
```
Options are passed to `install.sh` as UPPERCASED env vars (`ALLOWDOMAINS` for option `allowDomains`) — confirm the casing convention against the spec. Make `run-firewall.sh` source `allow-domains.env` for `ALLOWDOMAINS` (postStart doesn't get the build-time option env). Adjust `refresh-firewall.sh`'s arg contract if it differs.
- [ ] **Step 6: README.md** for the feature (id, options, what it does, capAdd rationale, that it mirrors bunker's native firewall).
- [ ] **Step 7: Verify.** `go test ./features/ -v` (drift tests pass). `go build ./...`; `go vet ./...`; `go test ./...`; `gofmt -l .` empty. `sh -n features/src/firewall/install.sh` (shell syntax check). Confirm `devcontainer-feature.json` is valid JSON.
- [ ] **Step 8: Commit** — `git add features/ cmd/genfeatures/ generate.go && git commit -m "feat(features): firewall OCI Feature (derived scripts + builtin domains, drift-tested)"`

---

### Task 3: `hardening` OCI Feature package

**Files:**
- Create: `features/src/hardening/devcontainer-feature.json`, `features/src/hardening/install.sh`, `features/src/hardening/README.md`

**Interfaces:**
- Produces: `ghcr.io/Devon-White/claude-bunker/hardening` (unpublished) — installs bubblewrap and requests `apparmor=unconfined` (the Feature-expressible hardening). The custom seccomp profile is NOT here (a Feature can't carry it) — it rides via `runArgs` (Task 4); the README states this explicitly.

- [ ] **Step 1: Author `devcontainer-feature.json`.**
```json
{
  "id": "hardening",
  "version": "0.1.0",
  "name": "claude-bunker hardening",
  "description": "Installs bubblewrap (Claude Code's inner sandbox) and relaxes AppArmor so bwrap can create user namespaces. NOTE: the custom seccomp profile cannot be shipped in a Feature (host-resolved) — it is applied via runArgs in the generated devcontainer.json.",
  "securityOpt": ["apparmor=unconfined"],
  "installsAfter": ["ghcr.io/devcontainers/features/common-utils"]
}
```
- [ ] **Step 2: Author `install.sh`.**
```bash
#!/bin/sh
set -e
apt-get update
apt-get install -y --no-install-recommends bubblewrap
```
- [ ] **Step 3: README.md** — explain the split: bubblewrap + `apparmor=unconfined` here; the custom seccomp profile via `runArgs` + `.devcontainer/seccomp.json` (link to the repo's docs), and why (Feature seccomp is host-resolved).
- [ ] **Step 4: Verify.** `sh -n features/src/hardening/install.sh`; `devcontainer-feature.json` valid JSON. `go build ./...`; `go test ./...` still green.
- [ ] **Step 5: Commit** — `git add features/src/hardening/ && git commit -m "feat(features): hardening OCI Feature (bubblewrap + apparmor=unconfined; seccomp via runArgs)"`

---

### Task 4: Wire `generate`/`init` — reference the Features + seccomp via runArgs

**Files:**
- Modify: `internal/devcontainer/generate.go` — add `FirewallFeature`/`HardeningFeature` to `GenerateOpts`; reference them in `features`; add `runArgs` with the seccomp profile path.
- Modify: `cmd/init.go` — pass the two feature refs; write `.devcontainer/seccomp.json` (from `container.SeccompProfileJSON()`) next to the devcontainer.json in `writeDevContainer`.
- Test: `internal/devcontainer/generate_test.go` (feature refs + runArgs present); `internal/devcontainer/load_test.go` (ToProjectConfig strips firewall/hardening + ignores runArgs); a cmd/init test that seccomp.json is written.

**Interfaces:**
- Consumes: `container.SeccompProfileJSON()`; the existing `Generate`/`writeDevContainer` flow; `stripBunkerFeatures`.
- Produces: a generated `devcontainer.json` that references `ghcr.io/Devon-White/claude-bunker/firewall:0` + `.../hardening:0`, carries `runArgs: ["--security-opt", "seccomp=./.devcontainer/seccomp.json"]`, and a sibling `.devcontainer/seccomp.json`. Bunker strips the two features and ignores runArgs (its own native firewall/seccomp/apparmor unchanged).

- [ ] **Step 1: Write failing tests.** (a) `Generate` with `FirewallFeature`/`HardeningFeature` set → the `features` map contains both refs and `dc["runArgs"]` == `["--security-opt", "seccomp=./.devcontainer/seccomp.json"]`. (b) `ToProjectConfig`/`stripBunkerFeatures` on a config that lists firewall+hardening → both stripped from the engine features, and a devcontainer.json containing a `runArgs` key parses without error (unknown key ignored). Read `internal/devcontainer/generate_test.go` + `load_test.go` for the harness first.
- [ ] **Step 2: Run → FAIL.**
- [ ] **Step 3: Implement `generate.go`.** Add `FirewallFeature string` and `HardeningFeature string` to `GenerateOpts`. In `Generate`, after adding `ClaudeCodeFeature`, add each non-empty feature ref to `features` (value `map[string]any{}`, or `{"allowDomains": strings.Join(cfg.AllowDomains, ",")}` for firewall so VS Code gets the same allowlist — decide and note). Add:
```go
dc["runArgs"] = []string{"--security-opt", "seccomp=./.devcontainer/seccomp.json"}
```
(Only when hardening/seccomp is enabled — i.e. always, for a bunkered project. Confirm the relative path form VS Code expects — `./.devcontainer/seccomp.json` resolves relative to the devcontainer.json's folder / workspace; verify against the spec and use the correct form, possibly `${localWorkspaceFolder}/.devcontainer/seccomp.json`.)
- [ ] **Step 4: Implement `init.go` `writeDevContainer`.** Pass `FirewallFeature: "ghcr.io/Devon-White/claude-bunker/firewall:0"`, `HardeningFeature: "ghcr.io/Devon-White/claude-bunker/hardening:0"` (mirror the existing `ClaudeCodeFeature` line at init.go:584). After writing `devcontainer.json`, also write `.devcontainer/seccomp.json` with `container.SeccompProfileJSON()` (0644). (Import `internal/container` — confirm no import cycle: `cmd` already imports `internal/container`, fine.)
- [ ] **Step 5: Verify no double-apply / no regression.** Confirm (test + reasoning) bunker's native path: `stripBunkerFeatures` removes firewall+hardening (so bunker doesn't try to resolve them); the read struct has no `runArgs` (so runArgs is inert for bunker); bunker still applies its native seccomp/apparmor/firewall via `internal/container`. The generated file's `runArgs`/features are ONLY consumed by VS Code.
- [ ] **Step 6: Manual smoke.** `go build -o /tmp/cb2b . && WS=$(mktemp -d) && CLAUDE_BUNKER_WS=$WS /tmp/cb2b init --defaults` → inspect `$WS/.devcontainer/`: `devcontainer.json` references firewall+hardening features + has `runArgs`; `seccomp.json` exists and is valid JSON matching `SeccompProfileJSON()`. Report.
- [ ] **Step 7: Tests + build + commit.** All green; `gofmt` clean. `git add internal/devcontainer/generate.go cmd/init.go internal/devcontainer/*_test.go cmd/*_test.go && git commit -m "feat(init): generated devcontainer.json references firewall/hardening features + seccomp via runArgs (VS Code portability)"`

---

### Task 5: Opt-in publish workflow + docs

**Files:**
- Create: `.github/workflows/publish-features.yml` (does NOT auto-fire; `workflow_dispatch` + optionally `push: tags: features-v*`).
- Modify: `README.md`, `CLAUDE.md`.

**Interfaces:**
- Produces: a ready-but-inert workflow that publishes `features/src/*` to `ghcr.io/Devon-White/claude-bunker/*` when manually triggered; docs explaining the portability model + the manual publish steps.

- [ ] **Step 1: Publish workflow.** Use the official `devcontainers/action` publish action (or `oras`) with `permissions: { packages: write, contents: read }`, `on: { workflow_dispatch: {}, push: { tags: ["features-v*"] } }`, `base-path-to-features: features/src`. It must NOT run on normal pushes/PRs (that's `ci.yml`'s job) — trigger is dispatch/feature-tag only. Add a comment: publishing requires the packages to be made public in GHCR after first push (a one-time manual step).
- [ ] **Step 2: Docs.** README: a "Portability to VS Code / Codespaces" section — a bunkered repo opens in VS Code and gets the firewall (via the `firewall` Feature), bubblewrap + apparmor (via the `hardening` Feature), and the custom seccomp profile (via `runArgs` + `.devcontainer/seccomp.json`); note dynamic tmpfs-excludes are bunker-only. State that the Features are published from `features/src/` and that the portable artifacts are generated from bunker's canonical sources (so they never drift). CLAUDE.md: add a short "OCI Features / portability" note to the architecture section (the two features, the seccomp-via-runArgs, the generator + drift tests, `cmd/genfeatures`).
- [ ] **Step 3: Verify.** YAML parses (`python3 -c "import yaml;yaml.safe_load(open('.github/workflows/publish-features.yml'))"`); docs grep-check the feature refs + seccomp story; existing `ci.yml`/`release.yml` untouched.
- [ ] **Step 4: Commit** — `git add .github/workflows/publish-features.yml README.md CLAUDE.md && git commit -m "ci+docs: opt-in feature publish workflow + VS Code portability docs"`

---

## Self-Review Checklist

- Every portable artifact (scripts, builtin domains, seccomp profile) is DERIVED from a single canonical Go/script source with a CI-gating drift test — no hand-maintained copies.
- bunker's native build is behavior-identical: strips firewall/hardening, ignores runArgs, applies its own firewall/seccomp/apparmor. No double-apply.
- VS Code path gets: firewall Feature (capAdd + scripts + boot run w/ builtins+allowDomains), hardening Feature (bwrap + apparmor=unconfined), seccomp via runArgs + committed profile.
- Features are NOT added to `bunkerManagedFeaturePrefixes` beyond what's already there (they're already listed — bunker strips them; good).
- Publish workflow is opt-in and does not fire on normal pushes; nothing is published in this phase.
- The seccomp `runArgs` path form is the one VS Code actually resolves (verified against the spec).
