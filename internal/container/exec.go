package container

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/Devon-White/claude-bunker/internal/log"
	"github.com/Devon-White/claude-bunker/internal/platform"
)

// termEnvVars are host environment variables forwarded to interactive exec
// sessions so the container process can detect terminal capabilities (image
// protocols, true-color support, etc.).
var termEnvVars = []string{
	"TERM",
	"COLORTERM",
	"TERM_PROGRAM",
	"TERM_PROGRAM_VERSION",
	"KITTY_WINDOW_ID",
	"KITTY_PID",
	"WT_SESSION",
	"ITERM_SESSION_ID",
}

// ExecInteractive runs a command interactively with TTY support.
// Returns the exit code, the Docker exec ID, and any error.
func ExecInteractive(ctx context.Context, cli *client.Client, containerID, user string, cmd []string) (int, string, error) {
	// Forward terminal-related env vars so the container process can detect
	// capabilities like image pasting (kitty/iTerm2/sixel protocols).
	env := make([]string, 0, len(termEnvVars))
	for _, key := range termEnvVars {
		if val := os.Getenv(key); val != "" {
			env = append(env, key+"="+val)
		}
	}
	if !hasEnvKey(env, "TERM") {
		env = append(env, "TERM=xterm-256color")
	}

	execCfg := container.ExecOptions{
		User:         user,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Tty:          true,
		Cmd:          cmd,
		Env:          env,
	}

	execResp, err := cli.ContainerExecCreate(ctx, containerID, execCfg)
	if err != nil {
		return -1, "", fmt.Errorf("creating exec: %w", err)
	}

	attachResp, err := cli.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{
		Tty: true,
	})
	if err != nil {
		return -1, "", fmt.Errorf("attaching exec: %w", err)
	}
	defer attachResp.Close()

	// Set terminal to raw mode
	oldState, err := platform.MakeRaw()
	if err != nil {
		return -1, "", fmt.Errorf("setting raw mode: %w", err)
	}
	defer platform.Restore(oldState)

	// Enable VT100 input/output on Windows so arrow keys work
	platform.EnableVTMode()

	// Set initial terminal size
	w, h := platform.GetSize()
	if w > 0 && h > 0 {
		_ = cli.ContainerExecResize(ctx, execResp.ID, container.ResizeOptions{
			Width:  uint(w),
			Height: uint(h),
		})
	}

	// Start resize listener
	stopResize := platform.StartResizeListener(ctx, cli, execResp.ID)
	defer stopResize()

	// Stream I/O
	outputDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(os.Stdout, attachResp.Reader)
		outputDone <- err
	}()

	go func() {
		_, _ = io.Copy(attachResp.Conn, os.Stdin)
	}()

	// Wait for output to complete (command exited)
	select {
	case err := <-outputDone:
		if err != nil {
			log.Warnf("output stream error: %v", err)
		}
	case <-ctx.Done():
		return -1, "", ctx.Err()
	}

	// Get exit code
	inspect, err := cli.ContainerExecInspect(ctx, execResp.ID)
	if err != nil {
		return -1, execResp.ID, fmt.Errorf("inspecting exec: %w", err)
	}

	return inspect.ExitCode, execResp.ID, nil
}

