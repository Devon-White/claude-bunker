package container

import (
	"archive/tar"
	"container/heap"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/Devon-White/claude-bunker/internal/config"
	"github.com/Devon-White/claude-bunker/internal/log"

	"github.com/google/go-containerregistry/pkg/crane"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"golang.org/x/sync/errgroup"
)

// ResolvedFeature holds a downloaded and extracted devcontainer feature.
type ResolvedFeature struct {
	ID            string                 // feature identifier (e.g. "python")
	Source        string                 // full OCI reference
	InstallDir    string                 // temp dir containing install.sh
	Options       map[string]interface{} // effective options: feature defaults merged under user-specified values
	Env           map[string]string      // feature's containerEnv from metadata
	InstallsAfter []string               // OCI refs this feature should install after
}

// featureMetadata is the subset of devcontainer-feature.json we care about.
type featureMetadata struct {
	ID               string                   `json:"id"`
	RawInstallsAfter []json.RawMessage        `json:"installsAfter"`
	ContainerEnv     map[string]string        `json:"containerEnv"`
	Options          map[string]featureOption `json:"options"`
}

// featureOption is the subset of a devcontainer-feature.json option we use.
// The spec allows string, boolean, or enum options; Default carries whichever
// JSON scalar the feature declared.
type featureOption struct {
	Default interface{} `json:"default"`
}

// mergeOptionDefaults returns a new options map: the feature's declared option
// defaults, overridden by any user-supplied option. It never mutates userOpts.
// Options with no default (Default == nil) are not added — an unset option with
// no default is left for install.sh to handle.
func mergeOptionDefaults(userOpts map[string]interface{}, meta featureMetadata) map[string]interface{} {
	merged := make(map[string]interface{}, len(meta.Options)+len(userOpts))
	for name, opt := range meta.Options {
		if opt.Default != nil {
			merged[name] = opt.Default
		}
	}
	for k, v := range userOpts {
		merged[k] = v
	}
	return merged
}

// installsAfterRefs parses the installsAfter field, which the devcontainer
// spec allows as either ["string"] or [{"feature": "string"}].
func (m featureMetadata) installsAfterRefs() []string {
	var refs []string
	for _, raw := range m.RawInstallsAfter {
		// Try string first (e.g. "ghcr.io/devcontainers/features/common-utils")
		var s string
		if json.Unmarshal(raw, &s) == nil {
			refs = append(refs, s)
			continue
		}
		// Try object (e.g. {"feature": "ghcr.io/devcontainers/features/common-utils"})
		var obj struct {
			Feature string `json:"feature"`
		}
		if json.Unmarshal(raw, &obj) == nil && obj.Feature != "" {
			refs = append(refs, obj.Feature)
		}
	}
	return refs
}

