package cmd

import (
	"fmt"
	"os"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/Devon-White/claude-bunker/internal/container"
)

// Version is set via ldflags at build time.
var Version = "dev"

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
	versionCmd.Flags().Bool("json", false, "Output as JSON")
	statusCmd.Flags().Bool("json", false, "Output as JSON")

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
	return `{"version":` + strconv.Quote(version) + `}`
}

// Execute runs the root command.
func Execute() error {
	noColor := false
	for _, a := range os.Args[1:] {
		if a == "--no-color" {
			noColor = true
			break
		}
	}
	applyColorProfile(noColor)

	// Intercept help flags before cobra sees them (since flag parsing is disabled on root)
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "help", "-h", "--help":
			rootCmd.DisableFlagParsing = false
			rootCmd.SetArgs([]string{"--help"})
		case "version", "--version", "-v":
			hasJSON := false
			for _, a := range os.Args[2:] {
				if a == "--json" {
					hasJSON = true
					break
				}
			}
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
