package container

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"

	"github.com/Devon-White/claude-bunker/internal/config"
	"github.com/Devon-White/claude-bunker/internal/log"
)

// seccompArch, seccompSyscall, and seccompProfileDef describe the shape of a
// Docker/OCI seccomp profile document.
type seccompArch struct {
	Arch     string   `json:"architecture"`
	SubArchs []string `json:"subArchitectures,omitempty"`
}

type seccompSyscall struct {
	Names  []string `json:"names"`
	Action string   `json:"action"`
}

type seccompProfileDef struct {
	DefaultAction string           `json:"defaultAction"`
	Architectures []seccompArch    `json:"architectures,omitempty"`
	Syscalls      []seccompSyscall `json:"syscalls"`
}

// SeccompProfileJSON returns the canonical claude-bunker seccomp profile as
// pretty-printed, deterministic JSON. It is a custom seccomp profile that
// allows most syscalls but blocks dangerous kernel interfaces. This is a
// pragmatic middle ground between Docker's default profile (which blocks
// syscalls bubblewrap needs like pivot_root, clone, unshare, mount) and
// seccomp=unconfined (which allows everything). The default action is ALLOW,
// with an explicit blocklist of the most dangerous syscalls that have no
// legitimate use inside the sandbox.
//
// This is the single source of truth for the profile: it is used both for
// bunker's native container creation (SecurityOpt below) and for the
// portable `.devcontainer/seccomp.json` emitted for OCI-feature-based setups.
func SeccompProfileJSON() string {
	profile := seccompProfileDef{
		DefaultAction: "SCMP_ACT_ALLOW",
		Syscalls: []seccompSyscall{
			{
				Names: []string{
					"kexec_load",
					"kexec_file_load",
					"perf_event_open",
					"bpf",
					"add_key",
					"keyctl",
					"request_key",
					"init_module",
					"finit_module",
					"delete_module",
					"reboot",
					"swapon",
					"swapoff",
					"nfsservctl",
					"personality",
					"acct",
					"lookup_dcookie",
					"kcmp",
					"open_by_handle_at",
					"ptrace",
					"process_vm_readv",
					"process_vm_writev",
					"userfaultfd",
				},
				Action: "SCMP_ACT_ERRNO",
			},
		},
	}

	data, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		panic("failed to marshal seccomp profile: " + err.Error())
	}
	return string(data)
}

// seccompProfile is the cached canonical profile JSON, computed once at
// package init. It is used by native container creation below.
var seccompProfile = SeccompProfileJSON()

// mandatoryEnvKeys lists environment variables that are always set by
// claude-bunker and must not be overridden by user-defined env vars.
var mandatoryEnvKeys = map[string]bool{
	"CLAUDE_CONFIG_DIR":                        true,
	"POWERLEVEL9K_DISABLE_GITSTATUS":           true,
	"DEVCONTAINER":                             true,
	"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": true,
	"DISABLE_INSTALLATION_CHECKS":              true,
}

// FindByLabel finds a container (running or stopped) with the claude-bunker label.
func FindByLabel(ctx context.Context, cli *client.Client, containerName string) (string, error) {
	f := filters.NewArgs()
	f.Add("label", LabelKey+"="+containerName)
	containers, err := cli.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: f,
	})
	if err != nil {
		return "", err
	}
	if len(containers) == 0 {
		return "", nil
	}
	return containers[0].ID, nil
}

// ContainerRunning checks if a container with the label is currently running.
func ContainerRunning(ctx context.Context, cli *client.Client, containerName string) (string, bool) {
	f := filters.NewArgs()
	f.Add("label", LabelKey+"="+containerName)
	containers, err := cli.ContainerList(ctx, container.ListOptions{
		All:     false, // only running
		Filters: f,
	})
	if err != nil || len(containers) == 0 {
		return "", false
	}
	return containers[0].ID, true
}

// CreateAndStartOpts contains all the options for creating and starting a container.
type CreateAndStartOpts struct {
	ContainerName string
	ImageTag      string
	Workspace     string
	ProjectCfg    config.ProjectConfig
	Auth          AuthTokens
	ExtraEnv      map[string]string // additional env vars to inject (proxy, plugin flags, etc.)
}

