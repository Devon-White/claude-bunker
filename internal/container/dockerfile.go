package container

import (
	"fmt"
	"strings"
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

// GenerateBaseDockerfile produces the complete base Dockerfile as a string,
// ending with USER claude-bunker. Used for fingerprinting and standalone builds.
func GenerateBaseDockerfile() string {
	return generateBaseContent() + fmt.Sprintf("USER %s\n\n", ContainerUser)
}

// generateBaseContent produces the Dockerfile content without the final USER
// line. Used by GenerateBaseDockerfile (which appends USER) and by buildLocal
// when merging with dynamic layers (GenerateDockerfile appends USER).
func generateBaseContent() string {
	u := ContainerUser
	h := ContainerHome
	ws := ContainerWorkspace
	hist := CommandHistoryDir

	var b strings.Builder

	// --- Base image ---
	b.WriteString("FROM debian:bookworm-slim\n\n")

	// --- Create non-root user ---
	fmt.Fprintf(&b, "# Create %s user\n", u)
	fmt.Fprintf(&b, "RUN groupadd --gid 1000 %s && \\\n", u)
	fmt.Fprintf(&b, "  useradd --uid 1000 --gid 1000 -m -s /bin/bash %s\n\n", u)

	// --- Main apt-get install ---
	b.WriteString("# Install development tools, iptables for firewall, and utilities\n")
	b.WriteString("RUN apt-get update && apt-get install -y --no-install-recommends \\\n")
	b.WriteString("  bubblewrap \\\n")
	b.WriteString("  ca-certificates \\\n")
	b.WriteString("  curl \\\n")
	b.WriteString("  dnsutils \\\n")
	b.WriteString("  gh \\\n")
	b.WriteString("  git \\\n")
	b.WriteString("  ipset \\\n")
	b.WriteString("  iptables \\\n")
	b.WriteString("  iproute2 \\\n")
	b.WriteString("  less \\\n")
	b.WriteString("  tmux \\\n")
	b.WriteString("  && apt-get clean && rm -rf /var/lib/apt/lists/* \\\n")
	b.WriteString("  && update-alternatives --set iptables /usr/sbin/iptables-legacy\n\n")

	// --- Timezone (AFTER apt-get to avoid cache busting) ---
	b.WriteString("# Timezone — placed after apt-get so TZ changes don't bust the package cache\n")
	b.WriteString("ARG TZ=UTC\n")
	b.WriteString("ENV TZ=\"$TZ\"\n\n")

	// --- Bash history + workspace (merged into 1 RUN) ---
	fmt.Fprintf(&b, "ARG USERNAME=%s\n", u)
	b.WriteString("# Persist bash history, create dirs\n")
	fmt.Fprintf(&b, "RUN SNIPPET=\"export PROMPT_COMMAND='history -a' && export HISTFILE=%s/.bash_history\" \\\n", hist)
	fmt.Fprintf(&b, "  && mkdir %s \\\n", hist)
	fmt.Fprintf(&b, "  && touch %s/.bash_history \\\n", hist)
	fmt.Fprintf(&b, "  && chown -R $USERNAME %s \\\n", hist)
	fmt.Fprintf(&b, "  && echo \"$SNIPPET\" >> \"%s/.bashrc\" \\\n", h)
	fmt.Fprintf(&b, "  && mkdir -p %s %s/.claude \\\n", ws, h)
	fmt.Fprintf(&b, "  && chown -R %s:%s %s %s/.claude \\\n", u, u, ws, h)
	fmt.Fprintf(&b, "  && mkdir -p %s\n\n", ManagedSettingsDir)

	fmt.Fprintf(&b, "WORKDIR %s\n\n", ws)

	// --- Switch to non-root user for Claude Code install ---
	b.WriteString("# Switch to non-root user for Claude Code install\n")
	fmt.Fprintf(&b, "USER %s\n\n", u)

	// --- Environment variables ---
	b.WriteString("ENV DEVCONTAINER=true\n")
	b.WriteString("ENV SHELL=/bin/bash\n\n")

	// --- Claude Code install (runs as non-root user) ---
	b.WriteString("# Install Claude Code via native installer\n")
	b.WriteString("RUN curl -fsSL https://claude.ai/install.sh | bash\n")
	fmt.Fprintf(&b, "ENV PATH=\"%s/.local/bin:$PATH\"\n\n", h)

	// --- COPY layers AFTER expensive installs (prevents cache busting) ---
	b.WriteString("# COPY layers placed after expensive installs to avoid cache busting\n")
	b.WriteString("USER root\n")
	fmt.Fprintf(&b, "COPY firewall-common.sh %s\n", CommonFirewallScriptPath)
	fmt.Fprintf(&b, "COPY init-firewall.sh %s\n", FirewallScriptPath)
	fmt.Fprintf(&b, "COPY refresh-firewall.sh %s\n", RefreshFirewallScriptPath)
	fmt.Fprintf(&b, "COPY tmux.conf %s/.tmux.conf\n", h)
	fmt.Fprintf(&b, "RUN chmod +x %s %s %s && \\\n", CommonFirewallScriptPath, FirewallScriptPath, RefreshFirewallScriptPath)
	fmt.Fprintf(&b, "  chown %s:%s %s/.tmux.conf\n\n", u, u, h)

	return b.String()
}
