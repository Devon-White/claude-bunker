package container

import (
	"testing"
)

func TestSortFeatures_Alphabetical(t *testing.T) {
	features := []ResolvedFeature{
		{ID: "python", Source: "ghcr.io/features/python:1"},
		{ID: "node", Source: "ghcr.io/features/node:1"},
		{ID: "go", Source: "ghcr.io/features/go:1"},
	}

	sortFeatures(features)

	want := []string{"go", "node", "python"}
	for i, f := range features {
		if f.ID != want[i] {
			t.Errorf("position %d: got %q, want %q", i, f.ID, want[i])
		}
	}
}

func TestSortFeatures_WithDependencies(t *testing.T) {
	nodeRef := "ghcr.io/features/node:1"

	features := []ResolvedFeature{
		{ID: "python", Source: "ghcr.io/features/python:1", InstallsAfter: []string{nodeRef}},
		{ID: "node", Source: nodeRef},
		{ID: "go", Source: "ghcr.io/features/go:1"},
	}

	sortFeatures(features)

	// node must come before python (dependency), go sorts alphabetically
	nodeIdx := indexOf(features, "node")
	pythonIdx := indexOf(features, "python")
	if nodeIdx > pythonIdx {
		t.Errorf("node (idx %d) should come before python (idx %d)", nodeIdx, pythonIdx)
	}
}

func TestSortFeatures_Empty(t *testing.T) {
	features := []ResolvedFeature{}
	sortFeatures(features) // should not panic
}

func TestSortFeatures_Single(t *testing.T) {
	features := []ResolvedFeature{
		{ID: "python", Source: "ghcr.io/features/python:1"},
	}
	sortFeatures(features)
	if features[0].ID != "python" {
		t.Errorf("single feature should remain: got %q", features[0].ID)
	}
}

func indexOf(features []ResolvedFeature, id string) int {
	for i, f := range features {
		if f.ID == id {
			return i
		}
	}
	return -1
}
