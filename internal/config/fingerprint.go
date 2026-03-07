package config

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// BuildInput bundles the four values that flow through every fingerprint function.
type BuildInput struct {
	Version    string
	Dockerfile string
	Scripts    map[string][]byte
	ProjectCfg ProjectConfig
}

// ImageFingerprint computes a SHA-256 hash of all inputs that affect the Docker
// image build: the claude-bunker version, the generated Dockerfile, embedded
// scripts, apt packages, features config, and user env vars baked into the image.
func ImageFingerprint(b BuildInput) string {
	h := sha256.New()

	// Hash the claude-bunker version so upgrades invalidate the cache
	h.Write([]byte("version:"))
	h.Write([]byte(b.Version))

	// Hash the generated Dockerfile
	h.Write([]byte("dockerfile:"))
	h.Write([]byte(b.Dockerfile))

	// Hash embedded scripts in deterministic order
	scriptNames := make([]string, 0, len(b.Scripts))
	for name := range b.Scripts {
		scriptNames = append(scriptNames, name)
	}
	sort.Strings(scriptNames)
	for _, name := range scriptNames {
		h.Write([]byte("script:" + name + ":"))
		h.Write(b.Scripts[name])
	}

	// Hash apt packages (sorted, since they affect the image)
	if len(b.ProjectCfg.Apt) > 0 {
		sorted := make([]string, len(b.ProjectCfg.Apt))
		copy(sorted, b.ProjectCfg.Apt)
		sort.Strings(sorted)
		h.Write([]byte("apt:"))
		h.Write([]byte(strings.Join(sorted, ",")))
	}

	// Hash features config (affects image layers)
	if len(b.ProjectCfg.Features) > 0 {
		h.Write([]byte("features:"))
		featureNames := make([]string, 0, len(b.ProjectCfg.Features))
		for name := range b.ProjectCfg.Features {
			featureNames = append(featureNames, name)
		}
		sort.Strings(featureNames)
		for _, name := range featureNames {
			h.Write([]byte(name + ":"))
			opts := b.ProjectCfg.Features[name]
			optKeys := make([]string, 0, len(opts))
			for k := range opts {
				optKeys = append(optKeys, k)
			}
			sort.Strings(optKeys)
			for _, k := range optKeys {
				h.Write([]byte(fmt.Sprintf("%s=%v,", k, opts[k])))
			}
		}
	}

	// Hash user env vars that are baked into the image via GenerateDockerfile
	if len(b.ProjectCfg.Env) > 0 {
		h.Write([]byte("env:"))
		envKeys := make([]string, 0, len(b.ProjectCfg.Env))
		for k := range b.ProjectCfg.Env {
			envKeys = append(envKeys, k)
		}
		sort.Strings(envKeys)
		for _, k := range envKeys {
			h.Write([]byte(k + "=" + b.ProjectCfg.Env[k] + ","))
		}
	}

	return fmt.Sprintf("%x", h.Sum(nil))
}

// ContainerFingerprint computes a SHA-256 hash of inputs that affect container
// creation but NOT the Docker image: allowDomains, workspace path, exclude
// paths, and postStartCommand.
//
// Changes to container-only inputs require container recreation but not an
// image rebuild, saving significant time.
func ContainerFingerprint(projectCfg ProjectConfig) string {
	h := sha256.New()

	// AllowDomains affect firewall setup at container start
	if len(projectCfg.AllowDomains) > 0 {
		sorted := make([]string, len(projectCfg.AllowDomains))
		copy(sorted, projectCfg.AllowDomains)
		sort.Strings(sorted)
		h.Write([]byte("domains:"))
		h.Write([]byte(strings.Join(sorted, ",")))
	}

	// Workspace subpath affects container workdir
	if projectCfg.Workspace != "" {
		h.Write([]byte("workspace:" + projectCfg.Workspace))
	}

	// Exclude paths affect tmpfs mounts
	if len(projectCfg.Exclude) > 0 {
		sorted := make([]string, len(projectCfg.Exclude))
		copy(sorted, projectCfg.Exclude)
		sort.Strings(sorted)
		h.Write([]byte("exclude:"))
		h.Write([]byte(strings.Join(sorted, ",")))
	}

	// PostStartCommand runs at container start
	if projectCfg.PostStartCommand != "" {
		h.Write([]byte("poststart:" + projectCfg.PostStartCommand))
	}

	// Plugins level affects which configs/caches are seeded
	if projectCfg.Plugins != "" {
		h.Write([]byte("plugins:" + projectCfg.Plugins))
	}

	return fmt.Sprintf("%x", h.Sum(nil))
}