// containerDotfiles are dotfiles that Claude Code's bubblewrap sandbox creates
// in the working directory. We bind-mount /dev/null over each one so they
// don't leak to the host via the workspace bind mount.
var containerDotfiles = []string{
	".bash_profile",
	".bashrc",
	".gitconfig",
	".profile",
	".ripgreprc",
	".zprofile",
	".zshrc",
}

// containerDotDirs are directories that tools/IDEs may create in the workspace.
// Tmpfs mounts absorb these so they stay container-local.
var containerDotDirs = []string{
	".vscode",
	".idea",
}

// CreateAndStart creates and starts a new container with the correct mounts,
// caps, and environment.
func CreateAndStart(ctx context.Context, cli *client.Client, opts CreateAndStartOpts) (string, error) {
	workdir, err := config.EffectiveWorkdir(opts.ProjectCfg)
	if err != nil {
		return "", fmt.Errorf("invalid workspace config: %w", err)
	}
	bashVol := config.BashHistoryVolume(opts.ContainerName)
	claudeVol := config.ClaudeConfigVolume(opts.ContainerName)

	mounts := []mount.Mount{
		{
			Type:        mount.TypeBind,
			Source:      opts.Workspace,
			Target:      ContainerWorkspace,
			Consistency: mount.ConsistencyDelegated,
		},
		{
			Type:   mount.TypeVolume,
			Source: bashVol,
			Target: CommandHistoryDir,
		},
		{
			Type:   mount.TypeVolume,
			Source: claudeVol,
			Target: ContainerHome + "/.claude",
		},
		{
			Type:   mount.TypeTmpfs,
			Target: ContainerWorkspace + "/.claude",
			TmpfsOptions: &mount.TmpfsOptions{
				SizeBytes: 256 * 1024 * 1024, // 256MB limit
			},
		},
	}

	// Add tmpfs mount for secrets if any auth tokens or proxy certs are provided
	hasProxyCerts := opts.ExtraEnv["NODE_EXTRA_CA_CERTS"] != "" ||
		opts.ExtraEnv["CLAUDE_CODE_CLIENT_CERT"] != "" ||
		opts.ExtraEnv["CLAUDE_CODE_CLIENT_KEY"] != ""
	if opts.Auth.HasSecrets() || hasProxyCerts {
		mounts = append(mounts, mount.Mount{
			Type:   mount.TypeTmpfs,
			Target: SecretsDir,
			TmpfsOptions: &mount.TmpfsOptions{
				Mode: 0700,
			},
		})
	}

	// Absorb container-generated dotfiles so they don't leak to the host
	// via the workspace bind mount. Claude Code's bubblewrap sandbox creates
	// empty read-only dotfiles in the working directory for isolation; since
	// WORKDIR is /workspace (the host project), they would pollute the host.
	// Bind-mounting /dev/null over file paths and tmpfs over directories
	// ensures writes stay inside the container.
	for _, name := range containerDotfiles {
		mounts = append(mounts, mount.Mount{
			Type:     mount.TypeBind,
			Source:   "/dev/null",
			Target:   ContainerWorkspace + "/" + name,
			ReadOnly: true,
		})
	}
	for _, name := range containerDotDirs {
		mounts = append(mounts, mount.Mount{
			Type:   mount.TypeTmpfs,
			Target: ContainerWorkspace + "/" + name,
		})
	}

	// Add tmpfs mounts for excluded paths
	for _, p := range opts.ProjectCfg.Exclude {
		// filepath.Clean normalizes leading ./, trailing /, and .. components.
		// Then verify the result stays under the workspace to prevent traversal.
		target := filepath.Clean(ContainerWorkspace + "/" + p)
		if !strings.HasPrefix(target, ContainerWorkspace+"/") || target == ContainerWorkspace {
			continue // skip paths that escape /workspace
		}
		mounts = append(mounts, mount.Mount{
			Type:   mount.TypeTmpfs,
			Target: target,
		})
	}

	env := []string{
		"CLAUDE_CONFIG_DIR=" + ContainerHome + "/.claude",
		"POWERLEVEL9K_DISABLE_GITSTATUS=true",
		"DEVCONTAINER=true",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
		"DISABLE_INSTALLATION_CHECKS=1",
	}

	// Merge user-defined env vars (mandatory vars above always win)
	for k, v := range opts.ProjectCfg.Env {
		if !mandatoryEnvKeys[k] {
			env = append(env, k+"="+v)
		}
	}

	// Merge extra env vars (mandatory vars always win)
	for k, v := range opts.ExtraEnv {
		if !mandatoryEnvKeys[k] {
			env = append(env, k+"="+v)
		}
	}
	containerCfg := &container.Config{
		Image:      opts.ImageTag,
		User:       ContainerUser,
		WorkingDir: workdir,
		Env:        env,
		Labels: map[string]string{
			LabelKey: opts.ContainerName,
		},
		Cmd:       []string{"sleep", "infinity"},
		Tty:       false,
		OpenStdin: false,
	}

	// Security tradeoff: apparmor=unconfined is required because bubblewrap
	// (bwrap) needs to create user namespaces and call pivot_root, which
	// Docker's default AppArmor profile blocks. A custom seccomp profile
	// replaces the previous seccomp=unconfined — it defaults to ALLOW but
	// blocks the most dangerous kernel interfaces (module loading, kexec,
	// BPF, reboot, etc.). bwrap is Claude Code's inner sandbox — it provides
	// filesystem write restrictions that are the primary defense layer
	// inside the container. The iptables firewall and managed-settings.json
	// provide the outer defense layers.
	//
	// NET_ADMIN and NET_RAW capabilities are required for iptables firewall
	// setup, which runs as root via ExecNonInteractive. These capabilities
	// are NOT effective for the unprivileged claude-bunker user — Docker only
	// grants effective capabilities to UID 0 processes (ambient capabilities
	// are not set). A prompt injection that runs code as claude-bunker cannot
	// modify iptables rules. If the bubblewrap sandbox is bypassed, the
	// attacker would still need to escalate to root to exercise NET_ADMIN.
	hostCfg := &container.HostConfig{
		Mounts:      mounts,
		CapAdd:      []string{"NET_ADMIN", "NET_RAW"},
		SecurityOpt: []string{"apparmor=unconfined", "seccomp=" + seccompProfile},
	}

	networkCfg := &network.NetworkingConfig{}

	resp, err := cli.ContainerCreate(ctx, containerCfg, hostCfg, networkCfg, nil, opts.ContainerName)
	if err != nil {
		return "", fmt.Errorf("creating container: %w", err)
	}

	if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		// Clean up the created container on start failure
		cli.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		return "", fmt.Errorf("starting container: %w", err)
	}

	return resp.ID, nil
}

