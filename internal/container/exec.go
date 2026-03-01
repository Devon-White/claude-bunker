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
	case <-outputDone:
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

// ExecNonInteractive runs a command without TTY and returns the output.
func ExecNonInteractive(ctx context.Context, cli *client.Client, containerID, user string, cmd []string) (string, error) {
	execCfg := container.ExecOptions{
		User:         user,
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          cmd,
	}

	execResp, err := cli.ContainerExecCreate(ctx, containerID, execCfg)
	if err != nil {
		return "", fmt.Errorf("creating exec: %w", err)
	}

	attachResp, err := cli.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{})
	if err != nil {
		return "", fmt.Errorf("attaching exec: %w", err)
	}
	defer attachResp.Close()

	// Without TTY, Docker multiplexes stdout/stderr with 8-byte frame headers.
	// stdcopy.StdCopy demultiplexes the stream into separate stdout/stderr buffers.
	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, attachResp.Reader); err != nil {
		return "", fmt.Errorf("reading exec output: %w", err)
	}

	inspect, err := cli.ContainerExecInspect(ctx, execResp.ID)
	if err != nil {
		return stdout.String(), fmt.Errorf("inspecting exec: %w", err)
	}

	if inspect.ExitCode != 0 {
		combined := stdout.String() + stderr.String()
		return combined, fmt.Errorf("command exited with code %d: %s", inspect.ExitCode, combined)
	}

	return stdout.String(), nil
}
