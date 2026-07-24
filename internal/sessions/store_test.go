package sessions

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoreHonorsStoreDirEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_BUNKER_STORE_DIR", dir)

	s := newJSONMapStore("probe.json")
	if err := s.Set("k", "v"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// The file must be written under the temp dir, NOT under the real ~/.claude.
	want := filepath.Join(dir, "probe.json")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("expected store file at %s: %v", want, err)
	}
	if got := s.Get("k"); got != "v" {
		t.Errorf("Get = %q, want v", got)
	}
}
