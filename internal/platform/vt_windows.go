//go:build windows

package platform

import (
	"os"

	"golang.org/x/sys/windows"
)

const (
	enableVirtualTerminalInput      = 0x0200
	enableVirtualTerminalProcessing = 0x0004
)

// EnableVTMode enables VT100 escape sequence processing on Windows.
// Without this, arrow keys generate Windows virtual key events instead
// of VT100 escape sequences (\x1b[A, etc.), breaking interactive menus.
func EnableVTMode() {
	// Enable VT input on stdin — arrow keys become escape sequences
	h := windows.Handle(os.Stdin.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err == nil {
		_ = windows.SetConsoleMode(h, mode|enableVirtualTerminalInput)
	}

	// Enable VT output on stdout — terminal renders escape sequences
	h = windows.Handle(os.Stdout.Fd())
	if err := windows.GetConsoleMode(h, &mode); err == nil {
		_ = windows.SetConsoleMode(h, mode|enableVirtualTerminalProcessing)
	}
}
