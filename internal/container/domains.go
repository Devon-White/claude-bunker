package container

// builtinDomains is the canonical list of domains allowed through the
// firewall and the Claude Code sandbox. Both init-firewall.sh (IP-level)
// and managed-settings.json (domain-level) are derived from this list.
//
// The firewall resolves each domain to IPs via DNS at container start.
// The sandbox filter matches by domain name at request time and also
// supports wildcards (e.g. *.github.com).
var builtinDomains = []string{
	"github.com",
	"api.github.com",
	"api.anthropic.com",
	"sentry.io",
	"statsig.anthropic.com",
	"statsig.com",
	"marketplace.visualstudio.com",
	"vscode.blob.core.windows.net",
	"update.code.visualstudio.com",
}

// sandboxExtraDomains are additional domains included only in the
// managed-settings.json sandbox filter (which supports wildcards) but
// not in the iptables firewall (which resolves individual domains to IPs).
var sandboxExtraDomains = []string{
	"*.github.com",
}

// BuiltinDomains returns a copy of the canonical domain list.
func BuiltinDomains() []string {
	out := make([]string, len(builtinDomains))
	copy(out, builtinDomains)
	return out
}

// SandboxExtraDomains returns a copy of the sandbox-only domain list.
func SandboxExtraDomains() []string {
	out := make([]string, len(sandboxExtraDomains))
	copy(out, sandboxExtraDomains)
	return out
}

