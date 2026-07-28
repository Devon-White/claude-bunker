package container

import (
	"strings"
	"text/template"
)

// BaseImageRegistry is the GHCR registry for pre-built base images.
const BaseImageRegistry = "ghcr.io/devon-white/claude-bunker"

// baseTemplateData holds the values injected into base.dockerfile.tmpl.
type baseTemplateData struct {
	User                string
	Home                string
	Workspace           string
	HistoryDir          string
	ManagedSettingsDir  string
	CommonFirewallPath  string
	FirewallPath        string
	RefreshFirewallPath string
	AllowedDomainsPath  string
}

// baseTmpl is parsed once at package init from the embedded template.
var baseTmpl = template.Must(
	template.New("base.dockerfile.tmpl").Parse(string(mustReadEmbedded("base.dockerfile.tmpl"))),
)

// BaseImageRef returns the full image reference for a given version.
// Returns "" for dev builds (no pre-built image available).
func BaseImageRef(version string) string {
	if version == "" || version == "dev" {
		return ""
	}
	return BaseImageRegistry + ":v" + version
}

// GenerateBaseDockerfile produces the complete base Dockerfile as a string,
// ending with USER claude-bunker. Used for fingerprinting and standalone builds.
func GenerateBaseDockerfile() string {
	return generateBaseContent() + "USER " + ContainerUser + "\n\n"
}

// generateBaseContent produces the Dockerfile content without the final USER
// line. Used by GenerateBaseDockerfile (which appends USER) and by buildLocal
// when merging with dynamic layers (GenerateDockerfile appends USER).
func generateBaseContent() string {
	data := baseTemplateData{
		User:                ContainerUser,
		Home:                ContainerHome,
		Workspace:           ContainerWorkspace,
		HistoryDir:          CommandHistoryDir,
		ManagedSettingsDir:  ManagedSettingsDir,
		CommonFirewallPath:  CommonFirewallScriptPath,
		FirewallPath:        FirewallScriptPath,
		RefreshFirewallPath: RefreshFirewallScriptPath,
		AllowedDomainsPath:  AllowedDomainsPath,
	}

	var b strings.Builder
	if err := baseTmpl.Execute(&b, data); err != nil {
		panic("executing base dockerfile template: " + err.Error())
	}
	return b.String()
}