// RunPostStartOpts contains options for post-start container setup.
type RunPostStartOpts struct {
	ExtraDomains     []string
	PostStartCommand string
	Auth             AuthTokens
}

// RunPostStart runs the post-start commands inside the container:
// 1. Write domains file + batch pre-firewall setup (git config, domain lockdown)
// 1c. Prepare credential masking (real secrets → bunker-proxy-owned config)
// 2. Run init-firewall.sh (standalone — must run after domain file exists;
//
//	the firewall also starts the egress proxy, which reads the masking
//	config prepared in step 1c, so 1c MUST run before this step)
//
// 3. Batch post-firewall setup (refresh daemon + git identity)
// 4. Inject auth secrets for the agent — sentinels when masking is active,
//
//	so the agent never sees the real credentials (if provided)
//
// 5. Run postStartCommand (if configured)
//
// Steps are batched into as few Docker exec calls as possible to reduce
// API round-trip overhead (~200ms per exec create+attach+inspect).
func RunPostStart(ctx context.Context, cli *client.Client, containerID string, opts RunPostStartOpts) error {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	// 1a. Write all firewall domains (builtin + user extras) to temp file.
	// The firewall script reads this file instead of maintaining its own list,
	// keeping the Go code as the single source of truth for allowed domains.
	allDomains := BuiltinDomains()
	allDomains = append(allDomains, opts.ExtraDomains...)
	if err := CopyContentToContainer(ctx, cli, containerID,
		[]byte(strings.Join(allDomains, "\n")), DomainsFilePath); err != nil {
		return fmt.Errorf("writing firewall domains: %w", err)
	}

	// 1b. Pre-firewall batch (root): lock down domains file + git config.
	// Combines domain lockdown, safe.directory, and HTTPS rewrite into a
	// single exec to save ~400ms of Docker API overhead.
	{
		var script strings.Builder
		script.WriteString("set -e\n")
		// Lock down domains file so the container user cannot modify it.
		// Without this, a prompt injection could append domains to bypass the
		// firewall when the refresh daemon re-reads the file.
		fmt.Fprintf(&script, "chown root:root %s && chmod 444 %s\n", DomainsFilePath, DomainsFilePath)
		// Git safe directory (required for sandbox workspace access).
		fmt.Fprintf(&script, "su -s /bin/sh %s -c 'git config --global --add safe.directory %s'\n",
			ContainerUser, ContainerWorkspace)
		// HTTPS rewrite for GitHub — the container has no SSH keys, so rewrite
		// git@github.com: URLs to HTTPS for Claude Code's plugin marketplace.
		fmt.Fprintf(&script, "su -s /bin/sh %s -c 'git config --global url.https://github.com/.insteadOf git@github.com:' || true\n",
			ContainerUser)

		if _, err := ExecNonInteractive(ctx, cli, containerID, RootUser,
			[]string{"sh", "-c", script.String()}); err != nil {
			return fmt.Errorf("pre-firewall setup: %w", err)
		}
	}

	// 1c. Prepare credential masking (real secrets → proxy-owned config) BEFORE
	// the firewall starts the proxy. Returns the sentinel tokens for the agent.
	sentinelAuth, err := PrepareMasking(ctx, cli, containerID, opts.Auth)
	if err != nil {
		return fmt.Errorf("prepare masking: %w", err)
	}

	// 2. Run firewall init (exec runs as root, no sudo needed).
	// Pass the domains file path as an argument so Go is the single source of truth.
	_, err = ExecNonInteractive(ctx, cli, containerID, RootUser,
		[]string{FirewallScriptPath, DomainsFilePath})
	if err != nil {
		return fmt.Errorf("init-firewall.sh: %w", err)
	}

	// 3. Post-firewall batch (root): start refresh daemon + copy git identity.
	// Combines the firewall refresh daemon launch and host git identity into a
	// single exec to save ~200-400ms of Docker API overhead.
	{
		var script strings.Builder
		script.WriteString("set -e\n")
		// Start the background firewall refresh daemon. It re-resolves all
		// domains every 5 minutes and atomically swaps the ipset, so CDN/cloud
		// IP rotations (e.g. Google's proxy.golang.org) don't break connections
		// after the initial startup resolution. Non-fatal: the firewall is already
		// up; refresh is a best-effort improvement over the one-shot approach.
		//
		// Launched via nohup & inside a shell so the exec session finishes
		// immediately — a detached exec would stay "Running" forever and fool
		// HasOtherActiveSessions into thinking the container is in use.
		fmt.Fprintf(&script, "nohup '%s' '%s' >/dev/null 2>&1 &\n", RefreshFirewallScriptPath, DomainsFilePath)

		// Copy host git identity (name/email only, not credential helpers).
		// Non-fatal: git identity is nice-to-have, so errors don't fail the batch.
		name, email := hostGitIdentity()
		if name != "" {
			fmt.Fprintf(&script, "su -s /bin/sh %s -c 'git config --global user.name %q' || true\n", ContainerUser, name)
		}
		if email != "" {
			fmt.Fprintf(&script, "su -s /bin/sh %s -c 'git config --global user.email %q' || true\n", ContainerUser, email)
		}

		if _, err := ExecNonInteractive(ctx, cli, containerID, RootUser,
			[]string{"sh", "-c", script.String()}); err != nil {
			// Non-fatal: firewall is already up and git identity is nice-to-have
			log.Warnf("post-firewall setup: %v", err)
		}
	}

	// 4. Inject auth secrets (sentinels when masking is active) for the agent.
	if err := InjectAuthSecrets(ctx, cli, containerID, sentinelAuth); err != nil {
		return fmt.Errorf("auth injection: %w", err)
	}

	// 5. Run postStartCommand if configured.
	//
	// TRUST BOUNDARY: postStartCommand comes from the project's
	// .devcontainer/devcontainer.json, which lives inside the cloned repository.
	// A malicious .devcontainer/devcontainer.json can execute arbitrary shell
	// commands here — this is inherent to the devcontainer model (VS Code has
	// the same issue with its workspace trust model). Users should review
	// .devcontainer/devcontainer.json before running claude-bunker on untrusted
	// repos. The firewall limits blast radius by restricting network access,
	// but local filesystem access within /workspace is unrestricted.
	if opts.PostStartCommand != "" {
		_, err = ExecNonInteractive(ctx, cli, containerID, ContainerUser,
			[]string{"sh", "-c", opts.PostStartCommand})
		if err != nil {
			return fmt.Errorf("postStartCommand: %w", err)
		}
	}

	return nil
}

