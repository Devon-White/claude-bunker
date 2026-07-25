package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/docker/docker/api/types"
	"github.com/spf13/cobra"

	"github.com/Devon-White/claude-bunker/internal/devcontainer"
)

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check that the environment is ready to run claude-bunker",
	Long:  "Verifies Docker is reachable, reports versions, and checks for a project devcontainer. Exits non-zero if the environment is not ready.",
	RunE:  runDoctor,
}

// DockerPinger is the minimal Docker surface doctor needs (satisfied by *client.Client).
type DockerPinger interface {
	Ping(ctx context.Context) (types.Ping, error)
	ServerVersion(ctx context.Context) (types.Version, error)
}

type checkResult struct {
	Name   string
	Detail string
	OK     bool
}

func runDoctor(cmd *cobra.Command, args []string) error {
	initVerbosity(cmd)
	ctx := context.Background()

	cli, err := dockerClient()
	if err != nil {
		// Report the Docker failure as a doctor result, then exit 4.
		printCheck(checkResult{Name: "Docker", Detail: err.Error(), OK: false})
		return err
	}
	defer cli.Close()

	results := runDoctorChecks(ctx, cli, Version, resolveWorkspace())
	for _, r := range results {
		printCheck(r)
	}
	if !doctorAllOK(results) {
		return Coded(ExitError, fmt.Errorf("one or more checks failed"))
	}
	return nil
}

// runDoctorChecks gathers environment checks over injected dependencies (pure).
func runDoctorChecks(ctx context.Context, docker DockerPinger, version, workspace string) []checkResult {
	var out []checkResult

	if _, err := docker.Ping(ctx); err != nil {
		out = append(out, checkResult{Name: "Docker", Detail: "daemon not reachable: " + err.Error(), OK: false})
	} else {
		detail := "reachable"
		if v, err := docker.ServerVersion(ctx); err == nil {
			detail = "reachable (server " + v.Version + ")"
		}
		out = append(out, checkResult{Name: "Docker", Detail: detail, OK: true})
	}

	out = append(out, checkResult{Name: "claude-bunker", Detail: version, OK: true})

	if _, err := os.Stat(devcontainer.DevContainerPath(workspace)); err == nil {
		out = append(out, checkResult{Name: "devcontainer.json", Detail: "present", OK: true})
	} else {
		out = append(out, checkResult{Name: "devcontainer.json", Detail: "not found — run `claude-bunker init`", OK: true}) // informational, not a failure
	}
	return out
}

func doctorAllOK(results []checkResult) bool {
	for _, r := range results {
		if !r.OK {
			return false
		}
	}
	return true
}

// printCheck writes one styled check line to stdout (payload).
func printCheck(r checkResult) {
	mark := successMsgStyle.Render("✓")
	if !r.OK {
		mark = errorLabelStyle.Render("✗")
	}
	fmt.Printf("%s %s: %s\n", mark, r.Name, r.Detail)
}
