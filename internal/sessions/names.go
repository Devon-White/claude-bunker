package sessions

// nameStore manages persistent custom display names for containers.
// Names are stored in ~/.claude/session-names.json, keyed by container ID.
var nameStore = newJSONMapStore("session-names.json")

// GetCustomName returns the custom display name for a container, or empty string if none set.
func GetCustomName(containerID string) string {
	return nameStore.Get(containerID)
}

// SetCustomName sets a custom display name for a container.
func SetCustomName(containerID, name string) error {
	return nameStore.Set(containerID, name)
}

// PruneStaleNames removes entries for container IDs not in the given set.
func PruneStaleNames(activeIDs map[string]bool) {
	nameStore.Prune(func(key string) bool {
		return activeIDs[key]
	})
}