// InjectAuthSecrets injects authentication tokens into the container via tmpfs.
// Tokens are stored as files in /run/secrets/ (never as environment variables)
// so they don't appear in docker inspect or /proc/*/environ.
//
// Batched: all file writes + permission changes happen in a single root exec
// call, followed by a single user exec for git config. This reduces Docker API
// round-trips from ~10 to 2 (saving ~1-2s of exec overhead).
func InjectAuthSecrets(ctx context.Context, cli *client.Client, containerID string, auth AuthTokens) error {
	if !auth.HasSecrets() {
		return nil
	}

	// Build a single root script that writes all secrets + wrapper + sets permissions.
	var script strings.Builder
	script.WriteString("set -e\n")
	fmt.Fprintf(&script, "chmod 711 %s\n", SecretsDir)

	ug := ContainerUserGroup

	if err := writeSecretFiles(ctx, cli, containerID, &script, ug, auth); err != nil {
		return err
	}
	createAuthWrapper(&script, ug, auth)

	// Single root exec: write all secrets + wrapper + set all permissions
	if _, err := ExecNonInteractive(ctx, cli, containerID, RootUser,
		[]string{"sh", "-c", script.String()}); err != nil {
		return fmt.Errorf("writing auth secrets: %w", err)
	}

	// Single user exec: git credential helper (if gh token provided)
	if auth.GhToken != "" {
		credentialHelper := fmt.Sprintf(`!f() { echo "protocol=https"; echo "host=github.com"; echo "username=x-access-token"; echo "password=$(cat %s/gh_token)"; }; f`, SecretsDir)
		if _, err := ExecNonInteractive(ctx, cli, containerID, ContainerUser,
			[]string{"git", "config", "--global", "credential.https://github.com.helper", credentialHelper}); err != nil {
			return fmt.Errorf("git credential helper: %w", err)
		}
	}

	return nil
}

