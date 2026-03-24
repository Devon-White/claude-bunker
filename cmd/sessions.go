package cmd

import (
	"github.com/spf13/cobra"
)

var sessionsCmd = &cobra.Command{
	Use:   "sessions",
	Short: "Manage sandbox sessions",
	Long: `Interactive session manager for all claude-bunker containers.

Without a subcommand, launches an interactive TUI showing all running
sandboxes, their sessions, and subagents. Use subcommands for scripting
and automation (e.g., swarm orchestration).`,
	RunE: runSessionsTUI,
}

func init() {
	sessionsCmd.AddCommand(sessionsListCmd)
	sessionsCmd.AddCommand(sessionsStopCmd)
	sessionsCmd.AddCommand(sessionsAttachCmd)
	sessionsCmd.AddCommand(sessionsLogsCmd)
}
