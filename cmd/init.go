package cmd

import (
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

	// Create minimal empty config
	data := []byte("{}\n")

	if err := os.WriteFile(cfgPath, data, 0o644); err != nil {
		die("Failed to write config: " + err.Error())
	}

	info("Created " + cfgPath)
	fmt.Println()
	fmt.Println("Edit the config to customize your sandbox. Example:")
	fmt.Println()
	fmt.Println(`  {`)
	fmt.Println(`    "allowDomains": ["pypi.org", "files.pythonhosted.org"],`)
	fmt.Println(`    "apt": ["python3", "python3-pip"],`)
	fmt.Println(`    "features": {`)
	fmt.Println(`      "ghcr.io/devcontainers/features/node:1": {"version": "20"}`)
	fmt.Println(`    },`)
	fmt.Println(`    "env": {"NODE_ENV": "development"},`)
	fmt.Println(`    "postStartCommand": "npm install"`)
	fmt.Println(`  }`)
	fmt.Println()
	fmt.Println("Fields:")
	fmt.Println("  allowDomains     Additional domains the sandbox can access")
	fmt.Println("  apt              APT packages to install in the image")
	fmt.Println("  features         OCI devcontainer features (languages, runtimes)")
	fmt.Println("  env              Environment variables baked into the image")
	fmt.Println("  postStartCommand Shell command to run after container starts")
	fmt.Println("  exclude          Paths to hide via tmpfs overlays")
	fmt.Println("  workspace        Container working directory (monorepo subpath)")
	fmt.Println("  ghToken          GitHub PAT for git push from container")
	fmt.Println("  seedHistory      Seed host session history (default: true)")
	fmt.Println()
	fmt.Println("Docs: https://github.com/Devon-White/claude-bunker#project-config")
	return nil
}
