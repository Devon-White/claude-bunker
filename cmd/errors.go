package cmd

import (
	"errors"
	"fmt"
	"io"
)

// Process exit codes. Phase 0 defines the ones it needs; later phases extend
// this into the full exit-code catalog.
const (
	ExitOK                = 0
	ExitError             = 1
	ExitCancelled         = 2
	ExitDockerUnavailable = 4
)

// CodedError carries a process exit code alongside an error.
type CodedError struct {
	Code int
	Err  error
}

func (e *CodedError) Error() string { return e.Err.Error() }
func (e *CodedError) Unwrap() error { return e.Err }

// Coded wraps err with a specific process exit code. Returns nil if err is nil.
func Coded(code int, err error) error {
	if err == nil {
		return nil
	}
	return &CodedError{Code: code, Err: err}
}

// ExitCodeFor returns the process exit code for an error: a CodedError's Code
// (found anywhere in the wrap chain), 1 for any other non-nil error, 0 for nil.
func ExitCodeFor(err error) int {
	if err == nil {
		return ExitOK
	}
	var ce *CodedError
	if errors.As(err, &ce) {
		return ce.Code
	}
	return ExitError
}

// PrintError renders a styled error line to w. Used by main() for errors that
// bubble up from Execute() (which are otherwise silenced by SilenceErrors).
func PrintError(w io.Writer, err error) {
	fmt.Fprintln(w,
		prefixStyle.Render("[claude-bunker]"),
		errorLabelStyle.Render("ERROR:"),
		err.Error(),
	)
}
