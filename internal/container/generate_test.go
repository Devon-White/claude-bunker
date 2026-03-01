package container

import (
	"strings"
	"testing"
)

func TestGenerateDockerfile_NoLayers(t *testing.T) {
	base := "FROM debian:bookworm-slim\nRUN echo hello"
	got, err := GenerateDockerfile(base, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should still end with USER line
	if !strings.HasSuffix(strings.TrimSpace(got), "USER "+ContainerUser) {
		t.Errorf("expected trailing USER %s, got:\n%s", ContainerUser, got)
	}
	// Should NOT have the generated layers header
	if strings.Contains(got, "generated layers") {
		t.Errorf("should not have generated layers header with no layers")
	}
}

func TestGenerateDockerfile_AptPackages(t *testing.T) {
	base := "FROM debian:bookworm-slim"
	got, err := GenerateDockerfile(base, nil, []string{"vim", "curl", "git"}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(got, "# Apt packages") {
		t.Error("missing apt packages header")
	}
	if !strings.Contains(got, "apt-get install") {
		t.Error("missing apt-get install command")
	}
	// Packages should be sorted
	curlIdx := strings.Index(got, "curl")
	gitIdx := strings.Index(got, "git")
	vimIdx := strings.Index(got, "vim")
	if curlIdx > gitIdx || gitIdx > vimIdx {
		t.Error("apt packages should be sorted alphabetically")
	}
}

func TestGenerateDockerfile_UserEnv(t *testing.T) {
	base := "FROM debian:bookworm-slim"
	env := map[string]string{
		"FOO": "bar",
		"BAZ": "qux",
	}
	got, err := GenerateDockerfile(base, nil, nil, env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(got, "# User environment variables") {
		t.Error("missing user env header")
	}
	if !strings.Contains(got, `ENV BAZ="qux"`) {
		t.Error("missing BAZ env var")
	}
	if !strings.Contains(got, `ENV FOO="bar"`) {
		t.Error("missing FOO env var")
	}
	// Env vars should be sorted: BAZ before FOO
	bazIdx := strings.Index(got, "BAZ")
	fooIdx := strings.Index(got, "FOO")
	if bazIdx > fooIdx {
		t.Error("env vars should be sorted alphabetically")
	}
}

func TestGenerateDockerfile_Features(t *testing.T) {
	base := "FROM debian:bookworm-slim"
	features := []ResolvedFeature{
		{
			ID:     "python",
			Source: "ghcr.io/devcontainers/features/python:1",
			Options: map[string]interface{}{
				"version": "3.12",
			},
			Env: map[string]string{
				"PATH": "/usr/local/python/current/bin:${PATH}",
			},
		},
	}
	got, err := GenerateDockerfile(base, features, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(got, "# Feature: python") {
		t.Error("missing feature header")
	}
	if !strings.Contains(got, "COPY _features/python/") {
		t.Error("missing feature COPY instruction")
	}
	if !strings.Contains(got, "devcontainer-features-install.sh") {
		t.Error("missing wrapper script execution")
	}
	// Options should NOT appear as Dockerfile ENV instructions.
	// They go in devcontainer-features.env (sourced by the wrapper at build time).
	if strings.Contains(got, `ENV VERSION=`) || strings.Contains(got, `ENV PYTHON_VERSION=`) {
		t.Error("feature options should not be Dockerfile ENV instructions")
	}
}

func TestGenerateDockerfile_ContainerEnvBeforeInstall(t *testing.T) {
	base := "FROM debian:bookworm-slim"
	features := []ResolvedFeature{
		{
			ID:     "go",
			Source: "ghcr.io/devcontainers/features/go:1",
			Env: map[string]string{
				"GOROOT": "/usr/local/go",
				"GOPATH": "/go",
				"PATH":   "/usr/local/go/bin:/go/bin:${PATH}",
			},
		},
	}
	got, err := GenerateDockerfile(base, features, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// containerEnv should use plain ${PATH} (Docker-native expansion)
	if !strings.Contains(got, `GOROOT="/usr/local/go"`) {
		t.Error("missing GOROOT env var")
	}
	if !strings.Contains(got, `PATH="/usr/local/go/bin:/go/bin:${PATH}"`) {
		t.Error("missing PATH env var with Docker-native ${PATH} expansion")
	}

	// containerEnv must appear BEFORE the RUN install line
	envIdx := strings.Index(got, `ENV PATH=`)
	runIdx := strings.Index(got, "devcontainer-features-install.sh")
	if envIdx < 0 || runIdx < 0 {
		t.Fatal("missing ENV or RUN instruction")
	}
	if envIdx > runIdx {
		t.Error("containerEnv must be emitted BEFORE the install RUN layer")
	}
}

func TestGenerateDockerfile_EndsWithUser(t *testing.T) {
	base := "FROM debian:bookworm-slim"
	got, err := GenerateDockerfile(base, nil, []string{"vim"}, map[string]string{"A": "1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	trimmed := strings.TrimSpace(got)
	if !strings.HasSuffix(trimmed, "USER "+ContainerUser) {
		t.Errorf("Dockerfile should end with USER %s, got:\n%s", ContainerUser, got)
	}
}
