package main

import (
	"os"

	"github.com/Devon-White/claude-bunker/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		cmd.PrintError(os.Stderr, err)
		os.Exit(cmd.ExitCodeFor(err))
	}
}
