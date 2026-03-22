package container

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/Devon-White/claude-bunker/internal/log"
	"github.com/Devon-White/claude-bunker/internal/platform"
)

// ExecInteractive runs a command interactively with TTY support.
// Returns the exit code, the Docker exec ID, and any error.
func ExecInteractive(ctx context.Context, cli *client.Client, containerID, user string, cmd []string) (int, string, error) {
	// Pass TERM so the container process knows terminal capabilities.
	termVal := os.Getenv("TERM")
	if termVal == "" {
		termVal = "xterm-256color"
	}

	execCfg := container.ExecOptions{
		User:         user,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Tty:          true,
		Cmd:          cmd,
		Env:          []string{"TERM=" + termVal},
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