// CombinedFingerprint produces a single fingerprint string that encodes both
// image and container components, separated by ":". This is stored in the
// cache file for comparison.
func CombinedFingerprint(b BuildInput) string {
	imgFP := ImageFingerprint(b)
	ctrFP := ContainerFingerprint(b.ProjectCfg)
	return imgFP + ":" + ctrFP
}

// FingerprintResult holds the result of comparing current vs cached fingerprints.
type FingerprintResult struct {
	ImageMatch     bool // true if the image fingerprint matches (no rebuild needed)
	ContainerMatch bool // true if the container fingerprint matches (no recreate needed)
}

// CompareFingerprints computes current fingerprints and compares them against
// the cached fingerprint for the given container name. Returns which components
// match.
func CompareFingerprints(b BuildInput, containerName string) FingerprintResult {
	currentImg := ImageFingerprint(b)
	currentCtr := ContainerFingerprint(b.ProjectCfg)

	cached, err := LoadFingerprint(containerName)
	if err != nil {
		return FingerprintResult{ImageMatch: false, ContainerMatch: false}
	}

	parts := strings.SplitN(cached, ":", 2)
	if len(parts) != 2 {
		return FingerprintResult{ImageMatch: false, ContainerMatch: false}
	}

	return FingerprintResult{
		ImageMatch:     currentImg == parts[0],
		ContainerMatch: currentCtr == parts[1],
	}
}

// SaveCombinedFingerprint computes and saves the combined fingerprint.
func SaveCombinedFingerprint(b BuildInput, containerName string) error {
	fp := CombinedFingerprint(b)
	return SaveFingerprint(containerName, fp)
}

// CacheDir returns the fingerprint cache directory.
func CacheDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".cache", "claude-bunker")
	return dir, nil
}

// FingerprintPath returns the path to the cached fingerprint for a container.
func FingerprintPath(containerName string) (string, error) {
	dir, err := CacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, containerName+".fp"), nil
}

// LoadFingerprint reads the cached fingerprint for a container.
func LoadFingerprint(containerName string) (string, error) {
	p, err := FingerprintPath(containerName)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// SaveFingerprint writes the fingerprint to cache.
func SaveFingerprint(containerName, fingerprint string) error {
	p, err := FingerprintPath(containerName)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(fingerprint), 0600)
}

// ClearFingerprint deletes the cached fingerprint for a container.
func ClearFingerprint(containerName string) error {
	p, err := FingerprintPath(containerName)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// EffectiveWorkdir returns the container working directory based on project config.
// Returns an error if the resolved path escapes /workspace/.
func EffectiveWorkdir(cfg ProjectConfig) (string, error) {
	if cfg.Workspace != "" && cfg.Workspace != "." {
		sub := strings.TrimPrefix(cfg.Workspace, "./")
		result := path.Clean("/workspace/" + sub)
		if result != "/workspace" && !strings.HasPrefix(result, "/workspace/") {
			return "", fmt.Errorf("workspace path %q resolves to %q, which is outside /workspace/", cfg.Workspace, result)
		}
		return result, nil
	}
	return "/workspace", nil
}

// ExtraDomains returns a copy of the project's extra allowed domains.
func ExtraDomains(cfg ProjectConfig) []string {
	out := make([]string, len(cfg.AllowDomains))
	copy(out, cfg.AllowDomains)
	return out
}
