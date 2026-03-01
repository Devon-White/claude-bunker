package main

import (
	"fmt"
	"os"

	"github.com/Devon-White/claude-bunker/internal/container"
)

func main() {
	dir := ".bunker-build"
	os.MkdirAll(dir, 0755)

	os.WriteFile(dir+"/Dockerfile", []byte(container.GenerateBaseDockerfile()), 0644)
	os.WriteFile(dir+"/init-firewall.sh", container.InitFirewallScript(), 0755)
	os.WriteFile(dir+"/tmux.conf", container.TmuxConf(), 0644)

	fmt.Println("Build context generated in", dir)
}
