package container

// RootUser is the root user inside the container, used for privileged exec operations.
const RootUser = "root"

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

// RefreshFirewallScriptPath is the install path for refresh-firewall.sh inside the container.
const RefreshFirewallScriptPath = "/usr/local/bin/refresh-firewall.sh"

// CommonFirewallScriptPath is the install path for firewall-common.sh inside the container.
// Sourced by both init-firewall.sh and refresh-firewall.sh for shared DNS/ipset helpers.
const CommonFirewallScriptPath = "/usr/local/bin/firewall-common.sh"

// DomainsFilePath is the temp file where the Go binary writes the firewall
// domain list for init-firewall.sh to read.
const DomainsFilePath = "/tmp/.bunker-domains"

// ContainerUserGroup is the "user:group" string used for chown operations.
const ContainerUserGroup = ContainerUser + ":" + ContainerUser

// ManagedSettingsDir is the directory where Claude Code reads managed settings.
// The actual managed-settings.json is written at container start (not build time)
// so it can include the dynamic domain allowlist from project config.
const ManagedSettingsDir = "/etc/claude-code"

// AuthWrapperPath is the path to the auth wrapper script inside the container.
// The wrapper reads secrets from tmpfs and exports them as env vars before
// exec-ing the claude command.
const AuthWrapperPath = ContainerHome + "/.claude-auth-wrapper.sh"

// LabelKey is the Docker label used to identify claude-bunker containers.
const LabelKey = "claude-bunker"
