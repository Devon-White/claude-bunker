package sessions

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/events"

	ctr "github.com/Devon-White/claude-bunker/internal/container"
)

func TestWatcher_InitialSnapshot(t *testing.T) {
	cli := &mockClient{
		containers: []container.Summary{
			{ID: "abc", State: "running", Labels: map[string]string{ctr.LabelKey: "project-a1b2c3d4"}},
		},
		inspect: map[string]container.InspectResponse{
			"abc": {ContainerJSONBase: &container.ContainerJSONBase{State: &container.State{}}},
		},
		top: map[string]container.TopResponse{
			"abc": {Titles: []string{"PID", "COMMAND"}, Processes: [][]string{}},
		},
	}

	mgr := NewManager(cli)
	watcher := NewWatcher(mgr, "")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ch := watcher.Subscribe(ctx)

	// Should receive an initial snapshot.
	select {
	case msg := <-ch:
		if msg.Err != nil {
			t.Fatalf("unexpected error: %v", msg.Err)
		}
		if len(msg.Snapshot.Containers) != 1 {
			t.Fatalf("expected 1 container, got %d", len(msg.Snapshot.Containers))
		}
		if msg.Snapshot.Containers[0].Name != "project-a1b2c3d4" {
			t.Errorf("unexpected name: %s", msg.Snapshot.Containers[0].Name)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for initial snapshot")
	}
}

func TestWatcher_ContextCancellation(t *testing.T) {
	cli := &mockClient{}
	mgr := NewManager(cli)
	watcher := NewWatcher(mgr, "")

	ctx, cancel := context.WithCancel(context.Background())
	ch := watcher.Subscribe(ctx)

	// Drain the initial snapshot.
	<-ch

	// Cancel and verify channel closes.
	cancel()

	// Channel should eventually close (all goroutines exit).
	timeout := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return // channel closed — success
			}
		case <-timeout:
			t.Fatal("channel did not close after context cancellation")
		}
	}
}

// mockEventsClient extends mockClient with controllable event channels.
type mockEventsClient struct {
	mockClient
	eventsCh chan events.Message
	errCh    chan error
}

func (m *mockEventsClient) Events(_ context.Context, _ events.ListOptions) (<-chan events.Message, <-chan error) {
	return m.eventsCh, m.errCh
}

func TestWatcher_EventTriggersRefresh(t *testing.T) {
	eventsCh := make(chan events.Message, 1)
	errCh := make(chan error)

	cli := &mockEventsClient{
		mockClient: mockClient{
			containers: []container.Summary{
				{ID: "abc", State: "running", Labels: map[string]string{ctr.LabelKey: "project-a1b2c3d4"}},
			},
			inspect: map[string]container.InspectResponse{
				"abc": {ContainerJSONBase: &container.ContainerJSONBase{State: &container.State{}}},
			},
			top: map[string]container.TopResponse{
				"abc": {Titles: []string{"PID", "COMMAND"}, Processes: [][]string{}},
			},
		},
		eventsCh: eventsCh,
		errCh:    errCh,
	}

	mgr := NewManager(cli)
	watcher := NewWatcher(mgr, "")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ch := watcher.Subscribe(ctx)

	// Drain initial snapshot.
	<-ch

	// Send a Docker event.
	eventsCh <- events.Message{Action: events.ActionStart}

	// Should receive an event-triggered update quickly.
	select {
	case msg := <-ch:
		if msg.Err != nil {
			t.Fatalf("unexpected error: %v", msg.Err)
		}
		if len(msg.Snapshot.Containers) != 1 {
			t.Fatalf("expected 1 container, got %d", len(msg.Snapshot.Containers))
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for event-triggered update")
	}
}

func TestSocketListener_BasicEvent(t *testing.T) {
	tmpDir := t.TempDir()
	sl, err := NewSocketListener(tmpDir)
	if err != nil {
		t.Fatalf("failed to create socket listener: %v", err)
	}
	defer sl.Close()

	var received HookEvent
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go sl.Run(ctx, func(e HookEvent) {
		received = e
	})

	// Give listener time to start.
	time.Sleep(50 * time.Millisecond)

	conn, err := net.Dial("unix", filepath.Join(tmpDir, ".bunker.sock"))
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	conn.Write([]byte(`{"event":"SessionStart","session_id":"abc-123"}`))
	conn.Close()

	// Wait for signal.
	select {
	case <-sl.SignalCh():
		// Success
	case <-time.After(2 * time.Second):
		t.Fatal("no signal received")
	}

	if received.Event != "SessionStart" {
		t.Errorf("expected SessionStart, got %q", received.Event)
	}
	if received.SessionID != "abc-123" {
		t.Errorf("expected session_id abc-123, got %q", received.SessionID)
	}
}

func TestWatcher_SocketTriggersRefresh(t *testing.T) {
	tmpDir := t.TempDir()

	cli := &mockClient{
		containers: []container.Summary{
			{ID: "abc", State: "running", Labels: map[string]string{ctr.LabelKey: "project-a1b2c3d4"}},
		},
		inspect: map[string]container.InspectResponse{
			"abc": {ContainerJSONBase: &container.ContainerJSONBase{State: &container.State{}}},
		},
		top: map[string]container.TopResponse{
			"abc": {Titles: []string{"PID", "COMMAND"}, Processes: [][]string{}},
		},
	}

	mgr := NewManager(cli)
	watcher := NewWatcher(mgr, tmpDir)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch := watcher.Subscribe(ctx)

	// Drain initial snapshot.
	<-ch

	// Connect to socket and send an event.
	conn, err := net.Dial("unix", filepath.Join(tmpDir, ".bunker.sock"))
	if err != nil {
		t.Fatalf("failed to connect to socket: %v", err)
	}
	conn.Write([]byte(`{"event":"Stop","session_id":"test-123"}`))
	conn.Close()

	// Should receive a socket-triggered update within ~2 seconds (1s debounce + processing).
	select {
	case msg := <-ch:
		if msg.Err != nil {
			t.Fatalf("unexpected error: %v", msg.Err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for socket-triggered update")
	}
}
