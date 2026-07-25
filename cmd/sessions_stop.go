package cmd

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Devon-White/claude-bunker/internal/sessions"
)

var sessionsStopCmd = &cobra.Command{
	Use:   "stop <container-name>",
	Short: "Stop a sandbox container",
	Long:  "Stops a running claude-bunker container. Accepts full or partial container names.",
	Args:  cobra.ExactArgs(1),
	RunE:  runSessionsStop,
}

func init() {
	sessionsStopCmd.Flags().BoolP("force", "f", false, "Skip confirmation prompt")
	sessionsStopCmd.Flags().Bool("remove", false, "Also remove the container after stopping")
}

func runSessionsStop(cmd *cobra.Command, args []string) error {
	initVerbosity(cmd)
	ctx := context.Background()

	cli, err := dockerClient()
	if err != nil {
		return err
	}
	defer cli.Close()

	mgr := sessions.NewManager(cli)
	c, err := mgr.ResolveContainer(ctx, args[0])
	if err != nil {
		return err
	}

	if c.Status != "running" {
		info(fmt.Sprintf("Container %s is already %s.", c.DisplayName, c.Status))
		return nil
	}

	// Warn about active sessions.
	if len(c.Sessions) > 0 {
		warn(fmt.Sprintf("Container %s has %d active session(s).", c.DisplayName, len(c.Sessions)))
	}

	force, _ := cmd.Flags().GetBool("force")
	if !force {
		ok, err := confirmAction(fmt.Sprintf("Stop container %s?", c.DisplayName))
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
	}

	if err := mgr.StopContainer(ctx, c.ID); err != nil {
		return fmt.Errorf("stopping container: %w", err)
	}
	success(fmt.Sprintf("Stopped %s", c.DisplayName))

	remove, _ := cmd.Flags().GetBool("remove")
	if remove {
		if err := mgr.RemoveContainer(ctx, c.ID); err != nil {
			return fmt.Errorf("removing container: %w", err)
		}
		success(fmt.Sprintf("Removed %s", c.DisplayName))
	}

	return nil
}
