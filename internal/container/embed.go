package container

import (
	"embed"
	"io/fs"
)

//go:embed scripts/*
var embeddedScripts embed.FS

// mustReadEmbedded reads a file from the embedded scripts FS or panics.
func mustReadEmbedded(name string) []byte {
	data, err := embeddedScripts.ReadFile("scripts/" + name)
	if err != nil {
		panic("embedded " + name + " missing: " + err.Error())
	}
	return data
}

// Cached embedded script content — read once at package init, reused thereafter.
var (
	commonFirewallScript  = mustReadEmbedded("firewall-common.sh")
	initFirewallScript    = mustReadEmbedded("init-firewall.sh")
	refreshFirewallScript = mustReadEmbedded("refresh-firewall.sh")
	tmuxConf              = mustReadEmbedded("tmux.conf")
)

// copyScript returns a mutable copy of a cached script.
func copyScript(src []byte) []byte { return append([]byte(nil), src...) }

// BuildContextFile describes an embedded file that is part of the Docker build
// context. This is the single source of truth — buildContextTar, dumpDockerfile,
// genbuild, and fingerprinting all derive from this list.
type BuildContextFile struct {
	Name    string
	Content []byte
	Mode    fs.FileMode
}

// BuildContextScripts returns all embedded scripts and configs included in the
// Docker build context. Adding a new embedded file here automatically propagates
// to the tar archive, file dump, standalone genbuild tool, and fingerprinting.
func BuildContextScripts() []BuildContextFile {
	return []BuildContextFile{
		{"firewall-common.sh", copyScript(commonFirewallScript), 0755},
		{"init-firewall.sh", copyScript(initFirewallScript), 0755},
		{"refresh-firewall.sh", copyScript(refreshFirewallScript), 0755},
		{"tmux.conf", copyScript(tmuxConf), 0644},
	}
}
