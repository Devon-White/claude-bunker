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

// lockedResolvedRef converts a feature reference (with an optional tag OR an
// existing digest) and a digest into the spec's `resolved` form: base ref
// (tag/digest stripped) + @digest. It preserves a registry:port host.
func lockedResolvedRef(featureRef, digest string) string {
	base := featureRef
	if at := strings.Index(base, "@"); at != -1 {
		base = base[:at]
	}
	if slash := strings.LastIndex(base, "/"); slash != -1 {
		if colon := strings.LastIndex(base[slash:], ":"); colon != -1 {
			base = base[:slash+colon]
		}
	}
	return base + "@" + digest
}

// buildLockFile assembles a LockFile from feature-ref → digest and
// feature-ref → version maps.
func buildLockFile(refToDigest, refToVersion map[string]string) LockFile {
	l := LockFile{Features: make(map[string]LockedFeature, len(refToDigest))}
	for ref, digest := range refToDigest {
		if digest == "" {
			continue
		}
		l.Features[ref] = LockedFeature{
			Version:   refToVersion[ref],
			Resolved:  lockedResolvedRef(ref, digest),
			Integrity: digest,
		}
	}
	return l
}

// writeMergedLock loads the existing lock, overlays the freshly-resolved
// feature digests/versions, and saves — so entries for features present in
// the committed devcontainer.json but not resolved this run (e.g.
// bunker-managed features like claude-code that are stripped before bunker's
// own feature resolution) are preserved for VS Code/Codespaces instead of
// being dropped by a wholesale overwrite. On a genuine re-resolve (e.g.
// --rebuild), the freshly-resolved entry for a given ref still wins, since
// it's overlaid last — only entries not resolved this run are preserved
// as-is.
func writeMergedLock(workspace string, refToDigest, refToVersion map[string]string) error {
	existing, _ := LoadLockFile(workspace)
	merged := buildLockFile(refToDigest, refToVersion) // fresh entries
	if len(existing.Features) > 0 {
		out := make(map[string]LockedFeature, len(existing.Features)+len(merged.Features))
		for ref, lf := range existing.Features {
			out[ref] = lf // base = existing (preserved)
		}
		for ref, lf := range merged.Features {
			out[ref] = lf // fresh wins on conflict
		}
		merged.Features = out
	}
	return merged.Save(workspace)
}
