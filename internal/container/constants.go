package container

// ContainerUser is the non-root user created inside the sandbox container.
const ContainerUser = "claude-bunker"

// ContainerHome is the home directory for the container user.
const ContainerHome = "/home/" + ContainerUser

// ContainerWorkspace is the mount target for the host project directory.
const ContainerWorkspace = "/workspace"

// CommandHistoryDir is the mount target for the persistent bash history volume.
const CommandHistoryDir = "/commandhistory"

// SecretsDir is the tmpfs mount for auth tokens (never persisted to disk).
const SecretsDir = "/run/secrets"

// FirewallScriptPath is the install path for init-firewall.sh inside the container.
const FirewallScriptPath = "/usr/local/bin/init-firewall.sh"

// DomainsFilePath is the temp file where the Go binary writes the firewall
// domain list for init-firewall.sh to read.
const DomainsFilePath = "/tmp/.bunker-domains"

// ContainerUserGroup is the "user:group" string used for chown operations.
const ContainerUserGroup = ContainerUser + ":" + ContainerUser

// ManagedSettingsDir is the directory where Claude Code reads managed settings.
// The actual managed-settings.json is written at container start (not build time)
// so it can include the dynamic domain allowlist from project config.
const ManagedSettingsDir = "/etc/claude-code"

// LabelKey is the Docker label used to identify claude-bunker containers.
const LabelKey = "claude-bunker"
