package cmd

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Devon-White/claude-bunker/internal/config"
	ctr "github.com/Devon-White/claude-bunker/internal/container"
	"github.com/Devon-White/claude-bunker/internal/devcontainer"
	"github.com/Devon-White/claude-bunker/internal/sessions"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show sandbox status",
	Long:  "Shows the current state of the sandbox container including image info, uptime, workspace, and active sessions.",
	RunE:  runStatus,
}

func runStatus(cmd *cobra.Command, args []string) error {
	initVerbosity(cmd)
	ctx := context.Background()

	cli, err := ctr.NewClient()
	if err != nil {
		return fmt.Errorf("docker client: %w", err)
	}
	defer cli.Close()

	workspace := resolveWorkspace()

	containerName := config.ContainerName(workspace)
	imageTag := config.ImageTag(containerName)

	fmt.Println(kvLine("Workspace:", workspace))
	fmt.Println(kvLine("Container:", containerName))
	fmt.Println(kvLine("Image:", imageTag))

	// Find container (running or stopped)
	id, err := ctr.FindByLabel(ctx, cli, containerName)
	if err != nil {
		return fmt.Errorf("failed to query containers: %w", err)
	}
	if id == "" {
		fmt.Println(kvLineStyled("State:", "not created", stateStyle("not created")))
		return nil
	}

	inspect, err := cli.ContainerInspect(ctx, id)
	if err != nil {
		return fmt.Errorf("failed to inspect container: %w", err)
	}
	state := inspect.State.Status
	fmt.Println(kvLineStyled("State:", state, stateStyle(state)))
	idShort := id
	if len(idShort) > 12 {
		idShort = idShort[:12]
	}
	fmt.Println(kvLine("ID:", idShort))

	if state == "running" {
		// Show uptime
		if inspect.State != nil && inspect.State.StartedAt != "" {
			started, err := time.Parse(time.RFC3339Nano, inspect.State.StartedAt)
			if err == nil {
				fmt.Println(kvLine("Uptime:", sessions.FormatUptime(started)))
			}
		}

		// Check for active sessions
		mgr := sessions.NewManager(cli)
		tree, _ := mgr.GetProcessTree(ctx, id)
		if len(tree) > 0 {
			names := make([]string, len(tree))
			for i, s := range tree {
				names[i] = s.Command
			}
			fmt.Println(kvLine("Sessions:", strings.Join(names, ", ")))
		} else {
			fmt.Println(kvLine("Sessions:", "none"))
		}
	}

	// Image info
	imgInspect, _, err := cli.ImageInspectWithRaw(ctx, imageTag)
	if err == nil {
		created, err := time.Parse(time.RFC3339Nano, imgInspect.Created)
		if err == nil {
			fmt.Println(kvLine("Image built:", created.Local().Format("2006-01-02 15:04:05")))
		}
	}

	// Resolved project config
	cfg, _, cfgErr := devcontainer.LoadProjectConfig(workspace)
	if cfgErr == nil {
		printResolvedConfig(cfg)
	}

	return nil
}

// printResolvedConfig shows non-empty project config fields.
func printResolvedConfig(cfg config.ProjectConfig) {
	hasConfig := len(cfg.Apt) > 0 || len(cfg.Features) > 0 || len(cfg.Env) > 0 ||
		len(cfg.AllowDomains) > 0 || cfg.PostStartCommand != "" ||
		cfg.Workspace != "" || len(cfg.Exclude) > 0
	if !hasConfig {
		return
	}

	fmt.Println()
	fmt.Println(sectionHeaderStyle.Render("Config:"))
	if len(cfg.Apt) > 0 {
		fmt.Println(configLine("apt:", strings.Join(cfg.Apt, ", ")))
	}
	if len(cfg.Features) > 0 {
		names := make([]string, 0, len(cfg.Features))
		for name := range cfg.Features {
			names = append(names, name)
		}
		sort.Strings(names)
		fmt.Println(configLine("features:", strings.Join(names, ", ")))
	}
	if len(cfg.Env) > 0 {
		pairs := make([]string, 0, len(cfg.Env))
		for k, v := range cfg.Env {
			pairs = append(pairs, k+"="+v)
		}
		sort.Strings(pairs)
		fmt.Println(configLine("env:", strings.Join(pairs, ", ")))
	}
	if len(cfg.AllowDomains) > 0 {
		fmt.Println(configLine("domains:", strings.Join(cfg.AllowDomains, ", ")))
	}
	if cfg.PostStartCommand != "" {
		fmt.Println(configLine("postStart:", cfg.PostStartCommand))
	}
	if cfg.Workspace != "" {
		fmt.Println(configLine("workspace:", cfg.Workspace))
	}
	if len(cfg.Exclude) > 0 {
		fmt.Println(configLine("exclude:", strings.Join(cfg.Exclude, ", ")))
	}
}


