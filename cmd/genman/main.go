package main

import (
	"os"

	"github.com/spf13/cobra/doc"

	"github.com/Devon-White/claude-bunker/cmd"
)

func main() {
	out := "manpages"
	if len(os.Args) > 1 {
		out = os.Args[1]
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		panic(err)
	}
	hdr := &doc.GenManHeader{Title: "CLAUDE-BUNKER", Section: "1"}
	if err := doc.GenManTree(cmd.RootCmd(), hdr, out); err != nil {
		panic(err)
	}
}
