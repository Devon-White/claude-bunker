package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/spf13/cobra"

	ctr "github.com/Devon-White/claude-bunker/internal/container"
	"github.com/Devon-White/claude-bunker/internal/sessions"
)

var sessionsLogsCmd = &cobra.Command{
	Use:   "logs <container-name>",
	Short: "Stream container logs",
	Long:  "Streams logs from a claude-bunker container. Accepts full or partial container names.",
	Args:  cobra.ExactArgs(1),
	RunE:  runSessionsLogs,
}

func init() {
	sessionsLogsCmd.Flags().BoolP("follow", "f", false, "Follow log output")
	sessionsLogsCmd.Flags().String("tail", "100", "Number of lines to show from the end (or \"all\")")
}

func runSessionsLogs(cmd *cobra.Command, args []string) error {
	initVerbosity(cmd)
	ctx := context.Background()

	cli, err := ctr.NewClient()
	if err != nil {
		return fmt.Errorf("docker client: %w", err)
	}
	defer cli.Close()

	mgr := sessions.NewManager(cli)
	c, err := mgr.ResolveContainer(ctx, args[0])
	if err != nil {
		return err
	}

	follow, _ := cmd.Flags().GetBool("follow")
	tail, _ := cmd.Flags().GetString("tail")

	// ContainerLogs needs the concrete Docker client.
	dockerCli, ok := mgr.Client().(*client.Client)
	if !ok {
		return fmt.Errorf("logs requires a real Docker client")
	}

	reader, err := dockerCli.ContainerLogs(ctx, c.ID, container.LogsOptions{
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
