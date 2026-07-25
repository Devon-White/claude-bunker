package cmd

import (
	"context"
	"errors"
	"testing"

	"github.com/docker/docker/api/types"
)

type fakePinger struct {
	pingErr error
	ver     string
}

func (f fakePinger) Ping(context.Context) (types.Ping, error) {
	if f.pingErr != nil {
		return types.Ping{}, f.pingErr
	}
	return types.Ping{APIVersion: "1.45"}, nil
}
func (f fakePinger) ServerVersion(context.Context) (types.Version, error) {
	return types.Version{Version: f.ver}, nil
}

func TestRunDoctorChecks(t *testing.T) {
	t.Run("docker reachable → docker check OK", func(t *testing.T) {
		results := runDoctorChecks(context.Background(), fakePinger{ver: "27.0"}, "1.0.0", t.TempDir())
		var dockerOK bool
		for _, r := range results {
			if r.Name == "Docker" {
				dockerOK = r.OK
			}
		}
		if !dockerOK {
			t.Error("Docker check should be OK when ping succeeds")
		}
	})
	t.Run("docker down → docker check fails", func(t *testing.T) {
		results := runDoctorChecks(context.Background(), fakePinger{pingErr: errors.New("no daemon")}, "1.0.0", t.TempDir())
		for _, r := range results {
			if r.Name == "Docker" && r.OK {
				t.Error("Docker check should fail when ping errors")
			}
		}
	})
}

func TestDoctorAllOK(t *testing.T) {
	ok := doctorAllOK([]checkResult{{Name: "a", OK: true}, {Name: "b", OK: true}})
	if !ok {
		t.Error("all-OK should be true")
	}
	if doctorAllOK([]checkResult{{OK: true}, {OK: false}}) {
		t.Error("one failing check → not OK")
	}
}
