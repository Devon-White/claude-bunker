package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/Devon-White/claude-bunker/internal/sessions"
)

var sessionsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all sandbox sessions",
	Long:  "Lists all claude-bunker containers with their active sessions and subagents.",
	RunE:  runSessionsList,
}

func init() {
	sessionsListCmd.Flags().Bool("json", false, "Output as JSON (for scripting/swarm integration)")
}

func runSessionsList(cmd *cobra.Command, args []string) error {
	initVerbosity(cmd)
	ctx := context.Background()

	cli, err := dockerClient()
	if err != nil {
		return err
	}
	defer cli.Close()

	mgr := sessions.NewManager(cli)
	snap, err := mgr.FetchSnapshot(ctx)
	if err != nil {
		return fmt.Errorf("fetching sessions: %w", err)
	}

	jsonOut, _ := cmd.Flags().GetBool("json")
	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(snap)
	}

	if len(snap.Containers) == 0 {
		info("No claude-bunker containers found.")
		return nil
	}

	for i, c := range snap.Containers {
		if i > 0 {
			fmt.Println()
		}
		stStyle := stateStyle(c.Status)
		dot := stStyle.Render("●")
		if c.Status != "running" {
			dot = dimStyle.Render("○")
		}

		uptime := ""
		if c.Status == "running" && !c.StartedAt.IsZero() {
			uptime = fmt.Sprintf(" (%s)", sessions.FormatUptime(c.StartedAt))
		}

		nameLine := boldStyle.Render(c.DisplayName)
		originalName := sessions.DisplayName(c.Name)
		if c.DisplayName != originalName {
			nameLine += dimStyle.Render(fmt.Sprintf(" (%s)", originalName))
		}
		fmt.Printf("  %s %s %s%s\n", dot, nameLine, stStyle.Render(c.Status), dimStyle.Render(uptime))

		for j, s := range c.Sessions {
			connector := "├─"
			if j == len(c.Sessions)-1 {
				connector = "└─"
			}
			label := s.Command
			if s.Title != "" {
				label = boldStyle.Render(s.Title) + dimStyle.Render(fmt.Sprintf(" (%s)", s.Command))
			}
			fmt.Printf("    %s %s [pid %s]\n", dimStyle.Render(connector), label, s.PID)

			for k, a := range s.Subagents {
				prefix := "│"
				if j == len(c.Sessions)-1 {
					prefix = " "
				}
				subConn := "├─"
				if k == len(s.Subagents)-1 {
					subConn = "└─"
				}
				fmt.Printf("    %s   %s %s [pid %s]\n", dimStyle.Render(prefix), dimStyle.Render(subConn), a.Name, a.PID)
			}
		}

		if c.Status == "running" && len(c.Sessions) == 0 {
			fmt.Printf("    %s\n", dimStyle.Render("no active sessions"))
		}
	}

	// Show container name for reference.
	fmt.Println()
	fmt.Printf("  %s\n", dimStyle.Render(fmt.Sprintf("%d container(s)", len(snap.Containers))))

	return nil
}

