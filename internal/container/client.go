package container

import (
	"context"
	"fmt"
	"runtime"
	"strings"

	"github.com/docker/docker/client"
)

// NewClient creates a Docker client from environment settings and verifies
// the daemon is reachable.
func NewClient() (*client.Client, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("creating Docker client: %w\n%s", err, dockerInstallHint())
	}

	_, err = cli.Ping(context.Background())
	if err != nil {
		cli.Close()
		return nil, fmt.Errorf("%s", dockerErrorMessage(err))
	}

	return cli, nil
}

// dockerErrorMessage returns a user-friendly error message with actionable
// guidance based on the type of Docker connection failure.
func dockerErrorMessage(err error) string {
	msg := err.Error()
	lower := strings.ToLower(msg)

	switch {
	case strings.Contains(lower, "permission denied"):
		return fmt.Sprintf("Docker permission denied: %s\n\n"+
			"  Fix: Add your user to the docker group:\n"+
			"    sudo usermod -aG docker $USER\n"+
			"  Then log out and back in.", err)

	case strings.Contains(lower, "not found") || strings.Contains(lower, "no such file"):
		return fmt.Sprintf("Docker not found: %s\n\n"+
			"  %s", err, dockerInstallHint())

	default:
		return fmt.Sprintf("Cannot connect to Docker: %s\n\n"+
			"  %s", err, dockerStartHint())
	}
}

func dockerStartHint() string {
	if runtime.GOOS == "linux" {
		return "Start the Docker daemon:\n" +
			"    sudo systemctl start docker\n" +
			"  Or launch Docker Desktop if installed."
	}
	return "Start Docker Desktop and try again."
}

func dockerInstallHint() string {
	return "Install Docker: https://docs.docker.com/get-docker/"
}
