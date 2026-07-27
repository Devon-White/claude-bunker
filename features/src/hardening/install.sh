#!/bin/sh
# claude-bunker hardening — Dev Container Feature install script.
#
# Installs bubblewrap, which is Claude Code's inner sandbox. The custom
# seccomp profile for strict syscall filtering cannot be shipped in a Feature
# (it is host-resolved), so it is applied separately via runArgs in the
# generated devcontainer.json — see README.md.
set -e

apt-get update
apt-get install -y --no-install-recommends bubblewrap
apt-get clean
rm -rf /var/lib/apt/lists/*
