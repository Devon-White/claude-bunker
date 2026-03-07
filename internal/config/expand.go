package config

import (
	"os"
	"strings"
)

// expandEnvVars expands shell-style variable references in s.
// Supported syntax: $VAR, ${VAR}, ${VAR:-default}, $$ (literal $).
// Unresolved or empty variables expand to "" (or the default if provided).
// Unterminated ${ and bare $ at string end are emitted literally.
func expandEnvVars(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		if s[i] != '$' {
			b.WriteByte(s[i])
			i++
			continue
		}
		// '$' found
		if i+1 >= len(s) {
			// bare $ at end
			b.WriteByte('$')
			i++
			continue
		}
		next := s[i+1]
		// $$ → literal $
		if next == '$' {
			b.WriteByte('$')
			i += 2
			continue
		}
		// ${...} form
		if next == '{' {
			end := strings.IndexByte(s[i+2:], '}')
			if end < 0 {
				// unterminated — emit literally
				b.WriteString(s[i:])
				return b.String()
			}
			expr := s[i+2 : i+2+end]
			name, def, hasDef := parseDefault(expr)
			val := os.Getenv(name)
			if val == "" && hasDef {
				val = def
			}
			b.WriteString(val)
			i = i + 2 + end + 1
			continue
		}
		// $NAME form — name is [A-Za-z_][A-Za-z0-9_]*
		if isNameStart(next) {
			j := i + 2
			for j < len(s) && isNameCont(s[j]) {
				j++
			}
			name := s[i+1 : j]
			b.WriteString(os.Getenv(name))
			i = j
			continue
		}
		// $<other> (e.g. $1) — emit literally
		b.WriteByte('$')
		i++
	}
	return b.String()
}

// parseDefault splits "VAR:-default" into (VAR, default, true).
// If no ":-" separator, returns (expr, "", false).
func parseDefault(expr string) (name, def string, hasDef bool) {
	if before, after, ok := strings.Cut(expr, ":-"); ok {
		return before, after, true
	}
	return expr, "", false
}

func isNameStart(c byte) bool {
	return c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

func isNameCont(c byte) bool {
	return isNameStart(c) || (c >= '0' && c <= '9')
}

// expandProjectConfig applies env var expansion to all user-facing string
// fields in cfg. Called at load time so downstream consumers receive resolved
// values without any changes.
func expandProjectConfig(cfg *ProjectConfig) {
	cfg.Workspace = expandEnvVars(cfg.Workspace)
	cfg.PostStartCommand = expandEnvVars(cfg.PostStartCommand)
	cfg.GhToken = expandEnvVars(cfg.GhToken)
	cfg.Plugins = expandEnvVars(cfg.Plugins)

	for i, v := range cfg.Exclude {
		cfg.Exclude[i] = expandEnvVars(v)
	}
	for i, v := range cfg.AllowDomains {
		cfg.AllowDomains[i] = expandEnvVars(v)
	}
	for i, v := range cfg.Apt {
		cfg.Apt[i] = expandEnvVars(v)
	}
	for k, v := range cfg.Env {
		cfg.Env[k] = expandEnvVars(v)
	}
	for _, opts := range cfg.Features {
		for k, v := range opts {
			if s, ok := v.(string); ok {
				opts[k] = expandEnvVars(s)
			}
		}
	}
}

