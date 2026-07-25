package cmd

import (
	"fmt"
	"io"
	"os"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"golang.org/x/term"
)

// --- Color palette (light/dark adaptive) ---

var (
	colorBrand   = lipgloss.AdaptiveColor{Light: "#2563EB", Dark: "#60A5FA"} // muted blue
	colorSuccess = lipgloss.AdaptiveColor{Light: "#16A34A", Dark: "#4ADE80"}
	colorWarn    = lipgloss.AdaptiveColor{Light: "#CA8A04", Dark: "#FACC15"}
	colorError   = lipgloss.AdaptiveColor{Light: "#DC2626", Dark: "#F87171"}
	colorDim     = lipgloss.AdaptiveColor{Light: "#6B7280", Dark: "#9CA3AF"}
)

// Output streams. Seams so tests can capture output. Diagnostics go to errW
// (stderr) to keep stdout clean for piping; payload commands write to stdout.
var (
	outW io.Writer = os.Stdout
	errW io.Writer = os.Stderr
)

// --- Styles ---

var (
	prefixStyle       = lipgloss.NewStyle().Foreground(colorBrand).Bold(true)
	infoMsgStyle      = lipgloss.NewStyle()
	verboseMsgStyle   = lipgloss.NewStyle().Foreground(colorDim)
	warnLabelStyle    = lipgloss.NewStyle().Foreground(colorWarn).Bold(true)
	errorLabelStyle   = lipgloss.NewStyle().Foreground(colorError).Bold(true)
	successMsgStyle   = lipgloss.NewStyle().Foreground(colorSuccess)
	dimStyle          = lipgloss.NewStyle().Foreground(colorDim)
	brandStyle        = lipgloss.NewStyle().Foreground(colorBrand)
	boldStyle         = lipgloss.NewStyle().Bold(true)
	sectionHeaderStyle = lipgloss.NewStyle().Foreground(colorBrand).Bold(true)
)

// --- Styled output functions (drop-in replacements) ---

func info(msg string) {
	if verbosity >= 0 {
		fmt.Fprintln(errW, prefixStyle.Render("[claude-bunker]"), infoMsgStyle.Render(msg))
	}
}

func verbose(msg string) {
	if verbosity >= 1 {
		fmt.Fprintln(errW, prefixStyle.Render("[claude-bunker]"), verboseMsgStyle.Render(msg))
	}
}

func warn(msg string) {
	if verbosity >= 0 {
		fmt.Fprintln(errW,
			prefixStyle.Render("[claude-bunker]"),
			warnLabelStyle.Render("WARNING:"),
			msg,
		)
	}
}

func die(msg string) {
	fmt.Fprintln(errW,
		prefixStyle.Render("[claude-bunker]"),
		errorLabelStyle.Render("ERROR:"),
		msg,
	)
	if activeRunner != nil {
		activeRunner.cancel()
		activeRunner.cleanup()
		if activeRunner.cli != nil {
			activeRunner.cli.Close()
		}
	}
	os.Exit(1)
}

func success(msg string) {
	if verbosity >= 0 {
		fmt.Fprintln(errW, prefixStyle.Render("[claude-bunker]"), successMsgStyle.Render(msg))
	}
}

// --- Key-value helpers for status output ---

const kvLabelWidth = 12

func kvLine(label, value string) string {
	return dimStyle.Render(fmt.Sprintf("%-*s", kvLabelWidth, label)) + " " + value
}

func kvLineStyled(label, value string, style lipgloss.Style) string {
	return dimStyle.Render(fmt.Sprintf("%-*s", kvLabelWidth, label)) + " " + style.Render(value)
}

func configLine(key, value string) string {
	return "  " + dimStyle.Render(fmt.Sprintf("%-*s", kvLabelWidth-2, key)) + " " + value
}

// --- State color mapping ---

var (
	stateRunningStyle = lipgloss.NewStyle().Foreground(colorSuccess)
	stateErrorStyle   = lipgloss.NewStyle().Foreground(colorError)
)

func stateStyle(state string) lipgloss.Style {
	switch state {
	case "running":
		return stateRunningStyle
	case "exited", "dead":
		return stateErrorStyle
	default:
		return dimStyle
	}
}

// isTTY returns true when stdin is an interactive terminal.
func isTTY() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// shouldUseColor decides whether to emit ANSI color, per the no-color.org and
// CLICOLOR conventions. Precedence: NO_COLOR (any value) disables; else
// CLICOLOR_FORCE (non-empty, != "0") forces on; else --no-color / CLICOLOR=0
// disable; else color follows whether the diagnostic stream is a TTY.
func shouldUseColor(noColorFlag bool, env func(string) (string, bool), stderrIsTTY bool) bool {
	if _, ok := env("NO_COLOR"); ok {
		return false
	}
	if v, ok := env("CLICOLOR_FORCE"); ok && v != "" && v != "0" {
		return true
	}
	if noColorFlag {
		return false
	}
	if v, ok := env("CLICOLOR"); ok && v == "0" {
		return false
	}
	return stderrIsTTY
}

// isStderrTTY reports whether stderr (where diagnostics go) is an interactive terminal.
func isStderrTTY() bool {
	return term.IsTerminal(int(os.Stderr.Fd()))
}

// applyColorProfile sets lipgloss's color profile from the real environment.
// Call once early in Execute, before any styled output.
func applyColorProfile(noColorFlag bool) {
	if !shouldUseColor(noColorFlag, os.LookupEnv, isStderrTTY()) {
		lipgloss.SetColorProfile(termenv.Ascii)
	}
}

// confirmAction shows a yes/no prompt. Returns false on abort, error, or "No".
func confirmAction(title string) bool {
	if !isTTY() {
		return false
	}
	var confirmed bool
	err := huh.NewForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title(title).
				Affirmative("Yes").
				Negative("No").
				Value(&confirmed),
		),
	).Run()
	return err == nil && confirmed
}

// --- Version renderer ---

func renderVersion(version string) string {
	return brandStyle.Render("claude-bunker") + " " + boldStyle.Render(version)
}
