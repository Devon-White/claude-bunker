package sessions

import (
	"os"
	"testing"
)

// TestMain redirects the JSON stores to a throwaway directory for the entire
// package test run, so tests can never wipe the developer's real
// ~/.claude/session-names.json (FetchSnapshot calls PruneStaleNames, which with
// fake container IDs would otherwise delete every real custom name).
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "bunker-sessions-test-*")
	if err != nil {
		panic(err)
	}
	os.Setenv("CLAUDE_BUNKER_STORE_DIR", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
