package sessions

import (
	"context"
	"sync"
	"time"

	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
)

// UpdateMsg is sent through the subscription channel when state changes.
type UpdateMsg struct {
	Snapshot Snapshot
	Err      error
}

// Watcher combines Docker event stream monitoring with periodic polling
// for session state updates. Docker events catch container lifecycle
// (start/stop/die), while the poller catches session-level changes
// (new sessions, subagents, title changes) that Docker events alone
// cannot detect.
type Watcher struct {
	mgr *Manager
}

// NewWatcher creates a watcher.
func NewWatcher(mgr *Manager) *Watcher {
	return &Watcher{mgr: mgr}
}

// pollInterval is how often the watcher refreshes session state from
// `claude agents --json` while subscribed.
const pollInterval = 3 * time.Second

// Subscribe starts watching and returns a channel of UpdateMsg.
// Cancel the context to stop watching. The channel is closed only after
// all goroutines finish, preventing send-on-closed-channel panics.
func (w *Watcher) Subscribe(ctx context.Context) <-chan UpdateMsg {
	out := make(chan UpdateMsg, 1)
	var wg sync.WaitGroup
	wg.Add(3) // initial + docker events + poller

	go func() { // 1. initial snapshot
		defer wg.Done()
		snap, err := w.mgr.FetchSnapshot(ctx)
		select {
		case out <- UpdateMsg{Snapshot: snap, Err: err}:
		case <-ctx.Done():
		}
	}()
	go func() { defer wg.Done(); w.watchEvents(ctx, out) }() // 2. container lifecycle
	go func() { defer wg.Done(); w.pollRefresh(ctx, out) }() // 3. session/title poll

	go func() { wg.Wait(); close(out) }()
	return out
}

// watchEvents subscribes to Docker events and triggers snapshot refreshes.
// Only listens for container lifecycle events — session-level changes are
// caught by the poller instead.
func (w *Watcher) watchEvents(ctx context.Context, out chan<- UpdateMsg) {
	f := filters.NewArgs()
	f.Add("type", string(events.ContainerEventType))
	for _, action := range []string{"start", "stop", "die", "destroy"} {
		f.Add("event", action)
	}

	msgCh, errCh := w.mgr.cli.Events(ctx, events.ListOptions{Filters: f})

	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-msgCh:
			if !ok {
				return
			}
			// Debounce: wait briefly for rapid lifecycle events to coalesce
			// (e.g., "stop" followed by "die" within milliseconds).
			timer := time.NewTimer(500 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			w.drainEvents(msgCh)

			snap, err := w.mgr.FetchSnapshot(ctx)
			select {
			case out <- UpdateMsg{Snapshot: snap, Err: err}:
			default:
			}
		case _, ok := <-errCh:
			if !ok {
				return
			}
			return
		}
	}
}

// pollRefresh periodically fetches snapshots. This is the only way to
// detect session-level changes (new sessions, title renames, subagent
// spawns) since Docker events only cover container lifecycle.
func (w *Watcher) pollRefresh(ctx context.Context, out chan<- UpdateMsg) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			snap, err := w.mgr.FetchSnapshot(ctx)
			select {
			case out <- UpdateMsg{Snapshot: snap, Err: err}:
			default:
			}
		}
	}
}

// drainEvents reads and discards queued events to coalesce rapid updates.
func (w *Watcher) drainEvents(ch <-chan events.Message) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}
