package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/Devon-White/claude-bunker/internal/config"
	"github.com/Devon-White/claude-bunker/internal/container"
	"github.com/Devon-White/claude-bunker/internal/devcontainer"
)

// stdinIsTTY is a seam over isTTY so tests can force the non-interactive path.
var stdinIsTTY = isTTY

// nonInteractiveInit decides what init does when stdin is not a terminal.
// Returns write=true when a default config should be written; otherwise an
// error explaining how to proceed. It never silently overwrites.
func nonInteractiveInit(defaults bool) (write bool, err error) {
	if defaults {
		return true, nil
	}
	return false, Coded(ExitError, errors.New(
		"init needs an interactive terminal; re-run with --defaults to write a default config non-interactively"))
}

// abortErr maps a huh form abort (Esc/Ctrl+C) to a cancellation exit code and
// passes other errors through. Callers return this WITHOUT writing a config.
func abortErr(err error) error {
	if errors.Is(err, huh.ErrUserAborted) {
		return Coded(ExitCancelled, errors.New("init cancelled"))
	}
	return err
}

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize or update a project config",
	Long: `Creates or updates .devcontainer/devcontainer.json.

Run this in your project root to customize the sandbox behavior.
If a config already exists, the wizard pre-selects your current settings
so you can toggle items on/off or add new ones.`,
	RunE: runInit,
}

func runInit(cmd *cobra.Command, args []string) error {
	initVerbosity(cmd)
	dryRun, _ = cmd.Flags().GetBool("dry-run")

	workspace := resolveWorkspace()

	cfgPath := devcontainer.DevContainerPath(workspace)

	// Load existing config if present (for pre-populating the wizard)
	var existing *config.ProjectConfig
	if _, err := os.Stat(cfgPath); err == nil {
		if loaded, _, err := devcontainer.LoadProjectConfig(workspace); err == nil {
			existing = &loaded
			info("Updating existing devcontainer: " + cfgPath)
		}
	}

	// Create directory structure (skipped under dry-run — no host mutation).
	dir := filepath.Dir(cfgPath)
	if !dryRun {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			die("Failed to create config directory: " + err.Error())
		}
	}

	// Non-interactive: never silently overwrite. Require --defaults to write.
	if !stdinIsTTY() {
		defaults, _ := cmd.Flags().GetBool("defaults")
		write, err := nonInteractiveInit(defaults)
		if err != nil {
			return err
		}
		if write {
			return writeDevContainer(workspace, config.ProjectConfig{})
		}
		return nil
	}

	// Interactive wizard — multi-page form
	//
	// Page 1: Languages / runtimes
	// Page 2: Network (allowDomains, ghToken)
	// Page 3: Plugins (plugins level)
	// Page 4: Extras (apt packages, env vars, postStartCommand, seedHistory)

	// --- Page 1: Languages ---
	selected, err := selectLanguages(existing)
	if err != nil {
		return abortErr(err)
	}

	// Pre-fetch tags/versions concurrently while we collect other settings
	type prefetchResult struct {
		tag      string
		versions []string
	}
	results := make([]prefetchResult, len(selected))
	var wg sync.WaitGroup
	for i, idx := range selected {
		wg.Add(1)
		go func(i, idx int) {
			defer wg.Done()
			p := container.Presets[idx]
			results[i].tag = container.LatestFeatureTag(p.FeatureRepo)
			if live, err := container.FetchSupportedVersions(p); err == nil {
				results[i].versions = live
			}
		}(i, idx)
	}

	// --- Pages 2-4: Settings form (runs while prefetch continues) ---
	settings, err := selectSettings(existing)
	if err != nil {
		return abortErr(err)
	}

	// --- Language version selection (sequential, needs prefetch results) ---
	wg.Wait()

	selections := make([]initSelection, 0, len(selected))
	for i, idx := range selected {
		preset := container.Presets[idx]
		currentVersion := existingVersion(existing, preset)
		version, err := selectVersion(preset, results[i].tag, results[i].versions, currentVersion)
		if err != nil {
			return abortErr(err)
		}
		selections = append(selections, initSelection{
			preset:  preset,
			tag:     results[i].tag,
			version: version,
		})
	}

	// Build config map from all inputs
	cfg := buildConfig(selections)
	mergeSettings(cfg, settings)

	pc, err := mapToProjectConfig(cfg)
	if err != nil {
		return fmt.Errorf("building config: %w", err)
	}
	return writeDevContainer(workspace, pc)
}

