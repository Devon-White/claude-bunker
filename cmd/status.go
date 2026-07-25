package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/docker/docker/client"
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

// statusInfo is the machine-readable representation of `status`'s output.
type statusInfo struct {
	Workspace  string   `json:"workspace"`
	Container  string   `json:"container"`
	Image      string   `json:"image"`
	State      string   `json:"state"`
	ID         string   `json:"id,omitempty"`
	Uptime     string   `json:"uptime,omitempty"`
	Sessions   []string `json:"sessions,omitempty"`
	ImageBuilt string   `json:"image_built,omitempty"`
}

// gatherStatus collects the current sandbox state into a statusInfo struct
// without printing anything, so callers can render it as text or JSON.
func gatherStatus(ctx context.Context, cli *client.Client, workspace string) (statusInfo, error) {
	containerName := config.ContainerName(workspace)
	imageTag := config.ImageTag(containerName)

	s := statusInfo{
		Workspace: workspace,
		Container: containerName,
		Image:     imageTag,
	}

	// Find container (running or stopped)
	id, err := ctr.FindByLabel(ctx, cli, containerName)
	if err != nil {
		return s, fmt.Errorf("failed to query containers: %w", err)
	}
	if id == "" {
		s.State = "not created"
		return s, nil
	}

	inspect, err := cli.ContainerInspect(ctx, id)
	if err != nil {
		return s, fmt.Errorf("failed to inspect container: %w", err)
	}
	s.State = inspect.State.Status
	idShort := id
	if len(idShort) > 12 {
		idShort = idShort[:12]
	}
	s.ID = idShort

	if s.State == "running" {
		// Show uptime
		if inspect.State != nil && inspect.State.StartedAt != "" {
			started, err := time.Parse(time.RFC3339Nano, inspect.State.StartedAt)
			if err == nil {
				s.Uptime = sessions.FormatUptime(started)
			}
		}

		// Check for active sessions
		mgr := sessions.NewManager(cli)
		tree, _ := mgr.GetProcessTree(ctx, id)
		if len(tree) > 0 {
			names := make([]string, len(tree))
			for i, sess := range tree {
				names[i] = sess.Command
			}
			s.Sessions = names
		}
	}

	// Image info
	imgInspect, _, err := cli.ImageInspectWithRaw(ctx, imageTag)
	if err == nil {
		created, err := time.Parse(time.RFC3339Nano, imgInspect.Created)
		if err == nil {
			s.ImageBuilt = created.Local().Format("2006-01-02 15:04:05")
		}
	}

	return s, nil
}

// renderStatusText prints the human-readable status output (the pre-refactor
// behavior of runStatus, byte-identical).
func renderStatusText(s statusInfo) {
	fmt.Println(kvLine("Workspace:", s.Workspace))
	fmt.Println(kvLine("Container:", s.Container))
	fmt.Println(kvLine("Image:", s.Image))

	if s.State == "not created" {
		fmt.Println(kvLineStyled("State:", "not created", stateStyle("not created")))
		return
	}

	fmt.Println(kvLineStyled("State:", s.State, stateStyle(s.State)))
	fmt.Println(kvLine("ID:", s.ID))

	if s.State == "running" {
		if s.Uptime != "" {
			fmt.Println(kvLine("Uptime:", s.Uptime))
		}
		if len(s.Sessions) > 0 {
			fmt.Println(kvLine("Sessions:", strings.Join(s.Sessions, ", ")))
		} else {
			fmt.Println(kvLine("Sessions:", "none"))
		}
	}

	if s.ImageBuilt != "" {
		fmt.Println(kvLine("Image built:", s.ImageBuilt))
	}
}

func runStatus(cmd *cobra.Command, args []string) error {
	initVerbosity(cmd)
	ctx := context.Background()

	cli, err := dockerClient()
	if err != nil {
		return err
	}
	defer cli.Close()

	s, err := gatherStatus(ctx, cli, resolveWorkspace())
	if err != nil {
		return err
	}

	if j, _ := cmd.Flags().GetBool("json"); j {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(s)
	}

	renderStatusText(s)

	// The resolved-config section stays text-only (not in the JSON struct for now).
	if cfg, _, cfgErr := devcontainer.LoadProjectConfig(resolveWorkspace()); cfgErr == nil {
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
