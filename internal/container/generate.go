package container

import (
	"fmt"
	"sort"
	"strings"
)

// GenerateDockerfile appends apt packages, feature install layers, and user
// env vars to the base Dockerfile. The generated Dockerfile always ends with
// USER claude-bunker to ensure the container runs as the unprivileged user.
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
		b.WriteString(fmt.Sprintf("\n# Feature: %s (%s)\n", f.ID, f.Source))

		// Set feature options as ENV vars (per devcontainer feature spec)
		if len(f.Options) > 0 {
			keys := sortedKeys(f.Options)
			for _, k := range keys {
				v := fmt.Sprintf("%v", f.Options[k])
				envName := strings.NewReplacer("-", "_").Replace(strings.ToUpper(f.ID)) + "_" + strings.ToUpper(k)
				b.WriteString(fmt.Sprintf("ENV %s=%q\n", envName, v))
			}
		}

		// Copy feature files and run install.sh as root
		b.WriteString(fmt.Sprintf("COPY _features/%s/ /tmp/features/%s/\n", f.ID, f.ID))
		b.WriteString("USER root\n")
		b.WriteString(fmt.Sprintf("RUN chmod +x /tmp/features/%s/install.sh && /tmp/features/%s/install.sh && rm -rf /tmp/features/%s\n",
			f.ID, f.ID, f.ID))
	}

	// Append user-defined env vars
	if len(userEnv) > 0 {
		b.WriteString("\n# User environment variables\n")
		keys := make([]string, 0, len(userEnv))
		for k := range userEnv {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			b.WriteString(fmt.Sprintf("ENV %s=%q\n", k, userEnv[k]))
		}
	}

	// Always end with the unprivileged user
	b.WriteString(fmt.Sprintf("\nUSER %s\n", ContainerUser))

	return b.String(), nil
}

// sortedKeys returns the keys of a map sorted alphabetically.
func sortedKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
