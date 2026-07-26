package cmd

import (
	"time"

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
	// --interval is scoped to the `sessions` TUI (the only NewWatcher caller);
	// registering it on root would make it a no-op there. The 3s literal mirrors
	// sessions.defaultPollInterval, which stays unexported.
	sessionsCmd.Flags().Duration("interval", 3*time.Second, "Session-watcher poll interval")

	sessionsCmd.AddCommand(sessionsListCmd)
	sessionsCmd.AddCommand(sessionsStopCmd)
	sessionsCmd.AddCommand(sessionsAttachCmd)
	sessionsCmd.AddCommand(sessionsLogsCmd)
}
