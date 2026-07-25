package cmd

import (
	"context"
	"fmt"

	"github.com/docker/docker/client"
	"github.com/spf13/cobra"

	ctr "github.com/Devon-White/claude-bunker/internal/container"
	"github.com/Devon-White/claude-bunker/internal/sessions"
)

var sessionsAttachCmd = &cobra.Command{
	Use:   "attach <container-name>",
	Short: "Attach to a running sandbox",
	Long: `Opens an interactive claude session in a running container.
Accepts full or partial container names.

By default, resumes the most recent conversation (claude --continue).
Use --new to start a fresh session, or --resume to pick from recent sessions.`,
	Args: cobra.ExactArgs(1),
	RunE: runSessionsAttach,
}

func init() {
	sessionsAttachCmd.Flags().Bool("shell", false, "Open a bash shell instead of claude")
	sessionsAttachCmd.Flags().Bool("new", false, "Start a new session instead of continuing the last one")
	sessionsAttachCmd.Flags().Bool("resume", false, "Show recent sessions to pick from (claude --resume)")
	sessionsAttachCmd.Flags().Bool("keep", false, "Keep container running after session exits")
}

func runSessionsAttach(cmd *cobra.Command, args []string) error {
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

	if c.Status != "running" {
		return fmt.Errorf("container %s is %s (must be running to attach)", c.DisplayName, c.Status)
	}

	shell, _ := cmd.Flags().GetBool("shell")
	newSession, _ := cmd.Flags().GetBool("new")
	resume, _ := cmd.Flags().GetBool("resume")

	command := buildAttachCommand(shell, newSession, resume)

	// Use auth wrapper if available in the container.
	command = wrapWithAuth(ctx, cli, c.ID, command)

	// ExecInteractive needs the concrete Docker client, not our interface.
	dockerCli, ok := mgr.Client().(*client.Client)
	if !ok {
		return fmt.Errorf("attach requires a real Docker client")
	}

	exitCode, execID, err := ctr.ExecInteractive(ctx, dockerCli, c.ID, ctr.ContainerUser, command)
	if err != nil {
		return fmt.Errorf("exec: %w", err)
	}

	// Stop the container only if it's safe (no other active sessions) unless
	// --keep is set. Use --keep to prevent auto-stop.
	keep, _ := cmd.Flags().GetBool("keep")
	teardownAfterSession(ctx, dockerCli, c.ID, execID, keep, false)

	if exitCode != 0 {
		return fmt.Errorf("session exited with code %d", exitCode)
	}

	return nil
}

// buildAttachCommand constructs the command to exec in the container.
func buildAttachCommand(shell, newSession, resume bool) []string {
	if shell {
		return []string{"bash"}
	}
	if resume {
		return []string{"claude", "--resume"}
	}
	if newSession {
		return []string{"claude"}
	}
	// Default: continue the most recent conversation.
	return []string{"claude", "--continue"}
}

// wrapWithAuth prepends the auth wrapper script if it exists in the container.
// This ensures attached sessions have access to auth tokens on tmpfs.
func wrapWithAuth(ctx context.Context, cli *client.Client, containerID string, command []string) []string {
	if len(command) == 0 || command[0] == "bash" {
		return command // shell sessions don't need auth
	}
	// Check if the auth wrapper exists in the container.
	_, err := ctr.ExecNonInteractive(ctx, cli, containerID, ctr.ContainerUser,
		[]string{"test", "-f", ctr.AuthWrapperPath})
	if err != nil {
		return command // no wrapper, run directly
	}
	return append([]string{ctr.AuthWrapperPath}, command...)
}
