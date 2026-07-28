package config

import (
	"crypto/sha256"
	"fmt"
	"hash"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// BuildInput bundles the values that flow through every fingerprint function.
type BuildInput struct {
	Version        string
	Dockerfile     string
	Scripts        map[string][]byte
	ProxySources   map[string][]byte // egress-proxy Go source compiled into the image (name → content, from container.EgressProxySources())
	ProjectCfg     ProjectConfig
	FeatureDigests map[string]string // feature ref → resolved sha256 digest (from the committed lock)

	// MaskActive records whether credential masking (container.ShouldMask) is
	// active for this run. It affects container runtime setup (terminate vs
	// splice-only proxy mode) so it must bust the container fingerprint, but
	// it is a derived boolean only — auth/secrets themselves are deliberately
	// excluded from fingerprints (tokens change without requiring a rebuild).
	MaskActive bool
}

// imageFingerprint computes a SHA-256 hash of all inputs that affect the Docker
// image build: the claude-bunker version, the generated Dockerfile, embedded
// scripts, features config, and user env vars baked into the image.
func imageFingerprint(b BuildInput) string {
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

	// Hash egress-proxy source files in deterministic order. The multi-stage
	// build compiles internal/container/egressproxy/ INTO the image, so a
	// change to the proxy source must bust the image cache just like the
	// embedded scripts above.
	proxyNames := make([]string, 0, len(b.ProxySources))
	for name := range b.ProxySources {
		proxyNames = append(proxyNames, name)
	}
	sort.Strings(proxyNames)
	for _, name := range proxyNames {
		h.Write([]byte("proxysrc:" + name + ":"))
		h.Write(b.ProxySources[name])
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
				fmt.Fprintf(h, "%s=%v,", k, opts[k])
			}
		}
	}

	// Hash resolved feature digests from the committed lockfile so an upstream
	// feature change under the same tag invalidates the image cache.
	if len(b.FeatureDigests) > 0 {
		h.Write([]byte("featuredigests:"))
		refs := make([]string, 0, len(b.FeatureDigests))
		for ref := range b.FeatureDigests {
			refs = append(refs, ref)
		}
		sort.Strings(refs)
		for _, ref := range refs {
			h.Write([]byte(ref + "@" + b.FeatureDigests[ref] + ","))
		}
	}

	// Hash onCreateCommand (baked into image as a RUN layer)
	if b.ProjectCfg.OnCreateCommand != "" {
		h.Write([]byte("oncreate:"))
		h.Write([]byte(b.ProjectCfg.OnCreateCommand))
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

// containerFingerprint computes a SHA-256 hash of inputs that affect container
// creation but NOT the Docker image: allowDomains, workspace path, exclude
// paths, postStartCommand, and whether credential masking is active.
//
// Changes to container-only inputs require container recreation but not an
// image rebuild, saving significant time.
//
// maskActive is deliberately NOT derived from ProjectConfig: it comes from the
// caller (the run flow, which holds auth + proxy info) via container.ShouldMask.
// Only this boolean — never raw auth/secret values — enters the fingerprint,
// preserving the deliberate exclusion of secrets from fingerprinting while
// still forcing a recreate when the mask decision flips.
func containerFingerprint(projectCfg ProjectConfig, maskActive bool) string {
	h := sha256.New()

	// AllowDomains affect firewall setup at container start
	hashSortedSlice(h, "domains:", projectCfg.AllowDomains)

	// Workspace subpath affects container workdir
	if projectCfg.Workspace != "" {
		h.Write([]byte("workspace:" + projectCfg.Workspace))
	}

	// Exclude paths affect tmpfs mounts
	hashSortedSlice(h, "exclude:", projectCfg.Exclude)

	// PostStartCommand runs at container start
	if projectCfg.PostStartCommand != "" {
		h.Write([]byte("poststart:" + projectCfg.PostStartCommand))
	}

	// Plugins level affects which configs/caches are seeded
	if projectCfg.Plugins != "" {
		h.Write([]byte("plugins:" + projectCfg.Plugins))
	}

	// SeedHistory controls whether session history is copied into the container
	if projectCfg.SeedHistory != nil {
		if *projectCfg.SeedHistory {
			h.Write([]byte("seedhistory:true"))
		} else {
			h.Write([]byte("seedhistory:false"))
		}
	}

	// Masking toggles runtime proxy behavior (terminate-and-swap vs
	// splice-only). A container created WITHOUT masking must not be silently
	// reused as if masking were active, and vice versa.
	if maskActive {
		h.Write([]byte("masking:on"))
	}

	return fmt.Sprintf("%x", h.Sum(nil))
}

// FingerprintResult holds the result of comparing current vs cached fingerprints.
// The computed hashes are retained so callers can save them without recomputation.
type FingerprintResult struct {
	ImageMatch     bool // true if the image fingerprint matches (no rebuild needed)
	ContainerMatch bool // true if the container fingerprint matches (no recreate needed)
	combinedHash   string
}

// Save persists the computed fingerprint to the cache file for the given container.
// This avoids recomputing the hashes that were already calculated during comparison.
func (r FingerprintResult) Save(containerName string) error {
	return saveFingerprint(containerName, r.combinedHash)
}

// CompareFingerprints computes current fingerprints and compares them against
// the cached fingerprint for the given container name. Returns which components
// match, and retains the computed hashes for later saving via Save().
func CompareFingerprints(b BuildInput, containerName string) FingerprintResult {
	currentImg := imageFingerprint(b)
	currentCtr := containerFingerprint(b.ProjectCfg, b.MaskActive)
	combined := currentImg + ":" + currentCtr

	cached, err := loadFingerprint(containerName)
	if err != nil {
		return FingerprintResult{ImageMatch: false, ContainerMatch: false, combinedHash: combined}
	}

	parts := strings.SplitN(cached, ":", 2)
	if len(parts) != 2 {
		return FingerprintResult{ImageMatch: false, ContainerMatch: false, combinedHash: combined}
	}

	return FingerprintResult{
		ImageMatch:     currentImg == parts[0],
		ContainerMatch: currentCtr == parts[1],
		combinedHash:   combined,
	}
}

// ClearFingerprint deletes the cached fingerprint for a container.
func ClearFingerprint(containerName string) error {
	p, err := fingerprintPath(containerName)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// CacheDir returns the claude-bunker cache directory, where per-container
// fingerprints (<containerName>.fp) and build locks (<containerName>.build.lock)
// live. It honors CLAUDE_BUNKER_CACHE_DIR (used for test isolation and custom
// setups) and otherwise falls back to <UserHomeDir>/.cache/claude-bunker.
func CacheDir() (string, error) {
	if d := os.Getenv("CLAUDE_BUNKER_CACHE_DIR"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cache", "claude-bunker"), nil
}

// fingerprintPath returns the path to the cached fingerprint for a container.
func fingerprintPath(containerName string) (string, error) {
	dir, err := CacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, containerName+".fp"), nil
}

// loadFingerprint reads the cached fingerprint for a container.
func loadFingerprint(containerName string) (string, error) {
	p, err := fingerprintPath(containerName)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// saveFingerprint writes the fingerprint to cache.
func saveFingerprint(containerName, fingerprint string) error {
	p, err := fingerprintPath(containerName)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(fingerprint), 0600)
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

// hashSortedSlice writes a sorted copy of items to h with the given prefix.
// No-op when items is empty.
func hashSortedSlice(h hash.Hash, prefix string, items []string) {
	if len(items) == 0 {
		return
	}
	sorted := make([]string, len(items))
	copy(sorted, items)
	sort.Strings(sorted)
	h.Write([]byte(prefix))
	h.Write([]byte(strings.Join(sorted, ",")))
}

// ExtraDomains returns a copy of the project's extra allowed domains.
func ExtraDomains(cfg ProjectConfig) []string {
	out := make([]string, len(cfg.AllowDomains))
	copy(out, cfg.AllowDomains)
	return out
}
