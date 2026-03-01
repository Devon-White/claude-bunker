FROM debian:bookworm-slim

# Create claude-bunker user and suppress man-db index update
RUN groupadd --gid 1000 claude-bunker && \
  useradd --uid 1000 --gid 1000 -m -s /bin/bash claude-bunker && \
  echo 'man-db man-db/auto-update boolean false' | debconf-set-selections

# Install development tools, iptables for firewall, and utilities
RUN apt-get update && apt-get install -y --no-install-recommends \
  ca-certificates \
  curl \
  dnsutils \
  fzf \
  gh \
  git \
  gnupg2 \
  aggregate \
  iptables \
  iproute2 \
  jq \
  less \
  man-db \
  nano \
  procps \
  socat \
  sudo \
  tmux \
  unzip \
  vim \
  zsh \
  bubblewrap \
  && apt-get clean && rm -rf /var/lib/apt/lists/*

# Timezone — placed after apt-get so TZ changes don't bust the package cache
ARG TZ=UTC
ENV TZ="$TZ"

ARG USERNAME=claude-bunker
# Persist bash history, create dirs, bake managed-settings, configure sudoers
RUN SNIPPET="export PROMPT_COMMAND='history -a' && export HISTFILE=/commandhistory/.bash_history" \
  && mkdir /commandhistory \
  && touch /commandhistory/.bash_history \
  && chown -R $USERNAME /commandhistory \
  && echo "$SNIPPET" >> "/home/$USERNAME/.bashrc" \
  && mkdir -p /workspace /home/claude-bunker/.claude \
  && chown -R claude-bunker:claude-bunker /workspace /home/claude-bunker/.claude \
  && mkdir -p /etc/claude-code \
  && printf '%s\n' '{"sandbox":{"enabled":true,"allowUnsandboxedCommands":false,"enableWeakerNestedSandbox":true}}' > /etc/claude-code/managed-settings.json \
  && chmod 444 /etc/claude-code/managed-settings.json \
  && echo "claude-bunker ALL=(root) NOPASSWD: /usr/local/bin/init-firewall.sh" > /etc/sudoers.d/claude-bunker-firewall \
  && chmod 0440 /etc/sudoers.d/claude-bunker-firewall

WORKDIR /workspace

ARG GIT_DELTA_VERSION=0.18.2
RUN ARCH=$(dpkg --print-architecture) && \
  curl -fsSL -o "/tmp/git-delta_${GIT_DELTA_VERSION}_${ARCH}.deb" \
    "https://github.com/dandavison/delta/releases/download/${GIT_DELTA_VERSION}/git-delta_${GIT_DELTA_VERSION}_${ARCH}.deb" && \
  dpkg -i "/tmp/git-delta_${GIT_DELTA_VERSION}_${ARCH}.deb" && \
  rm "/tmp/git-delta_${GIT_DELTA_VERSION}_${ARCH}.deb"

# Switch to non-root user for zsh and Claude Code installs
USER claude-bunker

ENV DEVCONTAINER=true
ENV SHELL=/bin/zsh
ENV EDITOR=nano
ENV VISUAL=nano

ARG ZSH_IN_DOCKER_VERSION=1.2.0
RUN sh -c "$(curl -fsSL https://github.com/deluan/zsh-in-docker/releases/download/v${ZSH_IN_DOCKER_VERSION}/zsh-in-docker.sh)" -- \
  -p git \
  -p fzf \
  -a "source /usr/share/doc/fzf/examples/key-bindings.zsh" \
  -a "source /usr/share/doc/fzf/examples/completion.zsh" \
  -a "export PROMPT_COMMAND='history -a' && export HISTFILE=/commandhistory/.bash_history" \
  -x

ARG CLAUDE_CODE_VERSION=stable
# Install Claude Code via native installer (version-pinned for reproducible builds)
RUN curl -fsSL https://claude.ai/install.sh | bash -s "$CLAUDE_CODE_VERSION"
ENV PATH="/home/claude-bunker/.local/bin:$PATH"

# COPY layers placed after expensive installs to avoid cache busting
USER root
COPY init-firewall.sh /usr/local/bin/
COPY tmux.conf /home/claude-bunker/.tmux.conf
RUN chmod +x /usr/local/bin/init-firewall.sh && \
  chown claude-bunker:claude-bunker /home/claude-bunker/.tmux.conf

USER claude-bunker

