package platform

import (
	"os"
	"sync"

	"golang.org/x/term"
)

// savedState holds the pre-raw terminal state so it can be restored from
// signal handlers and die() paths that bypass defers.
var (
	savedMu    sync.Mutex
	savedState *term.State
)

// MakeRaw puts the terminal into raw mode. Returns the old state for restoring.
// The old state is also saved globally so RestoreSaved can recover it from
// code paths that bypass defers (signal handlers, os.Exit).
func MakeRaw() (*term.State, error) {
	fd := int(os.Stdin.Fd())
	old, err := term.MakeRaw(fd)
	if err == nil {
		savedMu.Lock()
		savedState = old
		savedMu.Unlock()
	}
	return old, err
}

// Restore restores the terminal to its previous state and clears the saved
// global copy.
func Restore(state *term.State) {
	if state != nil {
		fd := int(os.Stdin.Fd())
		term.Restore(fd, state)
		savedMu.Lock()
		savedState = nil
		savedMu.Unlock()
	}
}

// RestoreSaved restores the terminal from the globally saved state, if any.
// Safe to call from signal handlers or cleanup code that cannot access the
// local oldState variable.
func RestoreSaved() {
	savedMu.Lock()
	s := savedState
	savedState = nil
	savedMu.Unlock()
	if s != nil {
		fd := int(os.Stdin.Fd())
		term.Restore(fd, s)
	}
}

// GetSize returns the current terminal width and height.
func GetSize() (int, int) {
	fd := int(os.Stdout.Fd())
	w, h, err := term.GetSize(fd)
	if err != nil {
		return 0, 0
	}
	return w, h
}
