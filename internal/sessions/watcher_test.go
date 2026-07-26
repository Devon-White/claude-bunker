package sessions

import (
	"context"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/client"

	ctr "github.com/Devon-White/claude-bunker/internal/container"
)

func TestWatcher_InitialSnapshot(t *testing.T) {
	orig := execAgentsJSON
	defer func() { execAgentsJSON = orig }()
	execAgentsJSON = func(_ context.Context, _ *client.Client, _ string) (string, error) { return "[]", nil }

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
	watcher := NewWatcher(mgr, defaultPollInterval)

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
	watcher := NewWatcher(mgr, defaultPollInterval)

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
	orig := execAgentsJSON
	defer func() { execAgentsJSON = orig }()
	execAgentsJSON = func(_ context.Context, _ *client.Client, _ string) (string, error) { return "[]", nil }

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
	watcher := NewWatcher(mgr, defaultPollInterval)

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

func TestNewWatcher_Interval(t *testing.T) {
	mgr := NewManager(&mockClient{})

	t.Run("stores explicit interval", func(t *testing.T) {
		w := NewWatcher(mgr, 10*time.Second)
		if w.pollInterval != 10*time.Second {
			t.Errorf("pollInterval = %v, want %v", w.pollInterval, 10*time.Second)
		}
	})

	t.Run("non-positive interval falls back to default", func(t *testing.T) {
		w := NewWatcher(mgr, 0)
		if w.pollInterval != defaultPollInterval {
			t.Errorf("pollInterval = %v, want %v", w.pollInterval, defaultPollInterval)
		}
		if defaultPollInterval != 3*time.Second {
			t.Errorf("defaultPollInterval = %v, want 3s", defaultPollInterval)
		}
	})
}
