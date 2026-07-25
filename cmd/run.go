package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	dockerclient "github.com/docker/docker/client"

	"github.com/Devon-White/claude-bunker/internal/config"
	"github.com/Devon-White/claude-bunker/internal/container"
	"github.com/Devon-White/claude-bunker/internal/devcontainer"
	bunkerlog "github.com/Devon-White/claude-bunker/internal/log"
	"github.com/Devon-White/claude-bunker/internal/platform"
	"github.com/Devon-White/claude-bunker/internal/sandbox"
)

// verbosity controls output level: -1 = quiet, 0 = normal, 1 = verbose.
var verbosity int

// envQuiet is the environment variable that suppresses informational output.
const envQuiet = "CLAUDE_BUNKER_QUIET"

// activeRunner is set during runInSandbox so signal handlers and die() can
// access the runner for cleanup.
var activeRunner *runner

// runner holds all state for a single sandbox session.
type runner struct {
	mu        sync.Mutex
	cleanedUp bool
	teardown  bool

	ctx    context.Context
	cancel context.CancelFunc
	cli    *dockerclient.Client

	workspace     string
	projectCfg    config.ProjectConfig
	containerName string
	containerID   string
	imageTag      string
	extraDomains  []string
	auth          container.AuthTokens

	execID    string              // Docker exec ID from ExecInteractive, used for cleanup session detection
	reused    bool                // true when attaching to an already-running container with matching fingerprints
	noCache   bool                // true when --rebuild is used; passed to Docker build as NoCache
	force     bool                // --force: override fail-closed guards
	noSandbox bool                // --no-sandbox: launch even if sandbox settings can't be seeded
	proxyCfg  sandbox.ProxyConfig // proxy config detected from host env

	// Computed during resolveContainer, reused in buildAndCreate for fingerprint saving.
	buildInput config.BuildInput
	fpResult   config.FingerprintResult

	// Cached build artifacts from resolveContainer, passed to BuildImage to avoid recomputation.
	cachedDockerfile string
	cachedScripts    []container.BuildContextFile
}

// cleanup stops and removes the container. Safe to call multiple times
// and from any goroutine (signal handlers, normal exit, die()).
func (r *runner) cleanup() {
	platform.RestoreSaved()

	r.mu.Lock()
	if r.cleanedUp || !r.teardown || r.containerID == "" {
		r.mu.Unlock()
		return
	}
	r.cleanedUp = true
	cID := r.containerID
	execID := r.execID
	r.mu.Unlock()

	// Don't tear down the container if other sessions are still attached.
	// Use a fresh context — r.ctx may already be cancelled by signal handlers.
	checkCtx, checkCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer checkCancel()
	active, err := container.HasOtherActiveSessions(checkCtx, r.cli, cID, execID)
	if err != nil {
		// Fail closed: if we can't tell, assume other sessions may be active and
		// leave the container running — unless the user forced teardown.
		if !r.force {
			verbose("Could not determine active sessions; leaving container running: " + err.Error())
			return
		}
		verbose("Could not determine active sessions; forcing teardown (--force): " + err.Error())
	} else if active {
		verbose("Other sessions still active — leaving container running")
		return
	}

	// Stop and remove the container in the background so the user returns
	// to their terminal immediately. docker rm -f sends SIGKILL and removes
	// in one operation; the Docker daemon handles cleanup async.
	info("Stopping sandbox...")
	_ = exec.Command("docker", "rm", "-f", cID).Start()
}

// resolveWorkspace determines the workspace directory from env or cwd.
// Used by runInSandbox, status, and logs commands.
func resolveWorkspace() string {
	if ws := os.Getenv("CLAUDE_BUNKER_WS"); ws != "" {
		return ws
	}
	ws, err := os.Getwd()
	if err != nil {
		die("cannot determine workspace: " + err.Error())
	}
	return ws
}

// bunkerFlags holds claude-bunker-specific flags extracted from the args
// before the remaining args are passed through to claude/bash.
type bunkerFlags struct {
	auth      container.AuthTokens
	quiet     bool
	verbose   bool
	keep      bool
	rebuild   bool
	force     bool
	noSandbox bool
	noColor   bool
	remaining []string
	err       error
}

