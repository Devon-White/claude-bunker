package devcontainer

import "strings"

// forcedCaps are the network capabilities bunker's firewall requires.
var forcedCaps = []string{"NET_ADMIN", "NET_RAW"}

// IsBunkerGenerated reports whether the file's first non-blank line is the
// GENERATED marker, i.e. the file is bunker-owned and may be regenerated.
func IsBunkerGenerated(data []byte) bool {
	for _, line := range strings.Split(string(data), "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		return t == GeneratedMarker
	}
	return false
}

// Merge augments a user-authored DevContainer with bunker's non-negotiable
// settings: NET_ADMIN/NET_RAW unioned into capAdd, and remoteUser forced to the
// bunker user. All other user fields are preserved. The input is not mutated.
func Merge(existing DevContainer) DevContainer {
	merged := existing
	merged.RemoteUser = "claude-bunker"

	caps := append([]string{}, existing.CapAdd...)
	for _, forced := range forcedCaps {
		if !contains(caps, forced) {
			caps = append(caps, forced)
		}
	}
	merged.CapAdd = caps
	return merged
}