// writeSecretFiles writes all provided tokens to the secrets tmpfs in a single
// batched operation using CopyMultipleToContainer, avoiding shell commands that
// would expose tokens in /proc/*/cmdline. Permissions are set via the shell
// script (which only runs chmod/chown, not the secret data).
func writeSecretFiles(ctx context.Context, cli *client.Client, containerID string, script *strings.Builder, ug string, auth AuthTokens) error {
	type secret struct {
		token string
		file  string
	}
	secrets := []secret{
		{auth.GhToken, "gh_token"},
		{auth.ApiKey, "api_key"},
		{auth.OAuthToken, "oauth_token"},
	}

	var files []FileEntry
	for _, s := range secrets {
		if s.token == "" {
			continue
		}
		containerPath := SecretsDir + "/" + s.file
		files = append(files, FileEntry{
			Content: []byte(s.token),
			Path:    containerPath,
			Mode:    0400,
		})
		fmt.Fprintf(script, "chmod 400 %s && chown %s %s\n", containerPath, ug, containerPath)
	}

	if len(files) == 0 {
		return nil
	}

	if err := CopyMultipleToContainer(ctx, cli, containerID, files); err != nil {
		return fmt.Errorf("writing auth secrets: %w", err)
	}
	return nil
}

// createAuthWrapper appends shell commands to script that create a wrapper
// script at ~/.claude-auth-wrapper.sh. The wrapper reads secrets from tmpfs
// files and exports them as env vars before exec-ing the wrapped command,
// ensuring tokens never appear in docker inspect or container env.
func createAuthWrapper(script *strings.Builder, ug string, auth AuthTokens) {
	if !auth.HasSecrets() {
		return
	}

	fmt.Fprintf(script, "cat > %s << 'WRAPPER_EOF'\n", AuthWrapperPath)
	script.WriteString("#!/bin/sh\n")
	if auth.ApiKey != "" {
		fmt.Fprintf(script, "export ANTHROPIC_API_KEY=\"$(cat %s/api_key)\"\n", SecretsDir)
	}
	if auth.OAuthToken != "" {
		fmt.Fprintf(script, "export CLAUDE_CODE_OAUTH_TOKEN=\"$(cat %s/oauth_token)\"\n", SecretsDir)
	}
	if auth.GhToken != "" {
		// Export for the GitHub MCP plugin which expects this env var
		fmt.Fprintf(script, "export GITHUB_PERSONAL_ACCESS_TOKEN=\"$(cat %s/gh_token)\"\n", SecretsDir)
	}
	// When credential masking is active the proxy terminates the auth hosts;
	// Node/Claude Code must trust the per-container CA.
	fmt.Fprintf(script, "export NODE_EXTRA_CA_CERTS=%q\n", ProxyCACertPath)
	script.WriteString("exec \"$@\"\n")
	script.WriteString("WRAPPER_EOF\n")
	fmt.Fprintf(script, "chmod 755 %s && chown %s %s\n", AuthWrapperPath, ug, AuthWrapperPath)
}

