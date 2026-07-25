package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/huh"

	"github.com/Devon-White/claude-bunker/internal/devcontainer"
)

func TestNonInteractiveInit(t *testing.T) {
	// Without --defaults, init must refuse (no write) and return an error.
	write, err := nonInteractiveInit(false)
	if err == nil {
		t.Error("expected an error when not a TTY and --defaults not given")
	}
	if write {
		t.Error("must not write a config when refusing")
	}
	// With --defaults, init writes.
	write, err = nonInteractiveInit(true)
	if err != nil {
		t.Errorf("--defaults should not error, got %v", err)
	}
	if !write {
		t.Error("--defaults should write a config")
	}
}

func TestAbortErr(t *testing.T) {
	if got := abortErr(huh.ErrUserAborted); ExitCodeFor(got) != ExitCancelled {
		t.Errorf("user abort should map to ExitCancelled, got code %d", ExitCodeFor(got))
	}
	other := errors.New("form failed")
	if got := abortErr(other); !errors.Is(got, other) {
		t.Errorf("non-abort error should pass through, got %v", got)
	}
}

func TestRunInit_NonTTYWithoutDefaultsLeavesFileUntouched(t *testing.T) {
	ws := t.TempDir()
	t.Setenv("CLAUDE_BUNKER_WS", ws)

	cfgPath := devcontainer.DevContainerPath(ws)
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"customizations":{"claude-bunker":{"apt":["ripgrep"]}}}` + "\n")
	if err := os.WriteFile(cfgPath, original, 0o644); err != nil {
		t.Fatal(err)
	}

	// Force the non-interactive path.
	orig := stdinIsTTY
	stdinIsTTY = func() bool { return false }
	t.Cleanup(func() { stdinIsTTY = orig })

	err := runInit(initCmd, nil)
	if err == nil {
		t.Fatal("expected runInit to error on non-TTY without --defaults")
	}

	got, readErr := os.ReadFile(cfgPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != string(original) {
		t.Errorf("config was modified: got %q, want %q", got, original)
	}
}
