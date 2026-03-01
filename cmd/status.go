package cmd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/spf13/cobra"

	"github.com/Devon-White/claude-bunker/internal/config"
	ctr "github.com/Devon-White/claude-bunker/internal/container"
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

	// Find container (running or stopped)
	f := filters.NewArgs()
	f.Add("label", "claude-bunker="+containerName)
	containers, err := cli.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: f,
	})
	if err != nil {
		return fmt.Errorf("failed to query containers: %w", err)
	}

	fmt.Printf("Workspace:  %s\n", workspace)
	fmt.Printf("Container:  %s\n", containerName)
	fmt.Printf("Image:      %s\n", imageTag)

	if len(containers) == 0 {
		fmt.Printf("State:      not created\n")
		return nil
	}

	c := containers[0]
	state := c.State
	fmt.Printf("State:      %s\n", state)
	idShort := c.ID
	if len(idShort) > 12 {
		idShort = idShort[:12]
	}
	fmt.Printf("ID:         %s\n", idShort)

	if state == "running" {
		// Show uptime
		inspect, err := cli.ContainerInspect(ctx, c.ID)
		if err == nil && inspect.State != nil && inspect.State.StartedAt != "" {
			started, err := time.Parse(time.RFC3339Nano, inspect.State.StartedAt)
			if err == nil {
				uptime := time.Since(started).Truncate(time.Second)
				fmt.Printf("Uptime:     %s\n", formatDuration(uptime))
			}
		}

		// Check for active sessions
		sessions := listActiveSessions(ctx, cli, c.ID)
		if len(sessions) > 0 {
			fmt.Printf("Sessions:   %s\n", strings.Join(sessions, ", "))
		} else {
			fmt.Printf("Sessions:   none\n")
		}
	}

	// Image info
	imgInspect, _, err := cli.ImageInspectWithRaw(ctx, imageTag)
	if err == nil {
		created, err := time.Parse(time.RFC3339Nano, imgInspect.Created)
		if err == nil {
			fmt.Printf("Image built: %s\n", created.Local().Format("2006-01-02 15:04:05"))
		}
	}

	// Resolved project config
	cfg, cfgErr := config.LoadProjectConfig(workspace)
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
	fmt.Println("Config:")
	if len(cfg.Apt) > 0 {
		fmt.Printf("  apt:        %s\n", strings.Join(cfg.Apt, ", "))
	}
	if len(cfg.Features) > 0 {
		names := make([]string, 0, len(cfg.Features))
		for name := range cfg.Features {
			names = append(names, name)
		}
		fmt.Printf("  features:   %s\n", strings.Join(names, ", "))
	}
	if len(cfg.Env) > 0 {
		pairs := make([]string, 0, len(cfg.Env))
		for k, v := range cfg.Env {
			pairs = append(pairs, k+"="+v)
		}
		fmt.Printf("  env:        %s\n", strings.Join(pairs, ", "))
	}
	if len(cfg.AllowDomains) > 0 {
		fmt.Printf("  domains:    %s\n", strings.Join(cfg.AllowDomains, ", "))
	}
	if cfg.PostStartCommand != "" {
		fmt.Printf("  postStart:  %s\n", cfg.PostStartCommand)
	}
	if cfg.Workspace != "" {
		fmt.Printf("  workspace:  %s\n", cfg.Workspace)
	}
	if len(cfg.Exclude) > 0 {
		fmt.Printf("  exclude:    %s\n", strings.Join(cfg.Exclude, ", "))
	}
}

// listActiveSessions returns the names of active interactive sessions (claude, bash).
func listActiveSessions(ctx context.Context, cli interface {
	ContainerTop(ctx context.Context, containerID string, arguments []string) (container.ContainerTopOKBody, error)
}, containerID string) []string {
	top, err := cli.ContainerTop(ctx, containerID, []string{"-eo", "comm"})
	if err != nil {
		return nil
	}
	counts := map[string]int{}
	for _, proc := range top.Processes {
		for _, field := range proc {
			if field == "claude" || field == "bash" {
				counts[field]++
			}
		}
	}
	var sessions []string
	for name, count := range counts {
		if count == 1 {
			sessions = append(sessions, name)
		} else {
			sessions = append(sessions, fmt.Sprintf("%s (x%d)", name, count))
		}
	}
	return sessions
}

// formatDuration returns a human-readable duration string like "2h 15m 30s".
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		m := int(d.Minutes())
		s := int(d.Seconds()) % 60
		return fmt.Sprintf("%dm %ds", m, s)
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	return fmt.Sprintf("%dh %dm", h, m)
}
