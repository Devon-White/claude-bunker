package container

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

func TestSortFeatures_TransitiveDependencies(t *testing.T) {
	// Chain: A must come before B, B must come before C.
	// Even though A and C have no direct edge, A must still precede C.
	refA := "ghcr.io/features/alpha:1"
	refB := "ghcr.io/features/bravo:1"

	features := []ResolvedFeature{
		{ID: "charlie", Source: "ghcr.io/features/charlie:1", InstallsAfter: []string{refB}},
		{ID: "alpha", Source: refA},
		{ID: "bravo", Source: refB, InstallsAfter: []string{refA}},
	}

	sortFeatures(features)

	alphaIdx := indexOf(features, "alpha")
	bravoIdx := indexOf(features, "bravo")
	charlieIdx := indexOf(features, "charlie")

	if alphaIdx > bravoIdx {
		t.Errorf("alpha (idx %d) should come before bravo (idx %d)", alphaIdx, bravoIdx)
	}
	if bravoIdx > charlieIdx {
		t.Errorf("bravo (idx %d) should come before charlie (idx %d)", bravoIdx, charlieIdx)
	}
	if alphaIdx > charlieIdx {
		t.Errorf("alpha (idx %d) should come before charlie (idx %d) via transitive dependency", alphaIdx, charlieIdx)
	}

	// Verify exact order: alpha, bravo, charlie (topological + alphabetical)
	want := []string{"alpha", "bravo", "charlie"}
	for i, f := range features {
		if f.ID != want[i] {
			t.Errorf("position %d: got %q, want %q", i, f.ID, want[i])
		}
	}
}

func TestSortFeatures_CycleGracefulDegradation(t *testing.T) {
	// A -> B -> A forms a cycle. Both features should still appear in the
	// output (appended alphabetically), and the function must not hang or panic.
	refA := "ghcr.io/features/alpha:1"
	refB := "ghcr.io/features/bravo:1"

	features := []ResolvedFeature{
		{ID: "bravo", Source: refB, InstallsAfter: []string{refA}},
		{ID: "alpha", Source: refA, InstallsAfter: []string{refB}},
	}

	sortFeatures(features)

	if len(features) != 2 {
		t.Fatalf("expected 2 features, got %d", len(features))
	}

	// Both features are in a cycle so neither has in-degree 0.
	// They should be appended alphabetically by the fallback.
	want := []string{"alpha", "bravo"}
	for i, f := range features {
		if f.ID != want[i] {
			t.Errorf("position %d: got %q, want %q", i, f.ID, want[i])
		}
	}
}

func TestSortFeatures_PartialCycle(t *testing.T) {
	// "alpha" has no dependencies (in-degree 0), but "bravo" and "charlie"
	// form a cycle. Alpha should come first, then the cycle members alphabetically.
	refA := "ghcr.io/features/alpha:1"
	refB := "ghcr.io/features/bravo:1"
	refC := "ghcr.io/features/charlie:1"

	features := []ResolvedFeature{
		{ID: "charlie", Source: refC, InstallsAfter: []string{refB}},
		{ID: "bravo", Source: refB, InstallsAfter: []string{refC}},
		{ID: "alpha", Source: refA},
	}

	sortFeatures(features)

	if features[0].ID != "alpha" {
		t.Errorf("expected alpha first, got %q", features[0].ID)
	}
	// bravo and charlie are in a cycle — should follow alpha, sorted alphabetically
	if features[1].ID != "bravo" {
		t.Errorf("expected bravo second, got %q", features[1].ID)
	}
	if features[2].ID != "charlie" {
		t.Errorf("expected charlie third, got %q", features[2].ID)
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

func TestSafeOptionEnvName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"version", "VERSION"},
		{"golangciLintVersion", "GOLANGCILINTVERSION"},
		{"installTools", "INSTALLTOOLS"},
		{"install-path", "INSTALL_PATH"},
		{"nodeGypDependencies", "NODEGYPDEPENDENCIES"},
		{"pnpmVersion", "PNPMVERSION"},
		{"enable.shared", "ENABLE_SHARED"},
	}
	for _, tt := range tests {
		got := safeOptionEnvName(tt.input)
		if got != tt.want {
			t.Errorf("safeOptionEnvName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestWriteFeatureFiles(t *testing.T) {
	dir := t.TempDir()
	opts := map[string]interface{}{
		"version":    "1.22",
		"installFoo": "true",
	}

	if err := writeFeatureFiles(dir, opts); err != nil {
		t.Fatalf("writeFeatureFiles: %v", err)
	}

	// Check env file exists and contains correct variable names
	envData, err := os.ReadFile(filepath.Join(dir, "devcontainer-features.env"))
	if err != nil {
		t.Fatalf("reading env file: %v", err)
	}
	envContent := string(envData)
	if want := `INSTALLFOO='true'`; !strings.Contains(envContent, want) {
		t.Errorf("env file missing %s, got:\n%s", want, envContent)
	}
	if want := `VERSION='1.22'`; !strings.Contains(envContent, want) {
		t.Errorf("env file missing %s, got:\n%s", want, envContent)
	}

	// Check wrapper script exists and is executable
	wrapperPath := filepath.Join(dir, "devcontainer-features-install.sh")
	info, err := os.Stat(wrapperPath)
	if err != nil {
		t.Fatalf("stat wrapper script: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode()&0111 == 0 {
		t.Error("wrapper script should be executable")
	}
	wrapperData, err := os.ReadFile(wrapperPath)
	if err != nil {
		t.Fatalf("reading wrapper: %v", err)
	}
	wrapper := string(wrapperData)
	if !strings.Contains(wrapper, "devcontainer-features.env") {
		t.Error("wrapper should source devcontainer-features.env")
	}
	if !strings.Contains(wrapper, "./install.sh") {
		t.Error("wrapper should run install.sh")
	}
}

func TestWriteFeatureFiles_EmptyOptions(t *testing.T) {
	dir := t.TempDir()
	if err := writeFeatureFiles(dir, nil); err != nil {
		t.Fatalf("writeFeatureFiles with nil opts: %v", err)
	}

	// Env file should exist but be empty
	envData, err := os.ReadFile(filepath.Join(dir, "devcontainer-features.env"))
	if err != nil {
		t.Fatalf("reading env file: %v", err)
	}
	if len(envData) != 0 {
		t.Errorf("expected empty env file for nil opts, got: %s", envData)
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
