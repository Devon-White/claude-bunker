# Claude Bunker — Refactoring Overview

Slimmed the tool by removing over-engineering in the firewall script, build pipeline, and package list while keeping kernel-level security intact.

## Firewall Script (init-firewall.sh)

**298 → 199 lines**

- Removed GitHub meta API call and `aggregate` CIDR processing — `github.com` and `api.github.com` are now resolved via DNS like every other domain
- Removed parallel DNS resolution infrastructure (temp dir, background jobs, `wait`) — replaced with a single `resolve_and_allow()` function that resolves and adds iptables rules inline
- Removed `jq` dependency (only existed for GitHub API JSON parsing)
- Added `timeout 60` wrapper to prevent indefinite hangs
- Kept: fail-closed DROP policies, Docker DNS preservation, IPv6 default-deny, bootstrap HTTPS rule, ESTABLISHED/RELATED tracking, critical domain check, final policy verification

## Dockerfile (dockerfile.go)

**Removed 7 packages:**

| Package | Reason |
|---------|--------|
| `aggregate` | Only used by GitHub meta API (removed) |
| `jq` | Only used to parse GitHub API response (removed) |
| `sudo` | Dead code — firewall runs as root via Docker exec API |
| `zsh` | Bash is sufficient; Claude Code execs via `sh -c` |
| `fzf` | Interactive shell convenience; primary user is Claude Code |
| `nano` | Claude Code is the editor |
| `git-delta` | Removed entire download layer (`ARG GIT_DELTA_VERSION` + dpkg block) |

**Final apt list (10 packages):**
```
bubblewrap ca-certificates curl dnsutils gh git iptables iproute2 less tmux
```

**Other removals:**
- `COPY zshrc` + embedded `.zshrc` file + `chsh -s /bin/zsh`
- `EDITOR=nano` and `VISUAL=nano` env vars
- Sudoers RUN line
- `SHELL=/bin/zsh` changed to `SHELL=/bin/bash`

## Build Pipeline (build.go)

**346 → 257 lines**

- Replaced 3-path build system (`PullBaseImage` → tag, `PullBaseImage` → `buildDynamicLayers`, `buildLocal`) with a single local build path
- Added `--cache-from=ghcr.io/devon-white/claude-bunker:v{VER}` for layer reuse
- GHCR cache pull runs concurrently with build context preparation (non-blocking)
- Removed `PullBaseImage()`, `buildDynamicLayers()`, `BuildResult` struct
- Added build progress output: `Step N/M` lines emitted in non-verbose mode via callback
- Removed `GIT_DELTA_VERSION` build arg

## Fingerprint (fingerprint.go)

- Removed `baseImageDigest` parameter from `ImageFingerprint()`, `CompareFingerprints()`, `CombinedFingerprint()`, `SaveCombinedFingerprint()`
- Removed `LoadBaseImageDigest()`, `SaveBaseImageDigest()`, `BaseImageDigestPath()`
- Simplified fingerprint to always hash: Dockerfile + scripts + apt + features + env
- Added `ClearFingerprint()` for `--rebuild` support

## CLI Changes (cmd/)

**New flag: `--rebuild`**
- Deletes fingerprint cache, removes existing image and container
- Forces a full fresh build

**Removed flag: `--no-teardown`**
- Consolidated into `--keep` (which already existed)

**Shell change:**
- `claude-bunker shell` now runs `bash` instead of `zsh`
- Session detection updated accordingly

## Post-Start Timeout (lifecycle.go)

- `RunPostStart()` wraps context with 90-second timeout
- Prevents indefinite hangs during container initialization

## Image Pruning (prune.go)

- `claude-bunker prune` now lists and removes `claude-bunker:*` images alongside volumes
- Interactive selection with size display

## Files Changed

| File | Change |
|------|--------|
| `internal/container/scripts/init-firewall.sh` | Rewrite: removed GitHub API, parallel DNS, aggregate |
| `internal/container/scripts/zshrc` | Deleted |
| `internal/container/dockerfile.go` | Removed 7 packages, git-delta layer, zshrc, sudoers, editor env |
| `internal/container/build.go` | Single build path + `--cache-from`, concurrent cache pull, progress output |
| `internal/container/embed.go` | Removed `ZshRC()` |
| `internal/container/lifecycle.go` | Added 90s timeout to `RunPostStart` |
| `internal/container/volumes.go` | Added `ListBunkerImages`, `RemoveImageByTag` |
| `internal/config/fingerprint.go` | Removed baseImageDigest tracking, added `ClearFingerprint` |
| `cmd/run.go` | Added `--rebuild`, removed `--no-teardown` and baseImageDigest |
| `cmd/prune.go` | Added image pruning |
| `cmd/shell.go` | `zsh` → `bash` |
| `cmd/status.go` | Session detection `zsh` → `bash` |
| `cmd/root.go` | Removed zshrc from `--dump-dockerfile` |
| `*_test.go` | Updated to match all changes above |
