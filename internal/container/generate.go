package container

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// validAptPkg matches valid Debian package names: alphanumeric, plus, minus,
// dots, and colons (for arch-qualified names like libc6:amd64). Rejects
// shell metacharacters to prevent command injection via config.json apt field.
var validAptPkg = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.+\-:]+$`)

// validEnvKey matches valid environment variable names per POSIX: starts with
// letter or underscore, followed by letters, digits, or underscores.
var validEnvKey = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// validFeatureID matches safe feature IDs: alphanumeric, hyphens, underscores.
var validFeatureID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_\-]*$`)

// GenerateDockerfile appends apt packages, feature install layers, and user
// env vars to the base Dockerfile. The generated Dockerfile always ends with
// USER claude-bunker to ensure the container runs as the unprivileged user.
//
// Feature layers follow the devcontainer spec: containerEnv is emitted as ENV
// instructions BEFORE the install script runs (so PATH is available during
// installation), and options are passed via a sourced env file rather than
// persisted as image ENV vars.
func GenerateDockerfile(baseDockerfile string, features []ResolvedFeature, aptPackages []string, userEnv map[string]string) (string, error) {
	var b strings.Builder
	b.WriteString(baseDockerfile)

	hasLayers := len(features) > 0 || len(aptPackages) > 0 || len(userEnv) > 0
	if hasLayers {
		b.WriteString("\n\n# --- claude-bunker: generated layers ---\n")
	}

	// Apt packages layer (runs before features so features can depend on them)
	if len(aptPackages) > 0 {
		sorted := make([]string, len(aptPackages))
		copy(sorted, aptPackages)
		sort.Strings(sorted)
		for _, pkg := range sorted {
			if !validAptPkg.MatchString(pkg) {
				return "", fmt.Errorf("invalid apt package name %q: must match %s", pkg, validAptPkg.String())
			}
		}
		b.WriteString("\n# Apt packages\n")
		b.WriteString("USER root\n")
		b.WriteString("RUN apt-get update && apt-get install -y --no-install-recommends \\\n")
		for _, pkg := range sorted {
			b.WriteString(fmt.Sprintf("  %s \\\n", pkg))
		}
		b.WriteString("  && apt-get clean && rm -rf /var/lib/apt/lists/*\n")
	}

	// Append feature install layers
	for _, f := range features {
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

	// Append user-defined env vars
	if len(userEnv) > 0 {
		b.WriteString("\n# User environment variables\n")
		keys := make([]string, 0, len(userEnv))
		for k := range userEnv {
			if !validEnvKey.MatchString(k) {
				return "", fmt.Errorf("invalid env var key %q: must match %s", k, validEnvKey.String())
			}
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			b.WriteString(fmt.Sprintf("ENV %s=%q\n", k, userEnv[k]))
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
