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
	stripped = stripTrailingCommas(stripped)
	stripped = substituteLocalEnv(stripped, localEnv)
	return []byte(stripped)
}

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

// stripTrailingCommas removes a comma that is immediately followed (after
// optional whitespace) by a closing } or ]. It is string-literal-aware: a comma
// inside a "..." string is never touched. Runs after stripComments, so the input
// is already comment-free.
func stripTrailingCommas(s string) string {
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
		if c == '"' {
			inString = true
			b.WriteByte(c)
			continue
		}
		if c == ',' {
			j := i + 1
			for j < len(s) && (s[j] == ' ' || s[j] == '\t' || s[j] == '\n' || s[j] == '\r') {
				j++
			}
			if j < len(s) && (s[j] == '}' || s[j] == ']') {
				continue // trailing comma: skip it (the following whitespace + bracket are written normally)
			}
		}
		b.WriteByte(c)
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
