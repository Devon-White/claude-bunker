package cmd

import (
	"github.com/docker/docker/client"

	"github.com/Devon-White/claude-bunker/internal/container"
)

// dockerClient creates the Docker API client, tagging a connection/daemon
// failure with the Docker-unavailable exit code so callers (via main's
// ExitCodeFor) exit 4 instead of a generic 1.
func dockerClient() (*client.Client, error) {
	cli, err := container.NewClient()
	if err != nil {
		return nil, Coded(ExitDockerUnavailable, err)
	}
	return cli, nil
}