// extractBunkerFlags pulls claude-bunker-specific flags from the arg list.
// Value flags: --gh-token, --api-key, --oauth-token (each takes a non-empty
// value; a missing or empty value sets f.err).
// Boolean flags: --verbose, --quiet, --keep, --rebuild, --force, --no-sandbox.
// Returns the extracted values and the remaining args to pass through.
func extractBunkerFlags(args []string) bunkerFlags {
	var f bunkerFlags
	flagMap := map[string]*string{
		"--gh-token":    &f.auth.GhToken,
		"--api-key":     &f.auth.ApiKey,
		"--oauth-token": &f.auth.OAuthToken,
	}
	boolFlags := map[string]*bool{
		"--verbose":    &f.verbose,
		"--quiet":      &f.quiet,
		"--keep":       &f.keep,
		"--rebuild":    &f.rebuild,
		"--force":      &f.force,
		"--no-sandbox": &f.noSandbox,
		"--no-color":   &f.noColor,
	}

	i := 0
	for i < len(args) {
		arg := args[i]

		// Check boolean flags
		if dest, ok := boolFlags[arg]; ok {
			*dest = true
			i++
			continue
		}

		// Check for --flag value and --flag=value forms
		handled := false
		for flag, dest := range flagMap {
			if arg == flag {
				if i+1 < len(args) && args[i+1] != "" {
					*dest = args[i+1]
					i += 2
				} else {
					f.err = fmt.Errorf("flag %s needs a non-empty value", flag)
					i++
				}
				handled = true
				break
			}
			if strings.HasPrefix(arg, flag+"=") {
				val := arg[len(flag)+1:]
				if val == "" {
					f.err = fmt.Errorf("flag %s needs a non-empty value", flag)
				} else {
					*dest = val
				}
				i++
				handled = true
				break
			}
		}
		if !handled {
			f.remaining = append(f.remaining, arg)
			i++
		}
	}
	return f
}

// runDefault is the main orchestration flow for launching claude in the sandbox.
func runDefault(cmd *cobra.Command, args []string) error {
	return runInSandbox(args, "claude")
}

// runInSandbox contains the shared orchestration logic for both
// `claude-bunker` and `claude-bunker shell`.
func runInSandbox(passedArgs []string, execCmd string) error {
	flags := extractBunkerFlags(passedArgs)
	if flags.err != nil {
		die(flags.err.Error())
	}

	// Set verbosity: --quiet or CLAUDE_BUNKER_QUIET=1 suppresses info output,
	// --verbose enables detailed output.
	if flags.quiet || os.Getenv(envQuiet) == "1" {
		verbosity = -1
	} else if flags.verbose {
		verbosity = 1
	}

	// Wire internal log package to use styled output with verbosity control
	bunkerlog.WarnFunc = warn
	bunkerlog.InfoFunc = info

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cli, err := dockerClient()
	if err != nil {
		dieCode(ExitDockerUnavailable, err.Error())
	}

	r := &runner{
		ctx:       ctx,
		cancel:    cancel,
		cli:       cli,
		workspace: resolveWorkspace(),
		force:     flags.force,
		noSandbox: flags.noSandbox,
	}
	activeRunner = r

	r.loadConfig(flags)
	r.resolveNaming()

	// Handle --rebuild: force a clean slate
	if flags.rebuild {
		r.noCache = true
		info("Rebuild requested — clearing cache and removing existing image...")
		_ = config.ClearFingerprint(r.containerName)
		_ = container.RemoveImageByTag(r.ctx, r.cli, r.imageTag)
		if id, err := container.FindByLabel(r.ctx, r.cli, r.containerName); err == nil && id != "" {
			_ = container.Stop(r.ctx, r.cli, id)
			_ = container.Remove(r.ctx, r.cli, id)
		}
	}

	r.resolveContainer()

	if r.containerID == "" {
		r.buildAndCreate()
	}

	r.registerCleanup(!flags.keep)
	if flags.keep {
		verbose("Container will be kept running after exit (--keep)")
	}

	// Only seed settings on fresh containers. When reusing a running container
	// with matching fingerprints, settings are already correct from first start.
	if !r.reused {
		r.seedSettings()
	}

	// Re-inject auth secrets on every start (including reuse). Tokens may have
	// changed since the container was first created, and the tmpfs secrets are
	// lost if the container was stopped and restarted.
	if r.reused {
		r.reinjectAuthSecrets()
	}
	r.setupSignals()

	exitCode := r.exec(execCmd, flags.remaining)
	r.cleanup()
	cli.Close()
	os.Exit(exitCode)
	return nil // unreachable, but required by RunE signature
}

