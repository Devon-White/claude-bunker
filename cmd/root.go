package cmd

import (
	"fmt"
	"os"
	"slices"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/Devon-White/claude-bunker/internal/container"
)

// Version, Commit, and Date are set via ldflags at build time.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// RootCmd returns the root command. Exported so out-of-package tools
// (cmd/genman man-page generation) can traverse the command tree.
func RootCmd() *cobra.Command { return rootCmd }

var rootCmd = &cobra.Command{
	Use:   "claude-bunker [flags]",
	Short: "Run Claude Code in a sandboxed container",
	Long: `claude-bunker — Run Claude Code in a sandboxed container.

The sandbox starts automatically and tears down when Claude exits.
On first run the container image is built (takes a few minutes, cached
after that). Configuration changes are detected automatically.`,
	// Don't show errors twice (we handle them ourselves)
	SilenceErrors: true,
	SilenceUsage:  true,
	// DisableFlagParsing lets unknown flags pass through to claude
	DisableFlagParsing: true,
	RunE:               runDefault,
}

// passthroughUsage documents the flag-passthrough contract and the `--`
// terminator, which cobra cannot infer. It is appended to the default usage
// template so it renders in `claude-bunker --help` alongside the Flags section.
const passthroughUsage = `
Passthrough:
  Unknown flags are forwarded to claude.
  Use -- to force everything after it to claude verbatim, e.g.:
    claude-bunker --keep -- --model opus -p "hi"
`

func init() {
	// Re-enable flag parsing for subcommands
	shellCmd.DisableFlagParsing = false
	pruneCmd.DisableFlagParsing = false
	statusCmd.DisableFlagParsing = false
	initCmd.DisableFlagParsing = false
	logsCmd.DisableFlagParsing = false
	completionCmd.DisableFlagParsing = false
	sessionsCmd.DisableFlagParsing = false
	doctorCmd.DisableFlagParsing = false

	// Add --verbose/--quiet/--no-color flags to subcommands (root command handles
	// these via extractBunkerFlags since its flag parsing is disabled).
	for _, cmd := range []*cobra.Command{shellCmd, pruneCmd, statusCmd, initCmd, logsCmd, sessionsCmd, doctorCmd} {
		cmd.Flags().BoolP("verbose", "V", false, "Show detailed output")
		cmd.Flags().BoolP("quiet", "q", false, "Suppress informational output")
		cmd.Flags().Bool("no-color", false, "Disable ANSI color output")
	}

	initCmd.Flags().Bool("defaults", false, "Write a default config non-interactively (no prompts)")
	shellCmd.Flags().Bool("dry-run", false, "Show what would be built/created without launching")
	initCmd.Flags().Bool("dry-run", false, "Show what would be written without creating any files")
	versionCmd.Flags().Bool("json", false, "Output as JSON")
	statusCmd.Flags().Bool("json", false, "Output as JSON")

	// Register root's own flags for --help documentation ONLY. Root keeps
	// DisableFlagParsing:true for normal runs (so unknown claude flags pass
	// through unmodified), and extractBunkerFlags (cmd/run.go) stays the single
	// source of truth for these on the run path. They render in
	// `claude-bunker --help` because Execute() flips DisableFlagParsing=false
	// before SetArgs(["--help"]). Do NOT read these via rootCmd.Flags() on the
	// run path — they are zero-valued there because parsing is disabled.
	// (--interval is intentionally absent; it is sessions-scoped in Task 5.)
	rootCmd.Flags().Bool("keep", false, "Keep the container running after exit")
	rootCmd.Flags().Bool("rebuild", false, "Force a clean image rebuild (clears cache)")
	rootCmd.Flags().Bool("dry-run", false, "Plan the build/create/launch without performing it")
	rootCmd.Flags().String("gh-token", "", "GitHub token to inject (overrides config/env)")
	rootCmd.Flags().String("api-key", "", "Anthropic API key to inject")
	rootCmd.Flags().String("oauth-token", "", "Claude Code OAuth token to inject")
	rootCmd.Flags().BoolP("verbose", "V", false, "Show detailed output")
	rootCmd.Flags().BoolP("quiet", "q", false, "Suppress informational output")
	rootCmd.Flags().Bool("force", false, "Override fail-closed safety guards")
	rootCmd.Flags().Bool("no-sandbox", false, "Launch even if sandbox settings can't be seeded (NOT recommended)")
	rootCmd.Flags().Bool("no-color", false, "Disable ANSI color output")

	// Document the passthrough / `--` terminator contract, which cobra cannot
	// infer. Appended to the default usage template so it survives alongside the
	// auto-generated Flags section on the --help path.
	rootCmd.SetUsageTemplate(rootCmd.UsageTemplate() + passthroughUsage)

	rootCmd.AddCommand(shellCmd)
	rootCmd.AddCommand(pruneCmd)
	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(logsCmd)
	rootCmd.AddCommand(completionCmd)
	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(sessionsCmd)
	rootCmd.AddCommand(doctorCmd)
}

// initVerbosity sets the verbosity level from cobra flags or the CLAUDE_BUNKER_QUIET env var.
// Call from subcommand RunE functions before any output.
func initVerbosity(cmd *cobra.Command) {
	if q, _ := cmd.Flags().GetBool("quiet"); q || os.Getenv(envQuiet) == "1" {
		verbosity = -1
	} else if v, _ := cmd.Flags().GetBool("verbose"); v {
		verbosity = 1
	}
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version",
	Run: func(cmd *cobra.Command, args []string) {
		if j, _ := cmd.Flags().GetBool("json"); j {
			fmt.Println(renderVersionJSON(Version))
			return
		}
		fmt.Println(renderVersion(Version))
	},
}

// renderVersionJSON renders the version as a minimal JSON object.
func renderVersionJSON(version string) string {
	return `{"version":` + strconv.Quote(version) +
		`,"commit":` + strconv.Quote(Commit) +
		`,"date":` + strconv.Quote(Date) + `}`
}

// Execute runs the root command.
func Execute() error {
	noColor := slices.Contains(os.Args[1:], "--no-color")
	applyColorProfile(noColor)

	// Intercept help flags before cobra sees them (since flag parsing is disabled on root)
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "help", "-h", "--help":
			rootCmd.DisableFlagParsing = false
			rootCmd.SetArgs([]string{"--help"})
		case "version", "--version", "-v":
			hasJSON := slices.Contains(os.Args[2:], "--json")
			if !hasJSON {
				fmt.Println(renderVersion(Version))
				os.Exit(0)
			}
			if os.Args[1] != "version" {
				// --version / -v are not cobra subcommands; handle --json here.
				fmt.Println(renderVersionJSON(Version))
				os.Exit(0)
			}
			// `version --json`: fall through to cobra so the flag is parsed.
		case "--dump-dockerfile":
			if err := dumpDockerfile(); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			os.Exit(0)
		}
	}
	return rootCmd.Execute()
}

// dumpDockerfile writes the base Dockerfile and embedded scripts to a directory.
// Used by CI to generate the Docker build context for pre-built base images.
// If an argument is provided after --dump-dockerfile, it's used as the output
// directory; otherwise a temp directory is created.
func dumpDockerfile() error {
	var outDir string
	if len(os.Args) > 2 {
		outDir = os.Args[2]
	} else {
		var err error
		outDir, err = os.MkdirTemp("", "claude-bunker-dockerfile-*")
		if err != nil {
			return fmt.Errorf("creating temp dir: %w", err)
		}
	}

	if err := container.WriteBuildContext(outDir); err != nil {
		return err
	}

	fmt.Println(outDir)
	return nil
}
