package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
)

func TestExitCodeFor(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"nil is zero", nil, 0},
		{"plain error is one", errors.New("boom"), 1},
		{"coded error keeps code", Coded(ExitDockerUnavailable, errors.New("no docker")), 4},
		{"wrapped coded error keeps code", fmt.Errorf("startup: %w", Coded(4, errors.New("x"))), 4},
		{"coded nil is passthrough zero", Coded(4, nil), 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExitCodeFor(tt.err); got != tt.want {
				t.Errorf("ExitCodeFor(%v) = %d, want %d", tt.err, got, tt.want)
			}
		})
	}
}

func TestPrintError(t *testing.T) {
	var b bytes.Buffer
	PrintError(&b, errors.New("something broke"))
	out := b.String()
	if b.Len() == 0 {
		t.Fatal("PrintError wrote nothing")
	}
	if !bytes.Contains([]byte(out), []byte("something broke")) {
		t.Errorf("PrintError output %q missing the message", out)
	}
}
