package container

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

// maxArgSize is the threshold below which we pass base64 data as a command-line
// argument. Above this we pipe via stdin to avoid hitting Linux's
// MAX_ARG_STRLEN (~131KB per argument). Set to 96KB of raw content because
// base64 encoding inflates size by ~33% (96KB raw → ~128KB base64), staying
// safely under the kernel limit.
const maxArgSize = 96 * 1024

// CopyDirToContainer recursively copies an entire host directory tree into a
// container directory, preserving the subdirectory structure.
func CopyDirToContainer(ctx context.Context, cli *client.Client, containerID, hostDir, containerDir string) error {
	base := filepath.Clean(hostDir)

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	fileCount := 0

	err := filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}

		relPath, err := filepath.Rel(base, path)
		if err != nil {
			return nil
		}
		// Normalize to forward slashes for tar
		relPath = strings.ReplaceAll(relPath, "\\", "/")

		if info.IsDir() {
			if relPath == "." {
				return nil
			}
			hdr := &tar.Header{
				Typeflag: tar.TypeDir,
				Name:     relPath + "/",
				Mode:     0755,
				ModTime:  info.ModTime(),
			}
			return tw.WriteHeader(hdr)
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil // skip unreadable files
		}

		hdr := &tar.Header{
			Name:    relPath,
			Mode:    0600,
			Size:    int64(len(data)),
			ModTime: info.ModTime(),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := tw.Write(data); err != nil {
			return err
		}
		fileCount++
		return nil
	})
	if err != nil {
		return fmt.Errorf("walking %s: %w", hostDir, err)
	}

	if err := tw.Close(); err != nil {
		return err
	}

	if fileCount == 0 {
		return nil // nothing to copy
	}

	containerDir = strings.ReplaceAll(containerDir, "\\", "/")
	return cli.CopyToContainer(ctx, containerID, containerDir, &buf, container.CopyToContainerOptions{})
}

// CopyContentToContainer writes in-memory content as a single file into the
// container using exec + base64. This avoids the Docker archive API
// (CopyToContainer) which silently fails on tmpfs mounts with Docker Desktop
// for Windows.
//
// For small payloads the base64 data is passed as a command-line argument. For
// large payloads it is piped via stdin to avoid Linux's MAX_ARG_STRLEN limit.
func CopyContentToContainer(ctx context.Context, cli *client.Client, containerID string, content []byte, containerPath string) error {
	encoded := base64.StdEncoding.EncodeToString(content)
	containerPath = strings.ReplaceAll(containerPath, "\\", "/")

	if len(content) <= maxArgSize {
		_, err := ExecNonInteractive(ctx, cli, containerID, "root",
			[]string{"sh", "-c", "echo \"$1\" | base64 -d > \"$2\"", "_", encoded, containerPath})
		return err
	}

	return copyContentViaStdin(ctx, cli, containerID, encoded, containerPath)
}

// copyContentViaStdin pipes base64-encoded data through stdin into the
// container, avoiding command-line argument size limits.
func copyContentViaStdin(ctx context.Context, cli *client.Client, containerID, encoded, containerPath string) error {
	execCfg := container.ExecOptions{
		User:         "root",
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          []string{"sh", "-c", "base64 -d > \"$1\"", "_", containerPath},
	}

	execResp, err := cli.ContainerExecCreate(ctx, containerID, execCfg)
	if err != nil {
		return fmt.Errorf("creating exec: %w", err)
	}

	attachResp, err := cli.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{})
	if err != nil {
		return fmt.Errorf("attaching exec: %w", err)
	}
	defer attachResp.Close()

	// Write the base64 data to stdin, then close to signal EOF.
	if _, err := attachResp.Conn.Write([]byte(encoded)); err != nil {
		return fmt.Errorf("writing to exec stdin: %w", err)
	}
	if err := attachResp.CloseWrite(); err != nil {
		return fmt.Errorf("closing exec stdin: %w", err)
	}

	// Drain stdout/stderr so the exec process can finish.
	var stdout, stderr bytes.Buffer
	_, _ = stdcopy.StdCopy(&stdout, &stderr, attachResp.Reader)

	inspect, err := cli.ContainerExecInspect(ctx, execResp.ID)
	if err != nil {
		return fmt.Errorf("inspecting exec: %w", err)
	}
	if inspect.ExitCode != 0 {
		return fmt.Errorf("copy via stdin exited with code %d: %s", inspect.ExitCode, stderr.String())
	}

	return nil
}

// CopyFileToContainer copies a single file from the host into the container.
// Delegates to CopyContentToContainer (exec+base64) to avoid cli.CopyToContainer
// which silently fails on tmpfs mounts with Docker Desktop for Windows.
func CopyFileToContainer(ctx context.Context, cli *client.Client, containerID, hostPath, containerDir string) error {
	data, err := os.ReadFile(hostPath)
	if err != nil {
		return fmt.Errorf("reading %s: %w", hostPath, err)
	}

	containerDir = strings.TrimRight(strings.ReplaceAll(containerDir, "\\", "/"), "/")
	containerPath := containerDir + "/" + filepath.Base(hostPath)

	return CopyContentToContainer(ctx, cli, containerID, data, containerPath)
}
