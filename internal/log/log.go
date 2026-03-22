package log

import (
	"fmt"
	"os"
)

// WarnFunc is the function used for warning output. The cmd package overrides
// this with styled output during startup; internal packages call Warn/Warnf
// which delegate to it. The default prints plain text to stderr.
var WarnFunc func(string) = func(msg string) {
	fmt.Fprintln(os.Stderr, "[claude-bunker] WARNING:", msg)
}

// InfoFunc is the function used for informational output.
var InfoFunc func(string) = func(msg string) {
	fmt.Fprintln(os.Stderr, "[claude-bunker]", msg)
}

// Warn emits a warning message via WarnFunc.
func Warn(msg string) { WarnFunc(msg) }

// Warnf emits a formatted warning message via WarnFunc.
func Warnf(format string, args ...any) { WarnFunc(fmt.Sprintf(format, args...)) }

// Info emits an informational message via InfoFunc.
func Info(msg string) { InfoFunc(msg) }

// Infof emits a formatted informational message via InfoFunc.
func Infof(format string, args ...any) { InfoFunc(fmt.Sprintf(format, args...)) }
