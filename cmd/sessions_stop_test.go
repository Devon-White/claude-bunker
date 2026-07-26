package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"

	"github.com/Devon-White/claude-bunker/internal/sessions"
)

// noMutateDocker satisfies sessions.DockerClient via the embedded (nil) interface,
// overriding only the mutating calls to fail the test if they are ever reached.
type noMutateDocker struct {
	sessions.DockerClient
	t *testing.T
}

func (d noMutateDocker) ContainerStop(context.Context, string, container.StopOptions) error {
	d.t.Fatal("dry-run must not call ContainerStop")
	return nil
}

func (d noMutateDocker) ContainerRemove(context.Context, string, container.RemoveOptions) error {
	d.t.Fatal("dry-run must not call ContainerRemove")
	return nil
}

func TestStopOrPlan_DryRunPlansWithoutDockerCalls(t *testing.T) {
	var buf bytes.Buffer
	origErr := errW
	errW = &buf
	t.Cleanup(func() { errW = origErr })

	origDry := dryRun
	dryRun = true
	t.Cleanup(func() { dryRun = origDry })

	mgr := sessions.NewManager(noMutateDocker{t: t})
	c := sessions.ContainerState{ID: "abc123", DisplayName: "proj-a", Status: "running"}

	if err := stopOrPlan(context.Background(), mgr, c, false /*force*/, true /*remove*/); err != nil {
		t.Fatalf("stopOrPlan: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"would stop container proj-a", "would remove container proj-a"} {
		if !strings.Contains(out, want) {
			t.Errorf("plan output missing %q; got %q", want, out)
		}
	}
}
