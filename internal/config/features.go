package config

import (
	"fmt"
	"strings"
)

// ResolveFeatureName validates that a feature name is a full OCI reference.
// Full references must contain at least one "/" (e.g.
// "ghcr.io/devcontainers/features/python:1").
func ResolveFeatureName(name string) (string, error) {
	if !strings.Contains(name, "/") {
		return "", fmt.Errorf(
			"feature %q is not a valid OCI reference — use the full reference "+
				"(e.g. \"ghcr.io/devcontainers/features/python:1\"), "+
				"or use the apt-packages feature "+
				"(\"ghcr.io/rocker-org/devcontainer-features/apt-packages:1\") for plain apt packages",
			name,
		)
	}
	return name, nil
}
