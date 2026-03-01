package container

import (
	"archive/tar"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Devon-White/claude-bunker/internal/config"

	"github.com/google/go-containerregistry/pkg/crane"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
)

// ResolvedFeature holds a downloaded and extracted devcontainer feature.
type ResolvedFeature struct {
	ID            string                 // feature identifier (e.g. "python")
	Source        string                 // full OCI reference
	InstallDir    string                 // temp dir containing install.sh
	Options       map[string]interface{} // user-specified options
	Env           map[string]string      // feature's containerEnv from metadata
	InstallsAfter []string               // OCI refs this feature should install after
}

// featureMetadata is the subset of devcontainer-feature.json we care about.
type featureMetadata struct {
	ID            string            `json:"id"`
	InstallsAfter []struct {
		Feature string `json:"feature"`
	} `json:"installsAfter"`
	ContainerEnv map[string]string `json:"containerEnv"`
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

	var resolved []ResolvedFeature

	for name, opts := range features {
		ref, err := config.ResolveFeatureName(name)
		if err != nil {
			cleanup()
			return nil, func() {}, err
		}

		featureDir := filepath.Join(tmpBase, name)
		if err := os.MkdirAll(featureDir, 0755); err != nil {
			cleanup()
			return nil, func() {}, fmt.Errorf("mkdir %s: %w", name, err)
		}

		if err := downloadAndExtract(ref, featureDir); err != nil {
			cleanup()
			return nil, func() {}, fmt.Errorf("downloading feature %s (%s): %w", name, ref, err)
		}

		meta, err := readFeatureMetadata(featureDir)
		if err != nil {
			// Metadata is optional; use defaults
			meta = featureMetadata{ID: name}
		}

		if meta.ID == "" {
			meta.ID = name
		}

		var installsAfter []string
		for _, dep := range meta.InstallsAfter {
			installsAfter = append(installsAfter, dep.Feature)
		}

		resolved = append(resolved, ResolvedFeature{
			ID:            meta.ID,
			Source:        ref,
			InstallDir:    featureDir,
			Options:       opts,
			Env:           meta.ContainerEnv,
			InstallsAfter: installsAfter,
		})
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

	return extractImage(img, destDir)
}

// extractImage extracts all layers of an OCI image to destDir.
func extractImage(img v1.Image, destDir string) error {
	reader := mutate.Extract(img)
	defer reader.Close()

	return extractTar(reader, destDir)
}

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

		target := filepath.Join(destDir, filepath.Clean(hdr.Name))
		if !strings.HasPrefix(target, filepath.Clean(destDir)+string(os.PathSeparator)) {
			return fmt.Errorf("tar entry %q escapes destination directory", hdr.Name)
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
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

// sortFeatures sorts resolved features by installsAfter dependencies,
// then alphabetically by ID. Uses the cached InstallsAfter field.
func sortFeatures(features []ResolvedFeature) {
	// Build a set of OCI refs that each feature should come after
	afterMap := make(map[string]map[string]bool)
	for _, f := range features {
		deps := make(map[string]bool)
		for _, ref := range f.InstallsAfter {
			deps[ref] = true
		}
		afterMap[f.ID] = deps
	}

	sort.SliceStable(features, func(i, j int) bool {
		// If j should install after i, then i comes first
		if afterMap[features[j].ID][features[i].Source] {
			return true
		}
		// If i should install after j, then j comes first
		if afterMap[features[i].ID][features[j].Source] {
			return false
		}
		// Alphabetical fallback
		return features[i].ID < features[j].ID
	})
}