// failClosed returns a fatal error when err is non-nil and not overridden by a
// flag. When overridden, it returns nil and the caller should warn and continue.
// This centralizes the security-critical "refuse to run without the protection
// unless the user explicitly opted out" decision.
func failClosed(err error, overridden bool, remediation string) error {
	if err == nil || overridden {
		return nil
	}
	return fmt.Errorf("%w\n%s", err, remediation)
}

// loadConfig reads project config and resolves auth token precedence.
func (r *runner) loadConfig(flags bunkerFlags) {
	cfg, _, err := devcontainer.LoadProjectConfig(r.workspace)
	if fatal := failClosed(err, flags.force, "Fix .devcontainer/devcontainer.json, or re-run with --force to ignore it."); fatal != nil {
		die("Failed to parse devcontainer.json: " + fatal.Error())
	}
	if err != nil {
		warn("Continuing despite devcontainer.json error (--force): " + err.Error())
	}
	// Expand ${VAR} references for runtime use (auth token, firewall domains, env).
	config.ExpandProjectConfig(&cfg)

	// Fail-closed: reject invalid allowDomains patterns (shell metacharacters,
	// over-broad wildcards, empty segments) before they reach the sandbox
	// allowlist. Runs after expansion so raw ${VAR} refs are already resolved.
	domErr := config.NormalizeDomains(&cfg)
	if fatal := failClosed(domErr, flags.force, "Fix allowDomains in .devcontainer/devcontainer.json, or re-run with --force."); fatal != nil {
		die("Invalid allowDomains: " + fatal.Error())
	}
	if domErr != nil {
		warn("Continuing despite invalid allowDomains (--force): " + domErr.Error())
	}

	r.projectCfg = cfg

	// Resolve auth tokens: CLI flags > config > env vars
	r.auth = flags.auth
	if r.auth.GhToken == "" {
		r.auth.GhToken = r.projectCfg.GhToken
	}
	if r.auth.ApiKey == "" {
		r.auth.ApiKey = os.Getenv("ANTHROPIC_API_KEY")
	}
	if r.auth.OAuthToken == "" {
		r.auth.OAuthToken = os.Getenv("CLAUDE_CODE_OAUTH_TOKEN")
	}

	r.proxyCfg = sandbox.DetectProxyEnv()
}

// resolveNaming derives container name, image tag, and extra domains.
func (r *runner) resolveNaming() {
	r.containerName = config.ContainerName(r.workspace)
	r.imageTag = config.ImageTag(r.containerName)
	r.extraDomains = config.ExtraDomains(r.projectCfg)

	// Add proxy domain to firewall allowlist
	proxyDomains := sandbox.ExtractProxyDomain(r.proxyCfg)
	r.extraDomains = append(r.extraDomains, proxyDomains...)

	// Add plugin MCP domains to firewall allowlist
	pluginDomains := sandbox.ExtractPluginDomains(r.workspace, r.projectCfg.PluginLevel())
	r.extraDomains = append(r.extraDomains, pluginDomains...)
}

// lockedFeatureDigests returns the resolved feature digests from the committed
// devcontainer-lock.json, intersected with the features actually in the current
// config (so removed features don't leave phantom digests in the fingerprint).
func (r *runner) lockedFeatureDigests() map[string]string {
	digests := map[string]string{}
	lock, err := container.LoadLockFile(r.workspace)
	if err != nil {
		return digests
	}
	for ref := range r.projectCfg.Features {
		if f, ok := lock.Features[ref]; ok && f.Integrity != "" {
			digests[ref] = f.Integrity
		}
	}
	return digests
}

