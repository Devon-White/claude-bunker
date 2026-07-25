package container

import (
	"context"
	"net"
	"testing"
	"time"
)

// TestCloseOnCtxDone_UnblocksBlockedRead proves the exact mechanism execCore
// relies on to bound `claude agents --json` (and any other non-interactive
// exec) by a caller ctx timeout: closing the hijacked connection when ctx is
// done unblocks a read that would otherwise hang forever, since the Docker
// hijacked connection does not itself observe ctx cancellation once
// established.
//
// net.Pipe is used to stand in for the hijacked connection: a Read on one
// end blocks until data arrives or the pipe is closed, exactly like a Read
// on a stalled `docker exec` stream with a wedged container process on the
// other side.
func TestCloseOnCtxDone_UnblocksBlockedRead(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	defer close(done)
	go closeOnCtxDone(ctx, done, func() { serverConn.Close() })

	readErr := make(chan error, 1)
	go func() {
		buf := make([]byte, 16)
		// Nothing is ever written to clientConn, so absent the ctx-driven
		// close this Read blocks indefinitely — modeling a hung
		// `claude agents --json` process that never produces output.
		_, err := serverConn.Read(buf)
		readErr <- err
	}()

	select {
	case err := <-readErr:
		if err == nil {
			t.Fatal("expected Read to return an error once the connection was closed, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Read did not unblock within 2s of ctx timing out; ctx cancellation is not bounding the read")
	}
}

// TestCloseOnCtxDone_NormalPathLeavesConnOpen proves the success path is
// unaffected: when done fires before ctx expires, closeOnCtxDone must not
// touch the connection, so a subsequent read of already-buffered/streamed
// data is unaffected (mirrors execCore's real defer ordering, where "done"
// closes before the connection's own deferred Close runs).
func TestCloseOnCtxDone_NormalPathLeavesConnOpen(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	closed := make(chan struct{}, 1)
	done := make(chan struct{})

	go closeOnCtxDone(ctx, done, func() {
		select {
		case closed <- struct{}{}:
		default:
		}
	})

	// Signal completion (the "normal path") before ctx has any chance to fire.
	close(done)

	// Exchange data to prove the connection is still healthy afterward.
	want := []byte("ok")
	go func() { _, _ = clientConn.Write(want) }()
	got := make([]byte, len(want))
	if _, err := serverConn.Read(got); err != nil {
		t.Fatalf("connection should still be usable after the normal path, Read failed: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("got %q, want %q", got, want)
	}

	select {
	case <-closed:
		t.Fatal("closeConn should not have been invoked when done fires before ctx")
	default:
	}
}