// hostGitIdentity reads user.name and user.email from the host's git config
// in a single subprocess call (instead of two separate `git config --get` calls).
func hostGitIdentity() (name, email string) {
	cmd := exec.Command("git", "config", "--global", "--list")
	out, err := cmd.Output()
	if err != nil {
		return "", ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			switch strings.TrimSpace(k) {
			case "user.name":
				name = strings.TrimSpace(v)
			case "user.email":
				email = strings.TrimSpace(v)
			}
		}
		if name != "" && email != "" {
			break
		}
	}
	return name, email
}

// Stop stops a container with a timeout.
func Stop(ctx context.Context, cli *client.Client, containerID string) error {
	timeout := 10
	return cli.ContainerStop(ctx, containerID, container.StopOptions{Timeout: &timeout})
}

// Remove removes a container.
func Remove(ctx context.Context, cli *client.Client, containerID string) error {
	return cli.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true})
}

// execInspector is the minimal Docker surface HasOtherActiveSessions needs.
// *client.Client satisfies it; tests use a fake.
type execInspector interface {
	ContainerInspect(ctx context.Context, containerID string) (container.InspectResponse, error)
	ContainerExecInspect(ctx context.Context, execID string) (container.ExecInspect, error)
}

// HasOtherActiveSessions reports whether the container has a running exec
// session other than myExecID. It returns an error if the daemon can't be
// queried — callers must treat that as "cannot determine" and fail closed
// (do not tear the container down), because a false "no sessions" would let one
// exiting session SIGKILL a container hosting other live sessions.
func HasOtherActiveSessions(ctx context.Context, cli execInspector, containerID, myExecID string) (bool, error) {
	inspect, err := cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return false, fmt.Errorf("inspecting container: %w", err)
	}
	for _, eid := range inspect.ExecIDs {
		if eid == myExecID {
			continue
		}
		execInfo, err := cli.ContainerExecInspect(ctx, eid)
		if err != nil {
			return false, fmt.Errorf("inspecting exec %s: %w", eid, err)
		}
		if execInfo.Running {
			return true, nil
		}
	}
	return false, nil
}

// HasAnyActiveSessions reports whether the container has any running exec session.
func HasAnyActiveSessions(ctx context.Context, cli execInspector, containerID string) (bool, error) {
	return HasOtherActiveSessions(ctx, cli, containerID, "")
}