// resolveContainer checks fingerprints and existing container state to decide
// whether to reuse, recreate, or rebuild.
func (r *runner) resolveContainer() {
	r.cachedScripts = container.BuildContextScripts()
	scriptMap := make(map[string][]byte, len(r.cachedScripts))
	for _, f := range r.cachedScripts {
		scriptMap[f.Name] = f.Content
	}
	r.cachedDockerfile = container.GenerateBaseDockerfile()
	r.buildInput = config.BuildInput{
		Version:        Version,
		Dockerfile:     r.cachedDockerfile,
		Scripts:        scriptMap,
		ProjectCfg:     r.projectCfg,
		FeatureDigests: r.lockedFeatureDigests(),
	}

	r.fpResult = config.CompareFingerprints(r.buildInput, r.containerName)

	if id, running := container.ContainerRunning(r.ctx, r.cli, r.containerName); running {
		if r.fpResult.ImageMatch && r.fpResult.ContainerMatch {
			r.containerID = id
			r.reused = true
			return
		}
		// Config changed, but don't kill active sessions — reuse the container
		// and let the changes take effect on the next clean start.
		active, aerr := container.HasAnyActiveSessions(r.ctx, r.cli, id)
		if aerr != nil {
			verbose("Could not verify active sessions: " + aerr.Error())
		}
		if active || aerr != nil {
			reason := "sandbox has active sessions"
			if aerr != nil {
				reason = "active sessions could not be verified"
			}
			if r.fpResult.ImageMatch {
				warn("Config changed but " + reason + " — restart to apply")
			} else {
				warn("Image config changed but " + reason + " — restart to apply")
			}
			r.containerID = id
			r.reused = true
			return
		}
		if r.fpResult.ImageMatch {
			info("Container configuration changed — recreating sandbox...")
		} else {
			info("Image configuration changed — rebuilding sandbox...")
		}
		if err := container.Stop(r.ctx, r.cli, id); err != nil {
			verbose("Stop existing container: " + err.Error())
		}
		if err := container.Remove(r.ctx, r.cli, id); err != nil {
			verbose("Remove existing container: " + err.Error())
		}
	} else {
		if id, err := container.FindByLabel(r.ctx, r.cli, r.containerName); err == nil && id != "" {
			if err := container.Remove(r.ctx, r.cli, id); err != nil {
				verbose("Remove stopped container: " + err.Error())
			}
		}
	}
}

// buildAndCreate builds the image (if needed), creates and starts the container,
// runs post-start commands, and saves the fingerprint.
func (r *runner) buildAndCreate() {
	needImageBuild := !r.fpResult.ImageMatch || !container.ImageExists(r.ctx, r.cli, r.imageTag)

	if needImageBuild {
		info("Building sandbox...")
		err := container.BuildImage(r.ctx, r.cli, container.BuildImageOpts{
			ImageTag:     r.imageTag,
			StreamOutput: verbosity >= 1,
			ProjectCfg:   r.projectCfg,
			Version:      Version,
			NoCache:      r.noCache,
			LogFn:        info,
			Cache: &container.BuildCache{
				Dockerfile: r.cachedDockerfile,
				Scripts:    r.cachedScripts,
			},
			Workspace: r.workspace,
		})
		if err != nil {
			die("Failed to build sandbox: " + err.Error())
		}

		// The build (re)wrote devcontainer-lock.json; recompute the fingerprint from
		// the post-build lock so the saved hash matches on-disk state and the next
		// run doesn't rebuild spuriously.
		r.buildInput.FeatureDigests = r.lockedFeatureDigests()
		r.fpResult = config.CompareFingerprints(r.buildInput, r.containerName)
	} else {
		info("Starting sandbox...")
	}

	extraEnv := sandbox.ProxyContainerEnv(r.proxyCfg)

	// When plugins are enabled, allow plugin auto-updates despite DISABLE_NONESSENTIAL_TRAFFIC
	if r.projectCfg.PluginLevel() != "" {
		extraEnv["FORCE_AUTOUPDATE_PLUGINS"] = "true"
	}

	id, err := container.CreateAndStart(r.ctx, r.cli, container.CreateAndStartOpts{
		ContainerName: r.containerName,
		ImageTag:      r.imageTag,
		Workspace:     r.workspace,
		ProjectCfg:    r.projectCfg,
		Auth:          r.auth,
		ExtraEnv:      extraEnv,
	})
	if err != nil {
		die("Failed to start sandbox: " + err.Error())
	}
	r.containerID = id

	if r.projectCfg.PostStartCommand != "" {
		info(fmt.Sprintf("Running postStartCommand from config: %s", r.projectCfg.PostStartCommand))
	}

	if err := container.RunPostStart(r.ctx, r.cli, r.containerID, container.RunPostStartOpts{
		ExtraDomains:     r.extraDomains,
		PostStartCommand: r.projectCfg.PostStartCommand,
		Auth:             r.auth,
	}); err != nil {
		die("Post-start failed: " + err.Error())
	}

	// Inject proxy certificates if configured
	if r.proxyCfg.HasCerts() {
		if err := sandbox.InjectProxyCerts(r.ctx, r.cli, r.containerID, r.proxyCfg, r.logWriter()); err != nil {
			warn("Failed to inject proxy certs: " + err.Error())
		}
	}

	if err := r.fpResult.Save(r.containerName); err != nil {
		warn("Failed to save fingerprint: " + err.Error())
	}
}

