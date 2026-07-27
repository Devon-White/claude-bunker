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
	"codeload.github.com",
	"objects.githubusercontent.com",
	"api.anthropic.com",
	"sentry.io",
	"statsig.anthropic.com",
	"statsig.com",
	"marketplace.visualstudio.com",
	"vscode.blob.core.windows.net",
	"update.code.visualstudio.com",
}

// sandboxExtraDomains are additional domains included only in the
// managed-settings.json sandbox filter (sandbox-level), NOT added to the
// iptables firewall domain file.
//
// This asymmetry is intentional:
//   - The sandbox uses pattern matching (e.g. *.github.com matches any subdomain)
//     and can handle wildcard patterns directly.
//   - The firewall resolves each domain to IP addresses via DNS at container start.
//     Wildcard patterns cannot be IP-resolved, so specific subdomains (e.g.
//     api.github.com, codeload.github.com) must be listed individually in
//     builtinDomains for firewall coverage.
//
// To allow a new service: add specific subdomains to builtinDomains for firewall
// access, and optionally add a wildcard here if the sandbox should permit the
// entire domain family.
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
// These domains are deliberately excluded from the firewall domain file
// because they are wildcard patterns that cannot be IP-resolved. They are
// only used in managed-settings.json where the sandbox performs domain-name
// pattern matching rather than IP-based filtering.
func SandboxExtraDomains() []string {
	out := make([]string, len(sandboxExtraDomains))
	copy(out, sandboxExtraDomains)
	return out
}