// preselectedLanguages returns the preset indices that match features in the existing config.
func preselectedLanguages(existing *config.ProjectConfig) []int {
	if existing == nil || len(existing.Features) == 0 {
		return nil
	}
	var selected []int
	for i, p := range container.Presets {
		for featureRef := range existing.Features {
			// featureRef is "ghcr.io/devcontainers/features/node:1"
			repo := strings.SplitN(featureRef, ":", 2)[0]
			if repo == p.FeatureRepo {
				selected = append(selected, i)
				break
			}
		}
	}
	return selected
}

// existingVersion returns the version string configured for a preset in the existing config.
func existingVersion(existing *config.ProjectConfig, preset container.LanguagePreset) string {
	if existing == nil {
		return ""
	}
	for featureRef, opts := range existing.Features {
		repo := strings.SplitN(featureRef, ":", 2)[0]
		if repo == preset.FeatureRepo {
			if v, ok := opts[preset.VersionOption]; ok {
				if s, ok := v.(string); ok {
					return s
				}
			}
		}
	}
	return ""
}

// selectLanguages shows a multi-select picker for languages/runtimes.
// If existing config is provided, previously selected languages are pre-checked.
func selectLanguages(existing *config.ProjectConfig) ([]int, error) {
	options := make([]huh.Option[int], len(container.Presets))
	for i, p := range container.Presets {
		options[i] = huh.NewOption(p.Label, i)
	}

	selected := preselectedLanguages(existing)
	err := huh.NewForm(
		huh.NewGroup(
			huh.NewMultiSelect[int]().
				Title("Select languages / runtimes").
				Description("Space to toggle, Enter to confirm").
				Options(options...).
				Value(&selected),
		),
	).Run()

	if err != nil {
		return nil, err
	}
	return selected, nil
}

// selectVersion shows a select picker for a language version.
// If prefetched is non-empty it is used directly; otherwise falls back to
// the preset's hardcoded CommonVersions. If currentVersion is non-empty and
// present in the list, it is pre-selected.
func selectVersion(preset container.LanguagePreset, tag string, prefetched []string, currentVersion string) (string, error) {
	versions := prefetched
	if len(versions) == 0 {
		versions = preset.CommonVersions
	}

	// If the current version is not in the list, prepend it so the user can keep it
	if currentVersion != "" && !slices.Contains(versions, currentVersion) {
		versions = append([]string{currentVersion}, versions...)
	}

	options := make([]huh.Option[string], len(versions))
	for i, v := range versions {
		label := v
		if v == currentVersion {
			label = v + " (current)"
		}
		options[i] = huh.NewOption(label, v)
	}

	version := currentVersion
	err := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title(fmt.Sprintf("%s version (%s:%s)", preset.Label, preset.FeatureRepo, tag)).
				Options(options...).
				Value(&version),
		),
	).Run()

	if err != nil {
		return "", err
	}
	return version, nil
}

type initSelection struct {
	preset  container.LanguagePreset
	tag     string
	version string
}

// initSettings holds values collected from the settings form pages.
type initSettings struct {
	allowDomains     string // comma-separated
	ghToken          string
	plugins          string
	aptPackages      string // space-separated
	envVars          string // KEY=VALUE per line
	onCreateCommand  string
	postStartCommand string
	seedHistory      bool
}

// settingKey identifies a toggleable setting section.
const (
	settingNetwork  = "network"
	settingPlugins  = "plugins"
	settingPackages = "packages"
	settingEnv      = "env"
	settingHooks    = "hooks"
)

