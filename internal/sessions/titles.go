package sessions

// Session titles set FROM claude-bunker. Claude Code has no CLI verb to rename a
// running session, so a bunker-side rename is stored here and shown as a fallback/
// override; Claude's own `name` (from `claude agents --json`) wins when present.
//
// Stored at <store dir>/session-titles.json, keyed by "containerID:sessionID".

var titleStore = newJSONMapStore("session-titles.json")

func titleKey(containerID, sessionID string) string { return containerID + ":" + sessionID }

// SetSessionTitle records a bunker-set title for a session.
func SetSessionTitle(containerID, sessionID, title string) error {
	return titleStore.Set(titleKey(containerID, sessionID), title)
}

// GetSessionTitle returns the bunker-set title for a session, or "" if none.
func GetSessionTitle(containerID, sessionID string) string {
	return titleStore.Get(titleKey(containerID, sessionID))
}
