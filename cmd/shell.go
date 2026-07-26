package cmd

import (
	"github.com/spf13/cobra"
)

var shellCmd = &cobra.Command{
	Use:   "shell",
	Short: "Open a shell in the sandbox",
	Long:  "Opens an interactive bash shell inside the sandbox container for debugging or manual work.",
	RunE: func(cmd *cobra.Command, args []string) error {
		// shell has DisableFlagParsing:false, so cobra parses its flags and
		// consumes any bare `--` before runInSandbox/extractBunkerFlags runs;
		// the `--` passthrough terminator is a default-`claude`-run affordance.
		initVerbosity(cmd)
		return runInSandbox(args, "bash")
	},
}