// ResolveFeatures downloads devcontainer features from OCI registries and
// extracts them to temp directories. The returned slice is sorted by
// installsAfter dependencies, then alphabetically by ID.
// The returned cleanup function removes all temp directories.
func ResolveFeatures(features map[string]map[string]interface{}) ([]ResolvedFeature, func(), error) {
	if len(features) == 0 {
		return nil, func() {}, nil
	}

	tmpBase, err := os.MkdirTemp("", "claude-bunker-features-*")
	if err != nil {
		return nil, func() {}, fmt.Errorf("creating temp dir: %w", err)
	}
	cleanup := func() { os.RemoveAll(tmpBase) }

	// Sort feature names for deterministic ordering.
	names := make([]string, 0, len(features))
	for name := range features {
		names = append(names, name)
	}
	sort.Strings(names)

	// Download features concurrently (max 4 parallel).
	resolved := make([]ResolvedFeature, len(names))
	var mu sync.Mutex // protects log output only
	g := new(errgroup.Group)
	g.SetLimit(4)

	for i, name := range names {
		i, name := i, name // capture loop vars
		opts := features[name]
		g.Go(func() error {
			ref, err := config.ResolveFeatureName(name)
			if err != nil {
				return err
			}

			featureDir := filepath.Join(tmpBase, safeFeatureDirName(name))
			if err := os.MkdirAll(featureDir, 0755); err != nil {
				return fmt.Errorf("mkdir %s: %w", name, err)
			}

			mu.Lock()
			log.Infof("Pulling feature: %s", name)
			mu.Unlock()

			if err := downloadAndExtract(ref, featureDir); err != nil {
				return fmt.Errorf("downloading feature %s (%s): %w", name, ref, err)
			}

			meta, err := readFeatureMetadata(featureDir)
			if err != nil {
				// Metadata is optional; use defaults
				meta = featureMetadata{ID: name}
			}
			if meta.ID == "" {
				meta.ID = name
			}

			// Merge the feature's declared option defaults under the user's
			// options, so a feature that relies on its default option values
			// (e.g. version) installs correctly when the user omits them.
			effectiveOpts := mergeOptionDefaults(opts, meta)

			if err := writeFeatureFiles(featureDir, effectiveOpts); err != nil {
				return fmt.Errorf("writing feature files for %s: %w", name, err)
			}

			resolved[i] = ResolvedFeature{
				ID:            meta.ID,
				Source:        ref,
				InstallDir:    featureDir,
				Options:       effectiveOpts,
				Env:           meta.ContainerEnv,
				InstallsAfter: meta.installsAfterRefs(),
			}
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		cleanup()
		return nil, func() {}, err
	}

	sortFeatures(resolved)

	return resolved, cleanup, nil
}

// downloadAndExtract pulls an OCI image and extracts its layers to destDir.
func downloadAndExtract(ref, destDir string) error {
	img, err := crane.Pull(ref)
	if err != nil {
		return fmt.Errorf("pulling %s: %w", ref, err)
	}

	// Log the resolved digest for supply-chain auditability.
	// Tags are mutable — the digest provides a pinnable reference
	// for reproducing this exact build.
	if digest, err := img.Digest(); err == nil {
		log.Infof("  %s → %s", ref, digest.String())
	}

	return extractImage(img, destDir)
}

// extractImage extracts all layers of an OCI image to destDir.
func extractImage(img v1.Image, destDir string) error {
	reader := mutate.Extract(img)
	defer reader.Close()

	return extractTar(reader, destDir)
}

// maxExtractFileSize is the maximum allowed size for a single file extracted
// from a tar archive. This guards against tar-bomb attacks from malicious OCI
// feature layers.
const maxExtractFileSize = 500 * 1024 * 1024 // 500 MB

// extractTar extracts a tar stream to destDir.
func extractTar(r io.Reader, destDir string) error {
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		cleaned := filepath.Clean(hdr.Name)
		if cleaned == "." {
			continue // root directory entry, skip
		}
		target := filepath.Join(destDir, cleaned)
		if !strings.HasPrefix(target, filepath.Clean(destDir)+string(os.PathSeparator)) {
			return fmt.Errorf("tar entry %q escapes destination directory", hdr.Name)
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			if hdr.Size > maxExtractFileSize {
				return fmt.Errorf("tar entry %q size %d exceeds maximum allowed size %d", hdr.Name, hdr.Size, maxExtractFileSize)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, io.LimitReader(tr, maxExtractFileSize+1)); err != nil {
				f.Close()
				return err
			}
			f.Close()
		}
	}
	return nil
}

// readFeatureMetadata reads devcontainer-feature.json from a feature directory.
func readFeatureMetadata(featureDir string) (featureMetadata, error) {
	data, err := os.ReadFile(filepath.Join(featureDir, "devcontainer-feature.json"))
	if err != nil {
		return featureMetadata{}, err
	}
	var meta featureMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return featureMetadata{}, err
	}
	return meta, nil
}

// featureHeap is a min-heap of feature indices, ordered alphabetically by ID.
// It is used by sortFeatures to break ties during topological sort.
type featureHeap struct {
	indices  []int
	features []ResolvedFeature
}

func (h featureHeap) Len() int { return len(h.indices) }
func (h featureHeap) Less(i, j int) bool {
	return h.features[h.indices[i]].ID < h.features[h.indices[j]].ID
}
func (h featureHeap) Swap(i, j int) { h.indices[i], h.indices[j] = h.indices[j], h.indices[i] }

func (h *featureHeap) Push(x any) {
	h.indices = append(h.indices, x.(int))
}

func (h *featureHeap) Pop() any {
	old := h.indices
	n := len(old)
	val := old[n-1]
	h.indices = old[:n-1]
	return val
}

