package container

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// validEnvKey matches valid environment variable names per POSIX: starts with
// letter or underscore, followed by letters, digits, or underscores.
var validEnvKey = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// validFeatureID matches safe feature IDs: alphanumeric, hyphens, underscores.
var validFeatureID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_\-]*$`)

// DockerfileOpts holds all inputs for generating a Dockerfile with project layers.
type DockerfileOpts struct {
	BaseDockerfile  string
	Features        []ResolvedFeature
	UserEnv         map[string]string
	OnCreateCommand string
}

// GenerateDockerfile appends feature install layers and user env vars to the
// base Dockerfile. The generated Dockerfile always ends with
// USER claude-bunker to ensure the container runs as the unprivileged user.
//
// Feature layers follow the devcontainer spec: containerEnv is emitted as ENV
// instructions BEFORE the install script runs (so PATH is available during
// installation), and options are passed via a sourced env file rather than
// persisted as image ENV vars.
func GenerateDockerfile(opts DockerfileOpts) (string, error) {
	var b strings.Builder
	b.WriteString(opts.BaseDockerfile)

	hasLayers := len(opts.Features) > 0 || len(opts.UserEnv) > 0 || opts.OnCreateCommand != ""
	if hasLayers {
		b.WriteString("\n\n# --- claude-bunker: generated layers ---\n")
	}

	// Append feature install layers
	for _, f := range opts.Features {
		if !validFeatureID.MatchString(f.ID) {
			return "", fmt.Errorf("invalid feature ID %q: must match %s", f.ID, validFeatureID.String())
		}

		b.WriteString(fmt.Sprintf("\n# Feature: %s (%s)\n", f.ID, f.Source))

		// containerEnv BEFORE install — per the devcontainer spec, these ENV
		// instructions (especially PATH) must be available during install.sh.
		// Features use plain ${PATH} which Docker expands natively.
		if len(f.Env) > 0 {
			envKeys := sortedStringMapKeys(f.Env)
			for _, k := range envKeys {
				if !validEnvKey.MatchString(k) {
					return "", fmt.Errorf("invalid feature env var key %q in feature %s", k, f.ID)
				}
				b.WriteString(fmt.Sprintf("ENV %s=%q\n", k, f.Env[k]))
			}
		}

		// Copy feature files (includes devcontainer-features.env with options
		// and devcontainer-features-install.sh wrapper) and run as root.
		b.WriteString(fmt.Sprintf("COPY _features/%s/ /tmp/dev-container-features/%s/\n", f.ID, f.ID))
		b.WriteString("USER root\n")
		b.WriteString(fmt.Sprintf("RUN cd /tmp/dev-container-features/%s && chmod +x ./devcontainer-features-install.sh && ./devcontainer-features-install.sh && rm -rf /tmp/dev-container-features/%s\n",
			f.ID, f.ID))
	}

	// onCreateCommand: arbitrary shell command baked into the image.
	//
	// TRUST BOUNDARY: onCreateCommand runs during `docker build` with UNRESTRICTED
	// network access — the iptables firewall is only configured at container runtime,
	// not build time. A malicious .devcontainer/devcontainer.json could exfiltrate data
	// during build. Users should review .devcontainer/devcontainer.json before running
	// claude-bunker on untrusted repos.
	// This matches the devcontainer trust model (VS Code has the same issue).
	if opts.OnCreateCommand != "" {
		b.WriteString("\n# onCreateCommand\n")
		b.WriteString("USER root\n")
		b.WriteString(fmt.Sprintf("RUN %s\n", opts.OnCreateCommand))
	}

	// Append user-defined env vars
	if len(opts.UserEnv) > 0 {
		b.WriteString("\n# User environment variables\n")
		keys := make([]string, 0, len(opts.UserEnv))
		for k := range opts.UserEnv {
			if !validEnvKey.MatchString(k) {
				return "", fmt.Errorf("invalid env var key %q: must match %s", k, validEnvKey.String())
			}
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			b.WriteString(fmt.Sprintf("ENV %s=%q\n", k, opts.UserEnv[k]))
		}
	}

	// Restore working directory and end with the unprivileged user
	b.WriteString("\nWORKDIR " + ContainerWorkspace + "\n")
	b.WriteString(fmt.Sprintf("USER %s\n", ContainerUser))

	return b.String(), nil
}

// sortedStringMapKeys returns the keys of a string map sorted alphabetically.
func sortedStringMapKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
