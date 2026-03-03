package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/Devon-White/claude-bunker/internal/config"
	"github.com/Devon-White/claude-bunker/internal/container"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize a project config",
	Long: `Creates a .claude/.claude-bunker/config.json with sensible defaults.

Run this in your project root to customize the sandbox behavior.
If a config already exists, it will not be overwritten.`,
	RunE: runInit,
}

func runInit(cmd *cobra.Command, args []string) error {
	initVerbosity(cmd)

	workspace := resolveWorkspace()

	cfgPath := config.ConfigPath(workspace)

	// Check if config already exists
	if _, err := os.Stat(cfgPath); err == nil {
		info("Config already exists: " + cfgPath)
		fmt.Println(dimStyle.Render("  Edit it directly to make changes."))
		return nil
	}

	// Create directory structure
	dir := filepath.Dir(cfgPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		die("Failed to create config directory: " + err.Error())
	}

	// Non-interactive fallback: write empty config if stdin is not a terminal
	if !isTTY() {
		return writeConfig(cfgPath, nil)
	}

	// Interactive wizard
	selected, err := selectLanguages()
	if err != nil {
		// User aborted (Ctrl+C) → write empty config
		return writeConfig(cfgPath, nil)
	}

	if len(selected) == 0 {
		return writeConfig(cfgPath, nil)
	}

	// Pre-fetch all tags and versions concurrently
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
	wg.Wait()

	// Interactive version selection (sequential — needs user input)
	selections := make([]initSelection, 0, len(selected))

	for i, idx := range selected {
		preset := container.Presets[idx]
		tag := results[i].tag
		versions := results[i].versions

		// Pick language version
		version, err := selectVersion(preset, tag, versions)
		if err != nil {
			// User aborted → write empty config
			return writeConfig(cfgPath, nil)
		}

		selections = append(selections, initSelection{
			preset:  preset,
			tag:     tag,
			version: version,
		})
	}

	// Build config map
	cfg := buildConfig(selections)

	return writeConfig(cfgPath, cfg)
}

// selectLanguages shows a multi-select picker for languages/runtimes.
func selectLanguages() ([]int, error) {
	options := make([]huh.Option[int], len(container.Presets))
	for i, p := range container.Presets {
		options[i] = huh.NewOption(p.Label, i)
	}

	var selected []int
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
// the preset's hardcoded CommonVersions.
func selectVersion(preset container.LanguagePreset, tag string, prefetched []string) (string, error) {
	versions := prefetched
	if len(versions) == 0 {
		versions = preset.CommonVersions
	}

	options := make([]huh.Option[string], len(versions))
	for i, v := range versions {
		options[i] = huh.NewOption(v, v)
	}

	var version string
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

// buildConfig constructs a config map from selected languages/versions.
func buildConfig(selections []initSelection) map[string]interface{} {
	if len(selections) == 0 {
		return nil
	}

	features := make(map[string]interface{})
	var domains []string

	for _, s := range selections {
		ref := fmt.Sprintf("%s:%s", s.preset.FeatureRepo, s.tag)
		features[ref] = map[string]interface{}{
			s.preset.VersionOption: s.version,
		}
		domains = append(domains, s.preset.Domains...)
	}

	sort.Strings(domains)

	cfg := map[string]interface{}{
		"features": features,
	}
	if len(domains) > 0 {
		cfg["allowDomains"] = domains
	}
	return cfg
}

// writeConfig writes config to disk. If cfg is nil, writes "{}".
func writeConfig(path string, cfg map[string]interface{}) error {
	var data []byte
	if cfg == nil || len(cfg) == 0 {
		data = []byte("{}\n")
	} else {
		var err error
		data, err = json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			return fmt.Errorf("marshaling config: %w", err)
		}
		data = append(data, '\n')
	}

	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}

	success("Created " + path)
	return nil
}
