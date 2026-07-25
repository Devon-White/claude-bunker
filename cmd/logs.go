package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/spf13/cobra"

	"github.com/Devon-White/claude-bunker/internal/config"
	ctr "github.com/Devon-White/claude-bunker/internal/container"
)

var logsCmd = &cobra.Command{
	Use:   "logs",
	Short: "Show sandbox container logs",
	Long:  "Shows the Docker container logs for the current project's sandbox. Useful for troubleshooting startup issues.",
	RunE:  runLogs,
}

func init() {
	logsCmd.Flags().BoolP("follow", "f", false, "Follow log output")
	logsCmd.Flags().String("tail", "100", "Number of lines to show from the end (or \"all\")")
}

func runLogs(cmd *cobra.Command, args []string) error {
	initVerbosity(cmd)
	ctx := context.Background()

	cli, err := dockerClient()
	if err != nil {
		return err
	}
	defer cli.Close()

	workspace := resolveWorkspace()

	containerName := config.ContainerName(workspace)

	id, err := ctr.FindByLabel(ctx, cli, containerName)
	if err != nil {
		return fmt.Errorf("failed to find container: %w", err)
	}
	if id == "" {
		info("No sandbox container found for this workspace.")
		return nil
	}

	follow, _ := cmd.Flags().GetBool("follow")
	tail, _ := cmd.Flags().GetString("tail")

	reader, err := cli.ContainerLogs(ctx, id, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     follow,
		Tail:       tail,
	})
	if err != nil {
		return fmt.Errorf("failed to get logs: %w", err)
	}
	defer reader.Close()

	_, _ = stdcopy.StdCopy(os.Stdout, os.Stderr, reader)
	return nil
}
