package main

import (
	"fmt"
	"os"

	"github.com/Devon-White/claude-bunker/internal/container"
)

func main() {
	dir := ".bunker-build"
	if err := container.WriteBuildContext(dir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Build context generated in", dir)
}
