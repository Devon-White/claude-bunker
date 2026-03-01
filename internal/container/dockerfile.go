package container

import (
	"fmt"
	"strings"
)

// Version constants — single source of truth for all build-time versions.
const (
	GitDeltaVersion = "0.18.2"
)

// BaseImageRegistry is the GHCR registry for pre-built base images.
const BaseImageRegistry = "ghcr.io/devon-white/claude-bunker"

// BaseImageRef returns the full image reference for a given version.
// Returns "" for dev builds (no pre-built image available).
func BaseImageRef(version string) string {
	if version == "" || version == "dev" {
		return ""
	}
	return BaseImageRegistry + ":v" + version
}

// ManagedSettingsDir is the directory where Claude Code reads managed settings.
// The actual managed-settings.json is written at container start (not build time)
// so it can include the dynamic domain allowlist from project config.
const ManagedSettingsDir = "/etc/claude-code"

// GenerateBaseDockerfile produces the complete base Dockerfile as a string,
// ending with USER claude-bunker. Used for fingerprinting and standalone builds.
func GenerateBaseDockerfile() string {
	return generateBaseContent() + fmt.Sprintf("USER %s\n\n", ContainerUser)
}

// generateBaseContent produces the Dockerfile content without the final USER
// line. Used by GenerateBaseDockerfile (which appends USER) and by buildLocal
// when merging with dynamic layers (GenerateDockerfile appends USER).
//
// Build optimizations applied vs the original:
//   - ARG TZ / ENV TZ moved after apt-get install (prevents cache busting on TZ change)
//   - wget removed from apt-get (only curl is used)
//   - User creation + debconf merged into single RUN layer
//   - Bash history + workspace + sudoers merged into single RUN layer
//   - COPY layers (init-firewall.sh, tmux.conf, zshrc) placed AFTER expensive network installs
//     to prevent cache busting on script changes
//   - Minimal .zshrc replaces Oh My Zsh / zsh-in-docker (~30MB clone eliminated)
//   - Version constants defined in Go (single source of truth)
//   - /etc/claude-code dir created for managed-settings.json (written at container start)
func generateBaseContent() string {
	var b strings.Builder

	// --- Base image ---
	b.WriteString("FROM debian:bookworm-slim\n\n")

	// --- Create non-root user ---
	b.WriteString("# Create claude-bunker user\n")
	b.WriteString("RUN groupadd --gid 1000 claude-bunker && \\\n")
	b.WriteString("  useradd --uid 1000 --gid 1000 -m -s /bin/bash claude-bunker\n\n")

	// --- Main apt-get install (no wget, TZ moved after) ---
	// Removed: vim (nano is default EDITOR), gnupg2 (GPG signing unused),
	// procps (Docker API used instead), unzip (unused), socat (unused),
	// man-db (indexing was already suppressed).
	b.WriteString("# Install development tools, iptables for firewall, and utilities\n")
	b.WriteString("RUN apt-get update && apt-get install -y --no-install-recommends \\\n")
	b.WriteString("  bubblewrap \\\n")
	b.WriteString("  ca-certificates \\\n")
	b.WriteString("  curl \\\n")
	b.WriteString("  dnsutils \\\n")
	b.WriteString("  fzf \\\n")
	b.WriteString("  gh \\\n")
	b.WriteString("  git \\\n")
	b.WriteString("  aggregate \\\n")
	b.WriteString("  iptables \\\n")
	b.WriteString("  iproute2 \\\n")
	b.WriteString("  jq \\\n")
	b.WriteString("  less \\\n")
	b.WriteString("  nano \\\n")
	b.WriteString("  sudo \\\n")
	b.WriteString("  tmux \\\n")
	b.WriteString("  zsh \\\n")
	b.WriteString("  && apt-get clean && rm -rf /var/lib/apt/lists/* \\\n")
	b.WriteString("  && update-alternatives --set iptables /usr/sbin/iptables-legacy\n\n")

	// --- Timezone (AFTER apt-get to avoid cache busting) ---
	b.WriteString("# Timezone — placed after apt-get so TZ changes don't bust the package cache\n")
	b.WriteString("ARG TZ=UTC\n")
	b.WriteString("ENV TZ=\"$TZ\"\n\n")

	// --- Bash history + workspace + sudoers (merged into 1 RUN) ---
	b.WriteString("ARG USERNAME=claude-bunker\n")
	b.WriteString("# Persist bash history, create dirs, configure sudoers\n")
	b.WriteString("RUN SNIPPET=\"export PROMPT_COMMAND='history -a' && export HISTFILE=/commandhistory/.bash_history\" \\\n")
	b.WriteString("  && mkdir /commandhistory \\\n")
	b.WriteString("  && touch /commandhistory/.bash_history \\\n")
	b.WriteString("  && chown -R $USERNAME /commandhistory \\\n")
	b.WriteString("  && echo \"$SNIPPET\" >> \"/home/$USERNAME/.bashrc\" \\\n")
	b.WriteString("  && mkdir -p /workspace /home/claude-bunker/.claude \\\n")
	b.WriteString("  && chown -R claude-bunker:claude-bunker /workspace /home/claude-bunker/.claude \\\n")
	b.WriteString("  && mkdir -p /etc/claude-code \\\n")
	b.WriteString("  && echo \"claude-bunker ALL=(root) NOPASSWD: /usr/local/bin/init-firewall.sh\" > /etc/sudoers.d/claude-bunker-firewall \\\n")
	b.WriteString("  && chmod 0440 /etc/sudoers.d/claude-bunker-firewall\n\n")

	b.WriteString("WORKDIR /workspace\n\n")

	// --- git-delta install (still root, using curl instead of wget) ---
	b.WriteString(fmt.Sprintf("ARG GIT_DELTA_VERSION=%s\n", GitDeltaVersion))
	b.WriteString("RUN ARCH=$(dpkg --print-architecture) && \\\n")
	b.WriteString("  curl -fsSL -o \"/tmp/git-delta_${GIT_DELTA_VERSION}_${ARCH}.deb\" \\\n")
	b.WriteString("    \"https://github.com/dandavison/delta/releases/download/${GIT_DELTA_VERSION}/git-delta_${GIT_DELTA_VERSION}_${ARCH}.deb\" && \\\n")
	b.WriteString("  dpkg -i \"/tmp/git-delta_${GIT_DELTA_VERSION}_${ARCH}.deb\" && \\\n")
	b.WriteString("  rm \"/tmp/git-delta_${GIT_DELTA_VERSION}_${ARCH}.deb\"\n\n")

	// --- Switch to non-root user for Claude Code install ---
	b.WriteString("# Switch to non-root user for Claude Code install\n")
	b.WriteString("USER claude-bunker\n\n")

	// --- Environment variables ---
	b.WriteString("ENV DEVCONTAINER=true\n")
	b.WriteString("ENV SHELL=/bin/zsh\n")
	b.WriteString("ENV EDITOR=nano\n")
	b.WriteString("ENV VISUAL=nano\n\n")

	// --- Minimal zsh config (replaces Oh My Zsh / zsh-in-docker) ---
	// A small .zshrc provides fzf integration, completion, history, and a prompt
	// without cloning the ~30MB Oh My Zsh repo or depending on external scripts.

	// --- Claude Code install (runs as non-root user) ---
	b.WriteString("# Install Claude Code via native installer\n")
	b.WriteString("RUN curl -fsSL https://claude.ai/install.sh | bash\n")
	b.WriteString("ENV PATH=\"/home/claude-bunker/.local/bin:$PATH\"\n\n")

	// --- COPY layers AFTER expensive installs (prevents cache busting) ---
	b.WriteString("# COPY layers placed after expensive installs to avoid cache busting\n")
	b.WriteString("USER root\n")
	b.WriteString("COPY init-firewall.sh /usr/local/bin/\n")
	b.WriteString("COPY tmux.conf /home/claude-bunker/.tmux.conf\n")
	b.WriteString("COPY zshrc /home/claude-bunker/.zshrc\n")
	b.WriteString("RUN chmod +x /usr/local/bin/init-firewall.sh && \\\n")
	b.WriteString("  chown claude-bunker:claude-bunker /home/claude-bunker/.tmux.conf /home/claude-bunker/.zshrc && \\\n")
	b.WriteString("  chsh -s /bin/zsh claude-bunker\n\n")

	return b.String()
}