// registerCleanup marks whether the container should be torn down on exit.
func (r *runner) registerCleanup(teardown bool) {
	r.mu.Lock()
	r.teardown = teardown
	r.mu.Unlock()
}

// logWriter returns an io.Writer for log output, respecting verbosity.
func (r *runner) logWriter() io.Writer {
	if verbosity < 0 {
		return io.Discard
	}
	return os.Stderr
}

// seedSettings copies settings and session history into the container.
func (r *runner) seedSettings() {
	log := r.logWriter()
	opts := sandbox.SeedOpts{
		ContainerID:  r.containerID,
		Workspace:    r.workspace,
		ExtraDomains: r.extraDomains,
		PluginLevel:  r.projectCfg.PluginLevel(),
		LogW:         log,
	}
	err := sandbox.SeedSettings(r.ctx, r.cli, opts)
	if fatal := failClosed(err, r.noSandbox, "The sandbox cannot be enforced. Re-run with --no-sandbox to launch without it (NOT recommended)."); fatal != nil {
		die("Failed to seed sandbox settings: " + fatal.Error())
	}
	if err != nil {
		warn("Launching without enforced sandbox settings (--no-sandbox): " + err.Error())
	}
	if r.projectCfg.ShouldSeedHistory() {
		if err := sandbox.SeedSessionHistory(r.ctx, r.cli, r.containerID, r.workspace, log); err != nil {
			warn("Failed to seed session history: " + err.Error())
		}
	}
}

// reinjectAuthSecrets re-applies auth tokens and proxy certs into a reused
// container. Tokens on tmpfs may be stale or missing after container restart.
// Unlike buildAndCreate, this skips firewall/git/domain setup (already done).
func (r *runner) reinjectAuthSecrets() {
	if err := container.InjectAuthSecrets(r.ctx, r.cli, r.containerID, r.auth); err != nil {
		warn("Failed to re-inject auth secrets: " + err.Error())
	}

	// Re-inject proxy certs if configured
	if r.proxyCfg.HasCerts() {
		if err := sandbox.InjectProxyCerts(r.ctx, r.cli, r.containerID, r.proxyCfg, r.logWriter()); err != nil {
			warn("Failed to re-inject proxy certs: " + err.Error())
		}
	}
}

// setupSignals configures signal handling for the sandbox session.
func (r *runner) setupSignals() {
	// SIGINT: ignore (Ctrl+C goes to the container process via raw TTY)
	signal.Ignore(syscall.SIGINT)

	// SIGTERM, SIGHUP: clean up container then exit
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		<-sigCh
		r.cancel()
		r.cleanup()
		os.Exit(1)
	}()
}

// exec runs the command interactively in the container and returns the exit code.
func (r *runner) exec(execCmd string, passedArgs []string) int {
	var execCommand []string
	if execCmd == "claude" && r.auth.HasSecrets() {
		execCommand = append([]string{container.AuthWrapperPath, execCmd}, passedArgs...)
	} else {
		execCommand = append([]string{execCmd}, passedArgs...)
	}

	if execCmd == "claude" {
		info("Launching Claude...")
	} else {
		info("Opening shell...")
	}

	exitCode, execID, err := container.ExecInteractive(r.ctx, r.cli, r.containerID, container.ContainerUser, execCommand)

	r.mu.Lock()
	r.execID = execID
	r.mu.Unlock()

	if err != nil {
		warn("Exec failed: " + err.Error())
		exitCode = 1
	}

	if exitCode != 0 && exitCode != 130 {
		warn(fmt.Sprintf("%s exited with code %d", execCmd, exitCode))
	}

	return exitCode
}
