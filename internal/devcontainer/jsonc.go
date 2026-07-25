package devcontainer

import (
	"regexp"
	"strings"
)

// preprocess turns JSONC (JSON with // and /* */ comments and trailing commas)
// plus ${localEnv:VAR} references into strict JSON. Comment stripping is
// string-literal-aware: // and /* inside a JSON string are preserved.
func preprocess(data []byte, localEnv func(name string) (string, bool)) []byte {
	stripped := stripComments(string(data))
	stripped = trailingCommaRe.ReplaceAllString(stripped, "$1")
	stripped = substituteLocalEnv(stripped, localEnv)
	return []byte(stripped)
}

// trailingCommaRe matches a comma followed by optional whitespace and a closing
// brace/bracket. The captured group ($1) is the closer, so the comma is dropped.
var trailingCommaRe = regexp.MustCompile(`,(\s*[}\]])`)

// stripComments removes // line comments and /* */ block comments, while leaving
// any such sequence that appears inside a JSON string literal untouched.
func stripComments(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inString := false
	escaped := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inString {
			b.WriteByte(c)
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false
			}
			continue
		}
		switch {
		case c == '"':
			inString = true
			b.WriteByte(c)
		case c == '/' && i+1 < len(s) && s[i+1] == '/':
			for i < len(s) && s[i] != '\n' {
				i++
			}
			if i < len(s) {
				b.WriteByte('\n')
			}
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			i += 2
			for i+1 < len(s) && !(s[i] == '*' && s[i+1] == '/') {
				i++
			}
			i++ // land on the '/'; loop's i++ moves past it
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// localEnvRe matches ${localEnv:VAR} and ${localEnv:VAR:-default}.
var localEnvRe = regexp.MustCompile(`\$\{localEnv:([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}`)

func substituteLocalEnv(s string, localEnv func(string) (string, bool)) string {
	return localEnvRe.ReplaceAllStringFunc(s, func(match string) string {
		m := localEnvRe.FindStringSubmatch(match)
		name, def := m[1], m[2]
		if localEnv != nil {
			if v, ok := localEnv(name); ok {
				return v
			}
		}
		return def // empty when no default was given
	})
}