// sortFeatures sorts resolved features by installsAfter dependencies,
// then alphabetically by ID. Uses Kahn's algorithm (BFS-based topological
// sort) to correctly handle transitive dependencies.
func sortFeatures(features []ResolvedFeature) {
	n := len(features)
	if n <= 1 {
		return
	}

	// Fast path: if no features declare installsAfter dependencies,
	// a simple alphabetical sort is sufficient.
	hasDeps := false
	for _, f := range features {
		if len(f.InstallsAfter) > 0 {
			hasDeps = true
			break
		}
	}
	if !hasDeps {
		sort.Slice(features, func(i, j int) bool {
			return features[i].ID < features[j].ID
		})
		return
	}

	// Map each OCI Source ref to its index in the features slice.
	// This lets us resolve InstallsAfter refs to concrete features.
	sourceToIdx := make(map[string]int, n)
	for i, f := range features {
		sourceToIdx[f.Source] = i
	}

	// Build adjacency list and in-degree counts.
	// If feature B has InstallsAfter containing source of A,
	// then A must come before B: edge A -> B.
	adj := make([][]int, n)
	inDeg := make([]int, n)
	for i, f := range features {
		for _, depRef := range f.InstallsAfter {
			if j, ok := sourceToIdx[depRef]; ok {
				adj[j] = append(adj[j], i)
				inDeg[i]++
			}
		}
	}

	// Initialize min-heap with all features that have in-degree 0.
	h := &featureHeap{features: features}
	heap.Init(h)
	for i := 0; i < n; i++ {
		if inDeg[i] == 0 {
			heap.Push(h, i)
		}
	}

	// BFS: pop the alphabetically-first feature with in-degree 0,
	// append it to the result, and decrement in-degrees of its dependents.
	// Track visited indices so the cycle fallback doesn't depend on Source uniqueness.
	visited := make(map[int]bool, n)
	result := make([]ResolvedFeature, 0, n)
	for h.Len() > 0 {
		idx := heap.Pop(h).(int)
		visited[idx] = true
		result = append(result, features[idx])
		for _, neighbor := range adj[idx] {
			inDeg[neighbor]--
			if inDeg[neighbor] == 0 {
				heap.Push(h, neighbor)
			}
		}
	}

	// If there's a cycle, some features won't appear in result.
	// Append any remaining features alphabetically so nothing is lost.
	if len(result) < n {
		var remaining []ResolvedFeature
		for i, f := range features {
			if !visited[i] {
				remaining = append(remaining, f)
			}
		}
		sort.Slice(remaining, func(i, j int) bool {
			return remaining[i].ID < remaining[j].ID
		})

		cycleIDs := make([]string, len(remaining))
		for i, f := range remaining {
			cycleIDs[i] = f.ID
		}
		log.Warnf("dependency cycle detected among features: %s — installing in alphabetical order", strings.Join(cycleIDs, ", "))

		result = append(result, remaining...)
	}

	// Copy result back into the original slice (in-place sort).
	copy(features, result)
}

// writeFeatureFiles generates the devcontainer-features.env (options) and
// devcontainer-features-install.sh (wrapper) files in the feature directory.
// This matches how the official devcontainer CLI passes options to install.sh:
// options go in an env file that the wrapper sources before running install.sh,
// so they're available during installation but don't persist as image ENV vars.
func writeFeatureFiles(featureDir string, opts map[string]interface{}) error {
	// Write devcontainer-features.env with options as env vars.
	var envBuf strings.Builder
	if len(opts) > 0 {
		keys := make([]string, 0, len(opts))
		for k := range opts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			// Use single quotes to prevent shell command substitution.
			// Escape embedded single quotes with the '\'' idiom, and strip
			// newlines to prevent shell injection via multiline values.
			val := fmt.Sprintf("%v", opts[k])
			val = strings.ReplaceAll(val, "\n", " ")
			val = strings.ReplaceAll(val, "\r", "")
			val = strings.ReplaceAll(val, "'", `'\''`)
			envBuf.WriteString(fmt.Sprintf("%s='%s'\n", safeOptionEnvName(k), val))
		}
	}
	if err := os.WriteFile(filepath.Join(featureDir, "devcontainer-features.env"), []byte(envBuf.String()), 0644); err != nil {
		return fmt.Errorf("writing env file: %w", err)
	}

	// Write devcontainer-features-install.sh wrapper that sources the env
	// file then runs the feature's install.sh.
	wrapper := "#!/bin/sh\nset -e\nset -a\n. ./devcontainer-features.env\nset +a\nchmod +x ./install.sh\n./install.sh\n"
	if err := os.WriteFile(filepath.Join(featureDir, "devcontainer-features-install.sh"), []byte(wrapper), 0755); err != nil {
		return fmt.Errorf("writing wrapper script: %w", err)
	}

	return nil
}

// nonWordRe matches characters that are not alphanumeric or underscore.
var nonWordRe = regexp.MustCompile(`[^0-9A-Za-z_]`)

// leadingNonAlphaRe matches leading digits and underscores.
var leadingNonAlphaRe = regexp.MustCompile(`^[0-9_]+`)

// safeOptionEnvName converts a feature option key to the environment variable
// name expected by install.sh, matching the devcontainer spec's getSafeId:
// replace non-word chars with _, strip leading digits/underscores, uppercase.
// e.g. "version" -> "VERSION", "golangciLintVersion" -> "GOLANGCILINTVERSION"
func safeOptionEnvName(key string) string {
	s := nonWordRe.ReplaceAllString(key, "_")
	s = leadingNonAlphaRe.ReplaceAllString(s, "_")
	return strings.ToUpper(s)
}

// safeFeatureDirName extracts a filesystem-safe directory name from an OCI
// reference like "ghcr.io/devcontainers/features/go:1". It takes the last
// path segment and strips the tag (e.g. "go"). This avoids Windows failures
// from : and / in directory names.
func safeFeatureDirName(ociRef string) string {
	// Strip tag/digest (everything after the last ":")
	if idx := strings.LastIndex(ociRef, ":"); idx != -1 {
		ociRef = ociRef[:idx]
	}
	// Take last path segment
	if idx := strings.LastIndex(ociRef, "/"); idx != -1 {
		return ociRef[idx+1:]
	}
	return ociRef
}
