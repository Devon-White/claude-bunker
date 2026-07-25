package devcontainer

import (
	"slices"
	"testing"
)

func TestIsBunkerGenerated(t *testing.T) {
	if !IsBunkerGenerated([]byte(GeneratedMarker + "\n{}")) {
		t.Error("marker file should be detected as bunker-generated")
	}
	if !IsBunkerGenerated([]byte("\n\n" + GeneratedMarker + "\n{}")) {
		t.Error("leading blank lines before the marker should still count")
	}
	if IsBunkerGenerated([]byte("{\n  \"name\": \"x\"\n}")) {
		t.Error("a user file must not be detected as bunker-generated")
	}
	if IsBunkerGenerated([]byte("// some other comment\n{}")) {
		t.Error("a different first-line comment is not the marker")
	}
}

func TestMerge_ForcesSecurityFields(t *testing.T) {
	user := DevContainer{
		Image:      "my/image",
		CapAdd:     []string{"SYS_PTRACE"},
		RemoteUser: "vscode",
	}
	got := Merge(user)

	if got.Image != "my/image" {
		t.Errorf("user image must be preserved: %q", got.Image)
	}
	if got.RemoteUser != "claude-bunker" {
		t.Errorf("remoteUser must be forced: %q", got.RemoteUser)
	}
	if !slices.Contains(got.CapAdd, "NET_ADMIN") || !slices.Contains(got.CapAdd, "NET_RAW") {
		t.Errorf("NET_ADMIN/NET_RAW must be unioned in: %+v", got.CapAdd)
	}
	if !slices.Contains(got.CapAdd, "SYS_PTRACE") {
		t.Errorf("user's cap must be preserved: %+v", got.CapAdd)
	}
	// No duplicates when the user already listed a forced cap.
	user2 := DevContainer{CapAdd: []string{"NET_ADMIN"}}
	got2 := Merge(user2)
	n := 0
	for _, c := range got2.CapAdd {
		if c == "NET_ADMIN" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("NET_ADMIN must not be duplicated: %+v", got2.CapAdd)
	}
}
