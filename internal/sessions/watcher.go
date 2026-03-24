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

// Watcher combines Docker event stream monitoring with hook-driven socket
// events for session state updates. Docker events catch container lifecycle
// (start/stop/die), while Claude Code hooks signal session-level changes
// (new sessions, subagents, title changes) via a Unix domain socket.
type Watcher struct {
	mgr            *Manager
	socketListener *SocketListener // nil if socket creation failed
	onHookEvent    func(HookEvent) // optional callback for title pushes
}

// NewWatcher creates a watcher. workspace is used to create the Unix socket
// at <workspace>/.bunker.sock for receiving hook events from containers.
// If socket creation fails, the watcher still works for Docker events
// but won't receive hook-driven updates.
func NewWatcher(mgr *Manager, workspace string) *Watcher {
	w := &Watcher{
		mgr: mgr,
	}

	if workspace != "" {
		sl, err := NewSocketListener(workspace)
		if err == nil {
			w.socketListener = sl
		}
		// If socket fails, Docker events still cover container lifecycle.
	}

	return w
}

// SetHookEventCallback sets an optional callback invoked on each hook event.
// Used by the TUI to process title pushes from the Stop hook.
func (w *Watcher) SetHookEventCallback(fn func(HookEvent)) {
	w.onHookEvent = fn
}

// pollInterval is the fallback polling interval used when the Unix socket
// is unavailable (e.g., Windows, where sockets don't traverse bind mounts).
// This catches session-level changes (new sessions, subagents, title renames)
// that Docker events alone cannot detect.
const pollInterval = 5 * time.Second

// Subscribe starts watching and returns a channel of UpdateMsg.
// Cancel the context to stop watching. The channel is closed only after
// all goroutines finish, preventing send-on-closed-channel panics.
func (w *Watcher) Subscribe(ctx context.Context) <-chan UpdateMsg {
	out := make(chan UpdateMsg, 1)

	goroutines := 2 // initial + Docker events
	if w.socketListener != nil {
		goroutines++ // socket listener
	} else {
		goroutines++ // fallback poller
	}

	var wg sync.WaitGroup
	wg.Add(goroutines)

	// 1. Send initial snapshot immediately.
	go func() {
		defer wg.Done()
		snap, err := w.mgr.FetchSnapshot(ctx)
		select {
		case out <- UpdateMsg{Snapshot: snap, Err: err}:
		case <-ctx.Done():
		}
	}()

	// 2. Docker event stream: container lifecycle (start/stop/die/destroy).
	go func() {
		defer wg.Done()
		w.watchEvents(ctx, out)
	}()

	if w.socketListener != nil {
		// 3a. Socket listener: hook-driven session/subagent/title changes.
		go func() {
			defer wg.Done()
			w.watchSocket(ctx, out)
		}()
	} else {
		// 3b. Fallback poller: periodic refresh for platforms where the
		// Unix socket doesn't work (Windows bind mounts, socket creation
		// failure). Catches session-level changes that Docker events miss.
		go func() {
			defer wg.Done()
			w.pollRefresh(ctx, out)
		}()
	}

	// Close channel only after all writers are done.
	go func() {
		wg.Wait()
		if w.socketListener != nil {
			w.socketListener.Close()
		}
		close(out)
	}()

	return out
}

// watchSocket listens for hook events via the Unix socket and triggers
// snapshot refreshes. Includes a 1-second debounce to coalesce rapid events
// (e.g., multiple subagents starting simultaneously).
func (w *Watcher) watchSocket(ctx context.Context, out chan<- UpdateMsg) {
	go w.socketListener.Run(ctx, w.onHookEvent)

	signalCh := w.socketListener.SignalCh()

	for {
		select {
		case <-ctx.Done():
			return
		case <-signalCh:
			// Debounce: wait 1 second for more events to coalesce.
			timer := time.NewTimer(1 * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			// Drain any signals that arrived during debounce.
			select {
			case <-signalCh:
			default:
			}

			snap, err := w.mgr.FetchSnapshot(ctx)
			select {
			case out <- UpdateMsg{Snapshot: snap, Err: err}:
			default:
			}
		}
	}
}

// watchEvents subscribes to Docker events and triggers snapshot refreshes.
// Only listens for container lifecycle events — session-level changes are
// handled by hooks via the socket listener.
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

// pollRefresh periodically fetches snapshots as a fallback when the Unix
// socket is unavailable. This is the only way to detect session-level
// changes (new sessions, title renames, subagent spawns) on platforms
// where the hook socket doesn't work.
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
