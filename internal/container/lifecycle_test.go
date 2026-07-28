package container

import (
	"context"
	"errors"
	"strings"
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

// TestCreateAuthWrapperExportsWorldReadableCAPath guards against
// NODE_EXTRA_CA_CERTS pointing at ProxyCACertPath — the proxy-owned copy of
// the CA under a 0700 dir (chown'd to bunker-proxy, uid 1001) that the agent
// (uid 1000) cannot even traverse, let alone read. It must point at
// ProxyCASystemPath, the world-readable copy init-firewall.sh installs via
// update-ca-certificates. Regressing this breaks every terminated
// api.anthropic.com request with an unknown-CA error.
func TestCreateAuthWrapperExportsWorldReadableCAPath(t *testing.T) {
	auth := AuthTokens{ApiKey: "sk-ant-REAL"}

	t.Run("masking active exports the system CA path", func(t *testing.T) {
		var script strings.Builder
		createAuthWrapper(&script, ContainerUserGroup, auth, true)
		got := script.String()
		if !strings.Contains(got, "NODE_EXTRA_CA_CERTS=\""+ProxyCASystemPath+"\"") {
			t.Errorf("wrapper script does not export NODE_EXTRA_CA_CERTS=%q:\n%s", ProxyCASystemPath, got)
		}
		if strings.Contains(got, ProxyCACertPath) {
			t.Errorf("wrapper script must not reference the proxy-owned (agent-unreadable) CA path %q:\n%s", ProxyCACertPath, got)
		}
	})

	t.Run("masking inactive exports no CA path at all", func(t *testing.T) {
		var script strings.Builder
		createAuthWrapper(&script, ContainerUserGroup, auth, false)
		got := script.String()
		if strings.Contains(got, "NODE_EXTRA_CA_CERTS") {
			t.Errorf("wrapper script must not export NODE_EXTRA_CA_CERTS when masking is inactive:\n%s", got)
		}
	})
}
