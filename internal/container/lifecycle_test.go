package container

import (
	"context"
	"errors"
	"testing"

	"github.com/docker/docker/api/types/container"
)

type fakeInspector struct {
	inspect     container.InspectResponse
	inspectErr  error
	execRunning map[string]bool
	execErr     error
}

func (f *fakeInspector) ContainerInspect(_ context.Context, _ string) (container.InspectResponse, error) {
	return f.inspect, f.inspectErr
}

func (f *fakeInspector) ContainerExecInspect(_ context.Context, execID string) (container.ExecInspect, error) {
	if f.execErr != nil {
		return container.ExecInspect{}, f.execErr
	}
	return container.ExecInspect{ExecID: execID, Running: f.execRunning[execID]}, nil
}

func inspectWithExecs(ids ...string) container.InspectResponse {
	return container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{ExecIDs: ids},
	}
}

func TestHasOtherActiveSessions(t *testing.T) {
	ctx := context.Background()

	t.Run("inspect error returns error", func(t *testing.T) {
		_, err := HasOtherActiveSessions(ctx, &fakeInspector{inspectErr: errors.New("daemon down")}, "cid", "myexec")
		if err == nil {
			t.Fatal("expected error when inspect fails")
		}
	})

	t.Run("no other execs is false", func(t *testing.T) {
		f := &fakeInspector{inspect: inspectWithExecs("myexec")}
		active, err := HasOtherActiveSessions(ctx, f, "cid", "myexec")
		if err != nil || active {
			t.Fatalf("want (false,nil), got (%v,%v)", active, err)
		}
	})

	t.Run("another running exec is true", func(t *testing.T) {
		f := &fakeInspector{
			inspect:     inspectWithExecs("myexec", "other"),
			execRunning: map[string]bool{"other": true},
		}
		active, err := HasOtherActiveSessions(ctx, f, "cid", "myexec")
		if err != nil || !active {
			t.Fatalf("want (true,nil), got (%v,%v)", active, err)
		}
	})

	t.Run("exec inspect error returns error", func(t *testing.T) {
		f := &fakeInspector{inspect: inspectWithExecs("other"), execErr: errors.New("gone")}
		_, err := HasOtherActiveSessions(ctx, f, "cid", "myexec")
		if err == nil {
			t.Fatal("expected error when exec inspect fails")
		}
	})
}