// aptPackagesFeatureRef is the standard community devcontainer feature used to
// express extra apt packages. Unlike the old bunker-specific "apt" field, this
// is a normal user feature: VS Code/Codespaces install it directly, and
// claude-bunker resolves + installs it like any other feature (it is not in
// bunkerManagedFeaturePrefixes, so it is preserved in the committed file).
const aptPackagesFeatureRef = "ghcr.io/rocker-org/devcontainer-features/apt-packages:1"

// initSettingsFromConfig builds pre-populated initSettings from an existing config.
// It also returns which setting sections should be pre-enabled.
func initSettingsFromConfig(existing *config.ProjectConfig) (initSettings, []string) {
	s := initSettings{
		seedHistory: true,
	}
	if existing == nil {
		return s, nil
	}

	var enabled []string

	// Network: domains that aren't from language presets, plus ghToken
	if len(existing.AllowDomains) > 0 || existing.GhToken != "" {
		enabled = append(enabled, settingNetwork)
		// Filter out domains that come from language preset selections
		presetDomains := make(map[string]bool)
		for _, p := range container.Presets {
			for _, d := range p.Domains {
				presetDomains[d] = true
			}
		}
		var extraDomains []string
		for _, d := range existing.AllowDomains {
			if !presetDomains[d] {
				extraDomains = append(extraDomains, d)
			}
		}
		s.allowDomains = strings.Join(extraDomains, ", ")
		s.ghToken = existing.GhToken
	}

	// Plugins
	if existing.Plugins != "" {
		enabled = append(enabled, settingPlugins)
		s.plugins = existing.Plugins
	}

	// Packages: pre-populate from the standard apt-packages feature entry
	// (comma-separated in the feature option; space-separated in the wizard).
	if opts, ok := existing.Features[aptPackagesFeatureRef]; ok {
		if raw, ok := opts["packages"].(string); ok && raw != "" {
			var pkgs []string
			for p := range strings.SplitSeq(raw, ",") {
				p = strings.TrimSpace(p)
				if p != "" {
					pkgs = append(pkgs, p)
				}
			}
			if len(pkgs) > 0 {
				enabled = append(enabled, settingPackages)
				s.aptPackages = strings.Join(pkgs, " ")
			}
		}
	}

	// Env
	if len(existing.Env) > 0 {
		enabled = append(enabled, settingEnv)
		var pairs []string
		for k, v := range existing.Env {
			pairs = append(pairs, k+"="+v)
		}
		sort.Strings(pairs)
		s.envVars = strings.Join(pairs, ", ")
	}

	// Hooks
	if existing.OnCreateCommand != "" || existing.PostStartCommand != "" || (existing.SeedHistory != nil && !*existing.SeedHistory) {
		enabled = append(enabled, settingHooks)
		s.onCreateCommand = existing.OnCreateCommand
		s.postStartCommand = existing.PostStartCommand
		s.seedHistory = existing.ShouldSeedHistory()
	}

	return s, enabled
}

