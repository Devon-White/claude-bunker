package container

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
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
)


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
	GhToken       string // GitHub fine-grained PAT for git auth (injected via tmpfs)
	ApiKey        string // Anthropic API key for Claude auth (injected via tmpfs)
	OAuthToken    string // Claude OAuth token for Claude auth (injected via tmpfs)
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
		},
	}

	// Add tmpfs mount for secrets if any auth tokens are provided
	if opts.GhToken != "" || opts.ApiKey != "" || opts.OAuthToken != "" {
		mounts = append(mounts, mount.Mount{
			Type:   mount.TypeTmpfs,
			Target: SecretsDir,
			TmpfsOptions: &mount.TmpfsOptions{
				Mode: 0700,
			},
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
	}

	// Merge user-defined env vars (mandatory vars above always win)
	mandatoryKeys := map[string]bool{
		"CLAUDE_CONFIG_DIR":            true,
		"POWERLEVEL9K_DISABLE_GITSTATUS": true,
		"DEVCONTAINER":                  true,
	}
	for k, v := range opts.ProjectCfg.Env {
		if !mandatoryKeys[k] {
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

	// Security tradeoff: apparmor=unconfined and seccomp=unconfined are
	// required because bubblewrap (bwrap) needs to create user namespaces
	// and call pivot_root, which Docker's default AppArmor and seccomp
	// profiles block. bwrap is Claude Code's inner sandbox — it provides
	// filesystem write restrictions that are the primary defense layer
	// inside the container. The iptables firewall and managed-settings.json
	// provide the outer defense layers.
	hostCfg := &container.HostConfig{
		Mounts:  mounts,
		CapAdd:  []string{"NET_ADMIN", "NET_RAW"},
		SecurityOpt: []string{"apparmor=unconfined", "seccomp=unconfined"},
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
	GhToken          string
	ApiKey           string
	OAuthToken       string
}

// RunPostStart runs the post-start commands inside the container:
// 1. git config safe.directory
// 2. Write extra domains to temp file
// 3. Run init-firewall.sh
// 4. Copy host git identity
// 5. Inject auth secrets (if provided)
// 6. Run postStartCommand (if configured)
func RunPostStart(ctx context.Context, cli *client.Client, containerID string, opts RunPostStartOpts) error {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	// 1. Git safe directory
	_, err := ExecNonInteractive(ctx, cli, containerID, ContainerUser,
		[]string{"git", "config", "--global", "--add", "safe.directory", ContainerWorkspace})
	if err != nil {
		return fmt.Errorf("git config: %w", err)
	}

	// 2. Write all firewall domains (builtin + user extras) to temp file.
	// The firewall script reads this file instead of maintaining its own list,
	// keeping the Go code as the single source of truth for allowed domains.
	allDomains := BuiltinDomains()
	allDomains = append(allDomains, opts.ExtraDomains...)
	if err := CopyContentToContainer(ctx, cli, containerID,
		[]byte(strings.Join(allDomains, "\n")), DomainsFilePath); err != nil {
		return fmt.Errorf("writing firewall domains: %w", err)
	}

	// 3. Run firewall init (exec runs as root, no sudo needed).
	// Pass the domains file path as an argument so Go is the single source of truth.
	_, err = ExecNonInteractive(ctx, cli, containerID, "root",
		[]string{FirewallScriptPath, DomainsFilePath})
	if err != nil {
		return fmt.Errorf("init-firewall.sh: %w", err)
	}

	// 4. Copy host git identity (name/email only, not credential helpers)
	if err := copyHostGitIdentity(ctx, cli, containerID); err != nil {
		// Non-fatal: git identity is nice-to-have
		fmt.Fprintf(os.Stderr, "[claude-bunker] WARNING: git identity: %v\n", err)
	}

	// 5. Inject auth secrets (if provided)
	if err := injectAuthSecrets(ctx, cli, containerID, opts); err != nil {
		return fmt.Errorf("auth injection: %w", err)
	}

	// 6. Run postStartCommand if configured.
	//
	// TRUST BOUNDARY: postStartCommand comes from the project's config.json,
	// which lives inside the cloned repository. A malicious config.json can
	// execute arbitrary shell commands here — this is inherent to the
	// devcontainer model (VS Code has the same issue with its workspace trust
	// model). Users should review config.json before running claude-bunker on
	// untrusted repos. The firewall limits blast radius by restricting network
	// access, but local filesystem access within /workspace is unrestricted.
	if opts.PostStartCommand != "" {
		_, err = ExecNonInteractive(ctx, cli, containerID, ContainerUser,
			[]string{"sh", "-c", opts.PostStartCommand})
		if err != nil {
			return fmt.Errorf("postStartCommand: %w", err)
		}
	}

	return nil
}

// injectAuthSecrets injects authentication tokens into the container via tmpfs.
// Tokens are stored as files in /run/secrets/ (never as environment variables)
// so they don't appear in docker inspect or /proc/*/environ.
//
// Batched: all file writes + permission changes happen in a single root exec
// call, followed by a single user exec for git config. This reduces Docker API
// round-trips from ~10 to 2 (saving ~1-2s of exec overhead).
func injectAuthSecrets(ctx context.Context, cli *client.Client, containerID string, opts RunPostStartOpts) error {
	hasSecrets := opts.GhToken != "" || opts.ApiKey != "" || opts.OAuthToken != ""
	if !hasSecrets {
		return nil
	}

	// Build a single root script that writes all secrets + wrapper + sets permissions.
	var script strings.Builder
	script.WriteString("set -e\n")
	fmt.Fprintf(&script, "chmod 711 %s\n", SecretsDir)

	ug := ContainerUserGroup

	writeSecretFiles(&script, ug, opts)
	createAuthWrapper(&script, ug, opts)

	// Single root exec: write all secrets + wrapper + set all permissions
	if _, err := ExecNonInteractive(ctx, cli, containerID, "root",
		[]string{"sh", "-c", script.String()}); err != nil {
		return fmt.Errorf("writing auth secrets: %w", err)
	}

	// Single user exec: git credential helper (if gh token provided)
	if opts.GhToken != "" {
		credentialHelper := fmt.Sprintf(`!f() { echo "protocol=https"; echo "host=github.com"; echo "username=x-access-token"; echo "password=$(cat %s/gh_token)"; }; f`, SecretsDir)
		if _, err := ExecNonInteractive(ctx, cli, containerID, ContainerUser,
			[]string{"git", "config", "--global", "credential.https://github.com.helper", credentialHelper}); err != nil {
			return fmt.Errorf("git credential helper: %w", err)
		}
	}

	return nil
}

// writeSecretFiles appends shell commands to script that write each provided
// token to the secrets tmpfs as a base64-decoded file with locked-down permissions.
func writeSecretFiles(script *strings.Builder, ug string, opts RunPostStartOpts) {
	type secret struct {
		token string
		file  string
	}
	secrets := []secret{
		{opts.GhToken, "gh_token"},
		{opts.ApiKey, "api_key"},
		{opts.OAuthToken, "oauth_token"},
	}
	for _, s := range secrets {
		if s.token == "" {
			continue
		}
		encoded := base64.StdEncoding.EncodeToString([]byte(s.token))
		fmt.Fprintf(script, "echo '%s' | base64 -d > %s/%s\n", encoded, SecretsDir, s.file)
		fmt.Fprintf(script, "chmod 400 %s/%s && chown %s %s/%s\n", SecretsDir, s.file, ug, SecretsDir, s.file)
	}
}

// createAuthWrapper appends shell commands to script that create a wrapper
// script at ~/.claude-auth-wrapper.sh. The wrapper reads secrets from tmpfs
// files and exports them as env vars before exec-ing the wrapped command,
// ensuring tokens never appear in docker inspect or container env.
func createAuthWrapper(script *strings.Builder, ug string, opts RunPostStartOpts) {
	if opts.ApiKey == "" && opts.OAuthToken == "" {
		return
	}

	wrapperPath := ContainerHome + "/.claude-auth-wrapper.sh"
	fmt.Fprintf(script, "cat > %s << 'WRAPPER_EOF'\n", wrapperPath)
	script.WriteString("#!/bin/sh\n")
	if opts.ApiKey != "" {
		fmt.Fprintf(script, "export ANTHROPIC_API_KEY=\"$(cat %s/api_key)\"\n", SecretsDir)
	}
	if opts.OAuthToken != "" {
		fmt.Fprintf(script, "export CLAUDE_CODE_OAUTH_TOKEN=\"$(cat %s/oauth_token)\"\n", SecretsDir)
	}
	script.WriteString("exec \"$@\"\n")
	script.WriteString("WRAPPER_EOF\n")
	fmt.Fprintf(script, "chmod 755 %s && chown %s %s\n", wrapperPath, ug, wrapperPath)
}

// copyHostGitIdentity extracts user.name and user.email from the host's
// git config and sets them in the container. Only identity fields are copied;
// credential helpers and other sensitive config are deliberately excluded.
//
// Batched: both git config calls combined into a single exec (saves ~100-200ms).
func copyHostGitIdentity(ctx context.Context, cli *client.Client, containerID string) error {
	name, nameErr := execGitConfig("user.name")
	email, emailErr := execGitConfig("user.email")

	if nameErr != nil && emailErr != nil {
		return nil // no git identity configured on host
	}

	var cmds []string
	if nameErr == nil && name != "" {
		cmds = append(cmds, fmt.Sprintf("git config --global user.name '%s'", strings.ReplaceAll(name, "'", "'\\''")))
	}
	if emailErr == nil && email != "" {
		cmds = append(cmds, fmt.Sprintf("git config --global user.email '%s'", strings.ReplaceAll(email, "'", "'\\''")))
	}

	if len(cmds) > 0 {
		if _, err := ExecNonInteractive(ctx, cli, containerID, ContainerUser,
			[]string{"sh", "-c", strings.Join(cmds, " && ")}); err != nil {
			return fmt.Errorf("setting git config in container: %w", err)
		}
	}

	return nil
}

// execGitConfig runs `git config --global --get <key>` on the host and returns the value.
func execGitConfig(key string) (string, error) {
	cmd := exec.Command("git", "config", "--global", "--get", key)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
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

// StopAndRemove stops and removes a container by its label name.
func StopAndRemove(ctx context.Context, cli *client.Client, containerName string) error {
	id, err := FindByLabel(ctx, cli, containerName)
	if err != nil {
		return err
	}
	if id == "" {
		return nil
	}

	_ = Stop(ctx, cli, id)
	return Remove(ctx, cli, id)
}

// HasOtherActiveSessions checks whether the container has any running exec
// sessions other than myExecID. It uses Docker's ContainerInspect to enumerate
// exec IDs and ContainerExecInspect to check each one's Running status.
func HasOtherActiveSessions(ctx context.Context, cli *client.Client, containerID, myExecID string) bool {
	inspect, err := cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return false
	}

	for _, eid := range inspect.ExecIDs {
		if eid == myExecID {
			continue
		}
		execInfo, err := cli.ContainerExecInspect(ctx, eid)
		if err != nil {
			continue
		}
		if execInfo.Running {
			return true
		}
	}
	return false
}

// HasAnyActiveSessions checks whether the container has any running exec sessions.
// Used to prevent stopping a container with active sessions during fingerprint-based rebuilds.
func HasAnyActiveSessions(ctx context.Context, cli *client.Client, containerID string) bool {
	return HasOtherActiveSessions(ctx, cli, containerID, "")
}
