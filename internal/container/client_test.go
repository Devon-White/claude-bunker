package container

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestNewClientPingTimeout verifies NewClient bounds its daemon ping instead of
// hanging forever against a socket that is present but unresponsive. A TCP
// listener accepts the connection but never writes an HTTP response, so moby's
// Ping blocks reading the response until the ping context's deadline fires.
// Without the bounded context in NewClient this test hangs and is killed by
// `go test -timeout`; with the 5s bound NewClient returns an error promptly.
func TestNewClientPingTimeout(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	// Deferred LIFO ordering matters: ln.Close() must run BEFORE wg.Wait().
	// Registering wg.Wait() first (runs last) and ln.Close() last (runs first)
	// means closing the listener unblocks Accept, the goroutine returns, then
	// wg.Wait() completes. Registering them the other way deadlocks.
	var wg sync.WaitGroup
	defer wg.Wait()
	defer ln.Close()

	wg.Add(1)
	go func() {
		defer wg.Done()
		var held []net.Conn
		for {
			conn, err := ln.Accept()
			if err != nil {
				for _, c := range held {
					c.Close()
				}
				return
			}
			// Black hole: hold the connection open and never respond.
			held = append(held, conn)
		}
	}()

	// Point the Docker client at the black-hole listener; clear TLS so FromEnv
	// does not try to load certificates. t.Setenv restores the prior
	// environment (including any real DOCKER_HOST) when the test ends.
	t.Setenv("DOCKER_HOST", "tcp://"+ln.Addr().String())
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")

	start := time.Now()
	cli, err := NewClient()
	elapsed := time.Since(start)
	if cli != nil {
		cli.Close()
	}
	if err == nil {
		t.Fatalf("expected error from unresponsive daemon, got nil (elapsed %s)", elapsed)
	}
	if elapsed > 30*time.Second {
		t.Fatalf("NewClient did not fail fast: took %s (ping likely unbounded)", elapsed)
	}
}

// TestDockerErrorMessage covers branch selection in dockerErrorMessage,
// including the context.DeadlineExceeded special case added for the bounded
// ping. Instant, no network. dockerErrorMessage is tested directly (not via a
// live Ping) so the assertion does not depend on moby's error-wrapping internals.
func TestDockerErrorMessage(t *testing.T) {
	tests := []struct {
		name         string
		err          error
		wantContains string
	}{
		{
			name:         "deadline exceeded",
			err:          context.DeadlineExceeded,
			wantContains: "did not respond within 5s",
		},
		{
			name:         "wrapped deadline exceeded",
			err:          fmt.Errorf("Get \"http://docker/_ping\": %w", context.DeadlineExceeded),
			wantContains: "did not respond within 5s",
		},
		{
			name:         "permission denied",
			err:          fmt.Errorf("dial unix /var/run/docker.sock: connect: permission denied"),
			wantContains: "Docker permission denied",
		},
		{
			name:         "socket not found",
			err:          fmt.Errorf("dial unix /var/run/docker.sock: connect: no such file or directory"),
			wantContains: "Docker not found",
		},
		{
			name:         "generic",
			err:          fmt.Errorf("connection reset by peer"),
			wantContains: "Cannot connect to Docker",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dockerErrorMessage(tt.err)
			if !strings.Contains(got, tt.wantContains) {
				t.Fatalf("dockerErrorMessage(%v) = %q, want substring %q", tt.err, got, tt.wantContains)
			}
		})
	}
}