// selectSettings shows a toggle picker followed by conditional detail pages.
// Only the sections the user enables are shown as subsequent form pages.
// If existing config is provided, settings are pre-populated.
func selectSettings(existing *config.ProjectConfig) (initSettings, error) {
	s, enabled := initSettingsFromConfig(existing)

	err := huh.NewForm(
		// Page 1: Toggle which settings to configure
		huh.NewGroup(
			huh.NewMultiSelect[string]().
				Title("Configure additional settings").
				Description("Space to toggle, Enter to confirm. Skipped sections use defaults.").
				Options(
					huh.NewOption("Network & Auth — firewall domains, GitHub token", settingNetwork),
					huh.NewOption("Plugins — MCP server & plugin forwarding", settingPlugins),
					huh.NewOption("Packages — extra apt packages in the sandbox", settingPackages),
					huh.NewOption("Environment — env vars for the container", settingEnv),
					huh.NewOption("Hooks — post-start command, session history", settingHooks),
				).
				Value(&enabled),
		).Title("Settings"),

		// Page 2: Network (shown only if toggled)
		huh.NewGroup(
			huh.NewInput().
				Title("Extra allowed domains").
				Description("Comma-separated domains to allow through the firewall (e.g. registry.npmjs.org, pypi.org)").
				Placeholder("leave empty for defaults only").
				Value(&s.allowDomains),
			huh.NewInput().
				Title("GitHub token").
				Description("Fine-grained PAT for git auth inside the sandbox (or use $GH_TOKEN)").
				Placeholder("ghp_... or $GH_TOKEN").
				Value(&s.ghToken),
		).Title("Network & Auth").
			WithHideFunc(func() bool { return !slices.Contains(enabled, settingNetwork) }),

		// Page 3: Plugins (shown only if toggled)
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Plugin / MCP support level").
				Description("Forward MCP server configs and plugin cache into the sandbox").
				Options(
					huh.NewOption("Project — allow MCP servers from .mcp.json", config.PluginLevelProject),
					huh.NewOption("User — above + ~/.claude.json MCP configs & plugin cache", config.PluginLevelUser),
					huh.NewOption("All — above + managed-mcp.json (enterprise)", config.PluginLevelAll),
				).
				Value(&s.plugins),
		).Title("Plugins").
			WithHideFunc(func() bool { return !slices.Contains(enabled, settingPlugins) }),

		// Page 4: Packages (shown only if toggled)
		huh.NewGroup(
			huh.NewInput().
				Title("Extra apt packages").
				Description("Space-separated packages to install in the sandbox image").
				Placeholder("e.g. jq ripgrep curl").
				Value(&s.aptPackages),
		).Title("Packages").
			WithHideFunc(func() bool { return !slices.Contains(enabled, settingPackages) }),

		// Page 5: Environment (shown only if toggled)
		huh.NewGroup(
			huh.NewInput().
				Title("Environment variables").
				Description("KEY=VALUE pairs, comma-separated").
				Placeholder("e.g. NODE_ENV=development,DEBUG=1").
				Value(&s.envVars),
		).Title("Environment").
			WithHideFunc(func() bool { return !slices.Contains(enabled, settingEnv) }),

		// Page 6: Hooks (shown only if toggled)
		huh.NewGroup(
			huh.NewInput().
				Title("On-create command").
				Description("Shell command baked into the image at build time (e.g. pip install uv)").
				Placeholder("leave empty for none").
				Value(&s.onCreateCommand),
			huh.NewInput().
				Title("Post-start command").
				Description("Shell command to run after the sandbox starts (e.g. npm install)").
				Placeholder("leave empty for none").
				Value(&s.postStartCommand),
			huh.NewConfirm().
				Title("Seed session history").
				Description("Copy previous Claude sessions into the sandbox for --resume support").
				Affirmative("Yes").
				Negative("No").
				Value(&s.seedHistory),
		).Title("Hooks & History").
			WithHideFunc(func() bool { return !slices.Contains(enabled, settingHooks) }),
	).Run()

	if err != nil {
		return initSettings{}, err
	}
	return s, nil
}

// buildConfig constructs a config map from selected languages/versions.
func buildConfig(selections []initSelection) map[string]any {
	cfg := make(map[string]any)

	if len(selections) == 0 {
		return cfg
	}

	features := make(map[string]any)
	var domains []string

	for _, s := range selections {
		ref := fmt.Sprintf("%s:%s", s.preset.FeatureRepo, s.tag)
		features[ref] = map[string]any{
			s.preset.VersionOption: s.version,
		}
		domains = append(domains, s.preset.Domains...)
	}

	sort.Strings(domains)

	cfg["features"] = features
	if len(domains) > 0 {
		cfg["allowDomains"] = domains
	}
	return cfg
}

