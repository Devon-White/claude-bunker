package container

import "embed"

//go:embed scripts/*
var embeddedScripts embed.FS

// InitFirewallScript returns the embedded init-firewall.sh content.
func InitFirewallScript() []byte {
	data, err := embeddedScripts.ReadFile("scripts/init-firewall.sh")
	if err != nil {
		panic("embedded init-firewall.sh missing: " + err.Error())
	}
	return data
}

// TmuxConf returns the embedded tmux.conf content.
func TmuxConf() []byte {
	data, err := embeddedScripts.ReadFile("scripts/tmux.conf")
	if err != nil {
		panic("embedded tmux.conf missing: " + err.Error())
	}
	return data
}
