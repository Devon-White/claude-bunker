package main

import (
	"bufio"
	"os"
	"strings"
)

// Allowlist matches SNI hostnames against the allowed set. Entries are plain
// hostnames (exact match) or "*.suffix" wildcards (match any single-or-more
// label prefix of ".suffix"). A leading "!" (the firewall's critical marker)
// and blank/comment lines are ignored.
type Allowlist struct {
	exact    map[string]struct{}
	suffixes []string // e.g. ".githubusercontent.com"
}

// LoadAllowlist parses the domains file (the same file the ipset is built from).
func LoadAllowlist(path string) (*Allowlist, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	al := &Allowlist{exact: make(map[string]struct{})}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		line = strings.TrimPrefix(line, "!")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.ToLower(line)
		if strings.HasPrefix(line, "*.") {
			al.suffixes = append(al.suffixes, line[1:]) // ".githubusercontent.com"
			continue
		}
		al.exact[line] = struct{}{}
	}
	return al, sc.Err()
}

// Allowed reports whether an SNI host is permitted.
func (a *Allowlist) Allowed(host string) bool {
	if host == "" {
		return false
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if _, ok := a.exact[host]; ok {
		return true
	}
	for _, s := range a.suffixes {
		// "*.githubusercontent.com" => host must end with ".githubusercontent.com"
		// and have at least one label before it.
		if strings.HasSuffix(host, s) && len(host) > len(s) {
			return true
		}
	}
	return false
}
