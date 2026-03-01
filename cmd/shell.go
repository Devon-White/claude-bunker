package cmd

import (
	"github.com/spf13/cobra"
)

var shellCmd = &cobra.Command{
	Use:   "shell",
	Short: "Open a shell in the sandbox",
	Long:  "Opens an interactive zsh shell inside the sandbox container for debugging or manual work.",
	RunE: func(cmd *cobra.Command, args []string) error {
		initVerbosity(cmd)
		return runInSandbox(args, "zsh")
	},
}
