package main

import (
	"fmt"
	"os"

	"github.com/Devon-White/claude-bunker/internal/container"
)

func main() {
	dir := ".bunker-build"
	if err := os.MkdirAll(dir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "error: mkdir %s: %v\n", dir, err)
		os.Exit(1)
	}

	if err := os.WriteFile(dir+"/Dockerfile", []byte(container.GenerateBaseDockerfile()), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "error: writing Dockerfile: %v\n", err)
		os.Exit(1)
	}
	for _, f := range container.BuildContextScripts() {
		if err := os.WriteFile(dir+"/"+f.Name, f.Content, f.Mode); err != nil {
			fmt.Fprintf(os.Stderr, "error: writing %s: %v\n", f.Name, err)
			os.Exit(1)
		}
	}

	fmt.Println("Build context generated in", dir)
}