// mergeSettings merges user-selected settings into the config map.
// Only sets keys for non-default values to keep the config minimal.
func mergeSettings(cfg map[string]any, s initSettings) {
	if cfg == nil {
		return
	}

	// Allowed domains: merge with any language-preset domains
	if s.allowDomains != "" {
		var extra []string
		for d := range strings.SplitSeq(s.allowDomains, ",") {
			d = strings.TrimSpace(d)
			if d != "" {
				extra = append(extra, d)
			}
		}
		if len(extra) > 0 {
			existing, _ := cfg["allowDomains"].([]string)
			cfg["allowDomains"] = append(existing, extra...)
		}
	}

	if s.ghToken != "" {
		cfg["ghToken"] = s.ghToken
	}

	if s.plugins != "" {
		cfg["plugins"] = s.plugins
	}

	if s.aptPackages != "" {
		if pkgs := strings.Fields(s.aptPackages); len(pkgs) > 0 {
			feats, _ := cfg["features"].(map[string]any)
			if feats == nil {
				feats = map[string]any{}
			}
			feats[aptPackagesFeatureRef] = map[string]any{
				"packages": strings.Join(pkgs, ","),
			}
			cfg["features"] = feats
		}
	}

	if s.envVars != "" {
		env := make(map[string]string)
		for pair := range strings.SplitSeq(s.envVars, ",") {
			pair = strings.TrimSpace(pair)
			if k, v, ok := strings.Cut(pair, "="); ok {
				k = strings.TrimSpace(k)
				v = strings.TrimSpace(v)
				if k != "" {
					env[k] = v
				}
			}
		}
		if len(env) > 0 {
			cfg["env"] = env
		}
	}

	if s.onCreateCommand != "" {
		cfg["onCreateCommand"] = s.onCreateCommand
	}

	if s.postStartCommand != "" {
		cfg["postStartCommand"] = s.postStartCommand
	}

	if !s.seedHistory {
		cfg["seedHistory"] = false
	}
}

// writeDevContainer generates .devcontainer/devcontainer.json from the wizard's
// config and writes it. The generated file references the claude-code feature
// and a portable base image for the VS Code path; bunker's own build ignores
// the image and strips the feature (it installs Claude Code natively).
func writeDevContainer(workspace string, cfg config.ProjectConfig) error {
	name := filepath.Base(workspace) + " (bunkered)"
	data, err := devcontainer.Generate(cfg, devcontainer.GenerateOpts{
		Name:               name,
		Image:              "mcr.microsoft.com/devcontainers/base:debian",
		ClaudeCodeFeature:  "ghcr.io/anthropics/devcontainer-features/claude-code:1",
		FirewallFeature:    "ghcr.io/Devon-White/claude-bunker/firewall:0",
		HardeningFeature:   "ghcr.io/Devon-White/claude-bunker/hardening:0",
		CommonUtilsFeature: "ghcr.io/devcontainers/features/common-utils:2",
	})
	if err != nil {
		return fmt.Errorf("generating devcontainer.json: %w", err)
	}
	p := devcontainer.DevContainerPath(workspace)
	if dryRun {
		planf("would write %s", p)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		return err
	}
	success("Saved " + p)

	// Sibling seccomp.json for the portable (VS Code/Codespaces) path,
	// referenced by devcontainer.json's runArgs above. Sourced from
	// container.SeccompProfileJSON(), the single profile also used for
	// bunker's own native container creation.
	seccompPath := filepath.Join(filepath.Dir(p), "seccomp.json")
	if err := os.WriteFile(seccompPath, []byte(container.SeccompProfileJSON()), 0o644); err != nil {
		return fmt.Errorf("writing seccomp.json: %w", err)
	}
	success("Saved " + seccompPath)
	return nil
}

// mapToProjectConfig converts the wizard's config map to a ProjectConfig via JSON
// (the map keys are ProjectConfig's json tags).
func mapToProjectConfig(m map[string]any) (config.ProjectConfig, error) {
	var pc config.ProjectConfig
	if len(m) == 0 {
		return pc, nil
	}
	data, err := json.Marshal(m)
	if err != nil {
		return pc, err
	}
	err = json.Unmarshal(data, &pc)
	return pc, err
}
