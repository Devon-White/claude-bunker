package container

import (
	"fmt"
	"strings"
)

// Version constants — single source of truth for all build-time versions.
const (
	GitDeltaVersion    = "0.18.2"
	ZshInDockerVersion = "1.2.0"
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

// GenerateBaseDockerfile produces the complete base Dockerfile as a string.
// This replaces the static .devcontainer/Dockerfile entirely.
//
// Build optimizations applied vs the original:
//   - ARG TZ / ENV TZ moved after apt-get install (prevents cache busting on TZ change)
//   - wget removed from apt-get (only curl is used)
//   - man-db index update suppressed via debconf-set-selections
//   - User creation + debconf merged into single RUN layer
//   - Bash history + workspace + sudoers merged into single RUN layer
//   - COPY layers (init-firewall.sh, tmux.conf) placed AFTER expensive network installs
//     (zsh-in-docker, Claude Code) to prevent cache busting on script changes
//   - git-delta and zsh-in-docker use curl instead of wget
//   - Version constants defined in Go (single source of truth)
//   - /etc/claude-code dir created for managed-settings.json (written at container start)
func GenerateBaseDockerfile() string {
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

	// --- Switch to non-root user for expensive network installs ---
	b.WriteString("# Switch to non-root user for zsh and Claude Code installs\n")
	b.WriteString("USER claude-bunker\n\n")

	// --- Environment variables ---
	b.WriteString("ENV DEVCONTAINER=true\n")
	b.WriteString("ENV SHELL=/bin/zsh\n")
	b.WriteString("ENV EDITOR=nano\n")
	b.WriteString("ENV VISUAL=nano\n\n")

	// --- zsh-in-docker (runs as non-root user, using curl instead of wget) ---
	b.WriteString(fmt.Sprintf("ARG ZSH_IN_DOCKER_VERSION=%s\n", ZshInDockerVersion))
	b.WriteString("RUN sh -c \"$(curl -fsSL https://github.com/deluan/zsh-in-docker/releases/download/v${ZSH_IN_DOCKER_VERSION}/zsh-in-docker.sh)\" -- \\\n")
	b.WriteString("  -p git \\\n")
	b.WriteString("  -p fzf \\\n")
	b.WriteString("  -a \"source /usr/share/doc/fzf/examples/key-bindings.zsh\" \\\n")
	b.WriteString("  -a \"source /usr/share/doc/fzf/examples/completion.zsh\" \\\n")
	b.WriteString("  -a \"export PROMPT_COMMAND='history -a' && export HISTFILE=/commandhistory/.bash_history\" \\\n")
	b.WriteString("  -x\n\n")

	// --- Claude Code install (runs as non-root user) ---
	b.WriteString("# Install Claude Code via native installer\n")
	b.WriteString("RUN curl -fsSL https://claude.ai/install.sh | bash\n")
	b.WriteString("ENV PATH=\"/home/claude-bunker/.local/bin:$PATH\"\n\n")

	// --- COPY layers AFTER expensive installs (prevents cache busting) ---
	b.WriteString("# COPY layers placed after expensive installs to avoid cache busting\n")
	b.WriteString("USER root\n")
	b.WriteString("COPY init-firewall.sh /usr/local/bin/\n")
	b.WriteString("COPY tmux.conf /home/claude-bunker/.tmux.conf\n")
	b.WriteString("RUN chmod +x /usr/local/bin/init-firewall.sh && \\\n")
	b.WriteString("  chown claude-bunker:claude-bunker /home/claude-bunker/.tmux.conf\n\n")

	// --- Final user switch back to non-root ---
	b.WriteString("USER claude-bunker\n\n")

	return b.String()
}
