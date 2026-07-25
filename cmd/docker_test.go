package cmd

import "testing"

func TestDockerClient_WrapsUnavailableAsCode4(t *testing.T) {
	// Force a Docker-client failure by pointing DOCKER_HOST at a dead socket.
	t.Setenv("DOCKER_HOST", "unix:///nonexistent/docker.sock")
	_, err := dockerClient()
	if err == nil {
		t.Skip("Docker appears reachable in this environment; cannot exercise the failure path")
	}
	if ExitCodeFor(err) != ExitDockerUnavailable {
		t.Errorf("docker-unavailable error must map to exit %d, got %d", ExitDockerUnavailable, ExitCodeFor(err))
	}
}
