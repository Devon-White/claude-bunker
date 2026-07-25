package container

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// LockedFeature is one entry in the Dev Container standard devcontainer-lock.json.
type LockedFeature struct {
	Version   string `json:"version,omitempty"`
	Resolved  string `json:"resolved"`
	Integrity string `json:"integrity,omitempty"`
}

// LockFile is the Dev Container spec lockfile: a map of feature reference →
// resolved digest, committed to the repo for reproducible builds.
type LockFile struct {
	Features map[string]LockedFeature `json:"features"`
}

// LockFilePath returns the standard lockfile path: <workspace>/.devcontainer/devcontainer-lock.json.
func LockFilePath(workspace string) string {
	return filepath.Join(workspace, ".devcontainer", "devcontainer-lock.json")
}

// LoadLockFile reads the lockfile. An absent file is not an error — it returns
// an empty (but non-nil) LockFile.
func LoadLockFile(workspace string) (LockFile, error) {
	l := LockFile{Features: map[string]LockedFeature{}}
	data, err := os.ReadFile(LockFilePath(workspace))
	if err != nil {
		if os.IsNotExist(err) {
			return l, nil
		}
		return l, err
	}
	if err := json.Unmarshal(data, &l); err != nil {
		return LockFile{Features: map[string]LockedFeature{}}, err
	}
	if l.Features == nil {
		l.Features = map[string]LockedFeature{}
	}
	return l, nil
}

// Save writes the lockfile to the standard path, creating .devcontainer/ if needed.
func (l LockFile) Save(workspace string) error {
	p := LockFilePath(workspace)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(p, data, 0o644)
}

// lockedResolvedRef converts a feature reference (with an optional tag) and a
// digest into the spec's `resolved` form: base ref (tag stripped) + @digest.
// It preserves a registry:port host (only the final :tag path segment is stripped).
func lockedResolvedRef(featureRef, digest string) string {
	base := featureRef
	if slash := strings.LastIndex(featureRef, "/"); slash != -1 {
		if colon := strings.LastIndex(featureRef[slash:], ":"); colon != -1 {
			base = featureRef[:slash+colon]
		}
	}
	return base + "@" + digest
}

// buildLockFile assembles a LockFile from feature-ref → digest and
// feature-ref → version maps.
func buildLockFile(refToDigest, refToVersion map[string]string) LockFile {
	l := LockFile{Features: make(map[string]LockedFeature, len(refToDigest))}
	for ref, digest := range refToDigest {
		l.Features[ref] = LockedFeature{
			Version:   refToVersion[ref],
			Resolved:  lockedResolvedRef(ref, digest),
			Integrity: digest,
		}
	}
	return l
}
