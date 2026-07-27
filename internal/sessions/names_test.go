package sessions

import (
	"context"
	"errors"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"

	ctr "github.com/Devon-White/claude-bunker/internal/container"
)

// TestPruneStaleNames is a direct regression guard for the §10 bug: the exact
// function (names.go PruneStaleNames -> jsonMapStore.Prune) that, before the
// STORE_DIR isolation fix, could wipe the developer's real ~/.claude custom
// names. It must keep entries whose ID is in the active set and drop the rest.
func TestPruneStaleNames(t *testing.T) {
	ids := []string{"prune-a", "prune-b", "prune-c"}
	for _, id := range ids {
		if err := SetCustomName(id, "name-"+id); err != nil {
			t.Fatalf("SetCustomName(%q) failed: %v", id, err)
		}
	}

	PruneStaleNames(map[string]bool{"prune-a": true, "prune-c": true})

	if got := GetCustomName("prune-a"); got != "name-prune-a" {
		t.Errorf("prune-a: want survive %q, got %q", "name-prune-a", got)
	}
	if got := GetCustomName("prune-c"); got != "name-prune-c" {
		t.Errorf("prune-c: want survive %q, got %q", "name-prune-c", got)
	}
	if got := GetCustomName("prune-b"); got != "" {
		t.Errorf("prune-b: want pruned (empty), got %q", got)
	}
}

// TestCustomName_RoundTrip covers SetCustomName/GetCustomName including the
// empty-value delete semantics of jsonMapStore.Set (store.go:94).
func TestCustomName_RoundTrip(t *testing.T) {
	const id = "rt-1"

	if got := GetCustomName(id); got != "" {
		t.Fatalf("want empty before set, got %q (store contaminated?)", got)
	}

	if err := SetCustomName(id, "custom-display"); err != nil {
		t.Fatalf("SetCustomName failed: %v", err)
	}
	if got := GetCustomName(id); got != "custom-display" {
		t.Errorf("after set: want %q, got %q", "custom-display", got)
	}

	// Empty value deletes the entry.
	if err := SetCustomName(id, ""); err != nil {
		t.Fatalf("SetCustomName(delete) failed: %v", err)
	}
	if got := GetCustomName(id); got != "" {
		t.Errorf("after delete: want empty, got %q", got)
	}
}

// TestRenameContainer asserts RenameContainer both persists the custom name and
// drives the Docker rename with matching args — the container-rename "direction"
// of the spec's rename coverage.
func TestRenameContainer(t *testing.T) {
	const id = "rename-1"
	cli := &mockClient{}
	mgr := NewManager(cli)

	if err := mgr.RenameContainer(context.Background(), id, "new-name"); err != nil {
		t.Fatalf("RenameContainer failed: %v", err)
	}

	if got := GetCustomName(id); got != "new-name" {
		t.Errorf("custom name: want %q, got %q", "new-name", got)
	}
	if !cli.renameCalled {
		t.Error("expected ContainerRename to be called")
	}
	if cli.renamedID != id || cli.renamedName != "new-name" {
		t.Errorf("ContainerRename args: got (%q, %q), want (%q, %q)",
			cli.renamedID, cli.renamedName, id, "new-name")
	}
}

// TestRenameContainer_DockerErrorSwallowed verifies manager.go:442: a failing
// Docker rename is ignored, yet the custom display name still persists.
func TestRenameContainer_DockerErrorSwallowed(t *testing.T) {
	const id = "rename-2"
	cli := &mockClient{renameErr: errors.New("name conflict")}
	mgr := NewManager(cli)

	if err := mgr.RenameContainer(context.Background(), id, "renamed"); err != nil {
		t.Fatalf("RenameContainer should swallow the Docker error, got: %v", err)
	}
	if got := GetCustomName(id); got != "renamed" {
		t.Errorf("custom name must persist despite Docker error: got %q", got)
	}
	if !cli.renameCalled {
		t.Error("expected ContainerRename to be attempted")
	}
}

// TestResolveContainer_CustomNameMatch seeds a custom name and asserts it both
// drives DisplayName (FetchSnapshot, manager.go:81) and satisfies the exact
// match in ResolveContainer (manager.go:272).
func TestResolveContainer_CustomNameMatch(t *testing.T) {
	orig := execAgentsJSON
	defer func() { execAgentsJSON = orig }()
	execAgentsJSON = func(_ context.Context, _ *client.Client, _ string) (string, error) {
		return "[]", nil
	}

	const cid = "resolve-cid"
	if err := SetCustomName(cid, "my-favorite"); err != nil {
		t.Fatalf("SetCustomName failed: %v", err)
	}

	cli := &mockClient{
		containers: []container.Summary{
			{ID: cid, State: "running", Labels: map[string]string{ctr.LabelKey: "project-a1b2c3d4"}},
		},
		inspect: map[string]container.InspectResponse{
			cid: {ContainerJSONBase: &container.ContainerJSONBase{State: &container.State{}}},
		},
		top: map[string]container.TopResponse{
			cid: {Titles: []string{"PID", "COMMAND"}, Processes: [][]string{}},
		},
	}

	mgr := NewManager(cli)
	c, err := mgr.ResolveContainer(context.Background(), "my-favorite")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.ID != cid {
		t.Errorf("expected ID %q, got %q", cid, c.ID)
	}
	if c.DisplayName != "my-favorite" {
		t.Errorf("expected DisplayName to reflect custom name, got %q", c.DisplayName)
	}
}
