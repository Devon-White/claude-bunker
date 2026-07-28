package container

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

//go:embed scripts/*
var embeddedScripts embed.FS

//go:embed egressproxy/*.go
var egressProxySrc embed.FS

// synthEgressGoMod is the standalone module file shipped into the build context
// so the multi-stage builder compiles the stdlib-only proxy offline.
const synthEgressGoMod = "module egressproxyd\n\ngo 1.23\n"

// EgressProxySources returns the proxy Go source (test files excluded) plus a
// synthetic go.mod, all rooted at egressproxy/ in the build context.
func EgressProxySources() []BuildContextFile {
	entries, err := egressProxySrc.ReadDir("egressproxy")
	if err != nil {
		panic("reading embedded egressproxy: " + err.Error())
	}
	var out []BuildContextFile
	for _, e := range entries {
		name := e.Name()
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := egressProxySrc.ReadFile("egressproxy/" + name)
		if err != nil {
			panic("reading embedded egressproxy/" + name + ": " + err.Error())
		}
		out = append(out, BuildContextFile{Name: "egressproxy/" + name, Content: data, Mode: 0644})
	}
	out = append(out, BuildContextFile{Name: "egressproxy/go.mod", Content: []byte(synthEgressGoMod), Mode: 0644})
	return out
}

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

// WriteBuildContext writes the base Dockerfile and all embedded scripts to
// outDir. Used by --dump-dockerfile and cmd/genbuild.
func WriteBuildContext(outDir string) error {
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return fmt.Errorf("creating output dir: %w", err)
	}

	if err := os.WriteFile(filepath.Join(outDir, "Dockerfile"), []byte(GenerateBaseDockerfile()), 0644); err != nil {
		return fmt.Errorf("writing Dockerfile: %w", err)
	}

	for _, f := range BuildContextScripts() {
		if err := os.WriteFile(filepath.Join(outDir, f.Name), f.Content, f.Mode); err != nil {
			return fmt.Errorf("writing %s: %w", f.Name, err)
		}
	}

	for _, f := range EgressProxySources() {
		full := filepath.Join(outDir, filepath.FromSlash(f.Name))
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			return fmt.Errorf("creating dir for %s: %w", f.Name, err)
		}
		if err := os.WriteFile(full, f.Content, f.Mode); err != nil {
			return fmt.Errorf("writing %s: %w", f.Name, err)
		}
	}

	return nil
}
