# claude-bunker hardening

## id: `hardening`

Installs bubblewrap (Claude Code's inner sandbox) and requests AppArmor to be
relaxed (`apparmor=unconfined`) so bubblewrap can create user namespaces for
restricted execution within the container.

## What it does

- **Build time** (`install.sh`, runs as root): installs `bubblewrap` via apt.

## The custom seccomp profile

This Feature installs **only** the bubblewrap binary and relaxes AppArmor. The
custom seccomp profile for strict syscall filtering is **not** part of this
Feature (the Dev Container Feature spec does not support carrying custom
seccomp files — `seccomp` values are host-resolved paths, not embedded).

The custom seccomp profile is instead applied via `runArgs` in the generated
`devcontainer.json` — this bypasses the Feature spec's limitations and ensures
every project gets the strict sandbox, whether run through `claude-bunker` CLI
or through VS Code / Codespaces.

**See:** `CLAUDE.md` (claude-bunker repository) for security layer details.

## capAdd rationale

This Feature does **not** declare `capAdd` — `bubblewrap` itself does not
require extra Linux capabilities. Capabilities are only needed if the inner
sandboxed process needs them; claude-bunker's seccomp profile denies most
syscalls and Claude Code does not require them.

## Requirements

- `installsAfter: ["ghcr.io/devcontainers/features/common-utils"]` so the base
  image is properly configured before this Feature runs.
