package sessions

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// HookEvent represents an event received from the bunker-hook.sh script
// inside a container via the Unix domain socket.
type HookEvent struct {
	Event       string `json:"event"`
	SessionID   string `json:"session_id"`
	Title       string `json:"title"`
	ContainerID string `json:"container_id"`
}

// SocketListener listens on a Unix domain socket for hook events from
// containers. The socket is created on the workspace bind mount so
// containers can connect via socat from inside.
type SocketListener struct {
	socketPath string
	listener   net.Listener
	signalCh   chan struct{} // buffered(1), signals watcher to fetch
	mu         sync.Mutex
	closed     bool
}

// NewSocketListener creates a Unix domain socket at <workspace>/.bunker.sock.
// Returns nil and an error if the socket cannot be created.
func NewSocketListener(workspace string) (*SocketListener, error) {
	sockPath := filepath.Join(workspace, ".bunker.sock")

	// Clean up stale socket from a previous run that didn't shut down cleanly.
	os.Remove(sockPath)

	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, err
	}

	// Make socket writable by the container user (UID 1000).
	// The workspace bind mount preserves host permissions, and the
	// container user needs write access to connect via socat.
	os.Chmod(sockPath, 0666)

	return &SocketListener{
		socketPath: sockPath,
		listener:   listener,
		signalCh:   make(chan struct{}, 1),
	}, nil
}

// SignalCh returns a channel that receives a signal when any hook event
// arrives. The channel is buffered(1) so rapid events coalesce naturally
// — the watcher reads one signal and fetches a fresh snapshot.
func (s *SocketListener) SignalCh() <-chan struct{} {
	return s.signalCh
}

// Run accepts connections and reads hook events until ctx is cancelled.
// Each connection sends a single JSON line and closes. On any event,
// a signal is sent to SignalCh. The optional onEvent callback is invoked
// for each successfully parsed event (e.g., to process title pushes).
func (s *SocketListener) Run(ctx context.Context, onEvent func(HookEvent)) {
	go func() {
		<-ctx.Done()
		s.Close()
	}()

	for {
		conn, err := s.listener.Accept()
		if err != nil {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if closed {
				return
			}
			continue
		}

		go s.handleConn(conn, onEvent)
	}
}

func (s *SocketListener) handleConn(conn net.Conn, onEvent func(HookEvent)) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))

	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil || n == 0 {
		// Even on read error, signal a refresh as a precaution.
		s.signal()
		return
	}

	var event HookEvent
	if err := json.Unmarshal(buf[:n], &event); err != nil {
		// Malformed JSON — still trigger a refresh.
		s.signal()
		return
	}

	if onEvent != nil {
		onEvent(event)
	}

	s.signal()
}

func (s *SocketListener) signal() {
	select {
	case s.signalCh <- struct{}{}:
	default:
		// Already signaled, coalesce.
	}
}

// Close shuts down the listener and removes the socket file.
func (s *SocketListener) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	s.listener.Close()
	os.Remove(s.socketPath)
}
