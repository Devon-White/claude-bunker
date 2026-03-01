package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/Devon-White/claude-bunker/internal/config"
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

	workspace, err := os.Getwd()
	if err != nil {
		die("cannot determine workspace: " + err.Error())
	}

	cfgPath := config.ConfigPath(workspace)

	// Check if config already exists
	if _, err := os.Stat(cfgPath); err == nil {
		fmt.Printf("Config already exists: %s\n", cfgPath)
		fmt.Println("Edit it directly to make changes.")
		return nil
	}

	// Create directory structure
	dir := filepath.Dir(cfgPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		die("Failed to create config directory: " + err.Error())
	}

	// Create default config
	cfg := map[string]interface{}{
		"workspace":    "/workspace",
		"exclude":      []string{},
		"allowDomains": []string{},
		"apt":          []string{},
		"env":          map[string]string{},
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		die("Failed to marshal config: " + err.Error())
	}
	data = append(data, '\n')

	if err := os.WriteFile(cfgPath, data, 0o644); err != nil {
		die("Failed to write config: " + err.Error())
	}

	info("Created " + cfgPath)
	fmt.Println()
	fmt.Println("Available options:")
	fmt.Println("  workspace      - Container working directory (default: /workspace)")
	fmt.Println("  exclude        - Paths to hide from sandbox via tmpfs overlays")
	fmt.Println("  allowDomains   - Additional domains the sandbox can access")
	fmt.Println("  apt            - APT packages to install in the sandbox image")
	fmt.Println("  env            - Environment variables to set in the container")
	fmt.Println("  features       - OCI dev container features to install")
	fmt.Println("  postCreateCommand - Shell command to run after container creation")
	return nil
}
