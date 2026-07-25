package container

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLockedResolvedRef(t *testing.T) {
	tests := []struct {
		name, ref, digest, want string
	}{
		{"tagged ref", "ghcr.io/devcontainers/features/node:1", "sha256:abc", "ghcr.io/devcontainers/features/node@sha256:abc"},
		{"ref with registry port and tag", "reg.example.com:5000/f/go:1.2", "sha256:def", "reg.example.com:5000/f/go@sha256:def"},
		{"untagged ref", "ghcr.io/devcontainers/features/node", "sha256:abc", "ghcr.io/devcontainers/features/node@sha256:abc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := lockedResolvedRef(tt.ref, tt.digest); got != tt.want {
				t.Errorf("lockedResolvedRef(%q,%q) = %q, want %q", tt.ref, tt.digest, got, tt.want)
			}
		})
	}
}

func TestLockFile_SaveLoadRoundTrip(t *testing.T) {
	ws := t.TempDir()

	// Absent file loads as empty, no error.
	l, err := LoadLockFile(ws)
	if err != nil {
		t.Fatalf("LoadLockFile(absent): %v", err)
	}
	if len(l.Features) != 0 {
		t.Fatalf("absent lock should be empty, got %d", len(l.Features))
	}

	l = buildLockFile(
		map[string]string{"ghcr.io/devcontainers/features/node:1": "sha256:abc"},
		map[string]string{"ghcr.io/devcontainers/features/node:1": "1.0.4"},
	)
	if err := l.Save(ws); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Written to the standard path.
	want := filepath.Join(ws, ".devcontainer", "devcontainer-lock.json")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("lock not at standard path %s: %v", want, err)
	}

	got, err := LoadLockFile(ws)
	if err != nil {
		t.Fatalf("LoadLockFile: %v", err)
	}
	f, ok := got.Features["ghcr.io/devcontainers/features/node:1"]
	if !ok {
		t.Fatal("feature missing after round-trip")
	}
	if f.Resolved != "ghcr.io/devcontainers/features/node@sha256:abc" {
		t.Errorf("resolved = %q", f.Resolved)
	}
	if f.Integrity != "sha256:abc" {
		t.Errorf("integrity = %q", f.Integrity)
	}
	if f.Version != "1.0.4" {
		t.Errorf("version = %q", f.Version)
	}
}