// execCore is the shared implementation for non-interactive Docker exec operations.
// It creates an exec, optionally pipes stdin data, captures stdout/stderr, and
// returns all output fields so callers can format errors differently.
func execCore(ctx context.Context, cli *client.Client, containerID, user string, cmd []string, stdin []byte) (stdout, stderr string, exitCode int, err error) {
	attachStdin := len(stdin) > 0
	execCfg := container.ExecOptions{
		User:         user,
		AttachStdin:  attachStdin,
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          cmd,
	}

	execResp, err := cli.ContainerExecCreate(ctx, containerID, execCfg)
	if err != nil {
		return "", "", -1, fmt.Errorf("creating exec: %w", err)
	}

	attachResp, err := cli.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{})
	if err != nil {
		return "", "", -1, fmt.Errorf("attaching exec: %w", err)
	}
	defer attachResp.Close()

	// ContainerExecAttach's hijacked connection is a raw net.Conn once
	// established: ctx is only consulted for the initial handshake, so a
	// blocked write/read below (stdin write, or stdcopy.StdCopy) will NOT be
	// interrupted by ctx cancellation on its own. Close the connection when
	// ctx is done so callers with a deadline (e.g. execAgentsJSON's 5s
	// timeout) actually get unblocked instead of hanging on a wedged
	// container. On the normal/success path "done" fires first and the
	// watcher exits without touching the connection.
	done := make(chan struct{})
	defer close(done)
	go closeOnCtxDone(ctx, done, attachResp.Close)

	// If stdin data was provided, write it and close the write side so the
	// command sees EOF.
	if attachStdin {
		if _, err := attachResp.Conn.Write(stdin); err != nil {
			return "", "", -1, fmt.Errorf("writing to exec stdin: %w", err)
		}
		if err := attachResp.CloseWrite(); err != nil {
			return "", "", -1, fmt.Errorf("closing exec stdin: %w", err)
		}
	}

	// Without TTY, Docker multiplexes stdout/stderr with 8-byte frame headers.
	// stdcopy.StdCopy demultiplexes the stream into separate stdout/stderr buffers.
	var stdoutBuf, stderrBuf bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdoutBuf, &stderrBuf, attachResp.Reader); err != nil {
		return "", "", -1, fmt.Errorf("reading exec output: %w", err)
	}

	inspect, err := cli.ContainerExecInspect(ctx, execResp.ID)
	if err != nil {
		return stdoutBuf.String(), stderrBuf.String(), -1, fmt.Errorf("inspecting exec: %w", err)
	}

	return stdoutBuf.String(), stderrBuf.String(), inspect.ExitCode, nil
}

// ExecNonInteractive runs a command without TTY and returns the output.
func ExecNonInteractive(ctx context.Context, cli *client.Client, containerID, user string, cmd []string) (string, error) {
	stdout, stderr, exitCode, err := execCore(ctx, cli, containerID, user, cmd, nil)
	if err != nil {
		return "", err
	}
	if exitCode != 0 {
		combined := stdout + stderr
		return combined, fmt.Errorf("command exited with code %d: %s", exitCode, combined)
	}
	return stdout, nil
}

// EnsureContainerDir creates a directory and sets ownership in a single exec.
func EnsureContainerDir(ctx context.Context, cli *client.Client, containerID, dir string) error {
	_, err := ExecNonInteractive(ctx, cli, containerID, RootUser,
		[]string{"sh", "-c", `mkdir -p "$1" && chown "$2" "$1"`, "_", dir, ContainerUserGroup})
	return err
}

// ChownRecursive sets ownership recursively on a container path.
func ChownRecursive(ctx context.Context, cli *client.Client, containerID, dir string) error {
	_, err := ExecNonInteractive(ctx, cli, containerID, RootUser,
		[]string{"chown", "-R", ContainerUserGroup, dir})
	return err
}

// closeOnCtxDone waits for either ctx to be canceled/expired or done to be
// closed, whichever happens first. If ctx wins, closeConn is invoked to
// unblock a caller that's stuck in a blocking read/write on the associated
// connection (a hijacked Docker exec connection does not itself observe ctx
// once established — see execCore). If done wins (the normal path, where the
// caller finished before ctx fired), closeConn is never called and the
// connection is left to the caller's own cleanup (e.g. a deferred Close).
func closeOnCtxDone(ctx context.Context, done <-chan struct{}, closeConn func()) {
	select {
	case <-ctx.Done():
		closeConn()
	case <-done:
	}
}

// hasEnvKey returns true if any entry in env starts with key=.
func hasEnvKey(env []string, key string) bool {
	prefix := key + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return true
		}
	}
	return false
}
