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

// buildDirTar creates a tar archive from a host directory tree. The optional
// skip function is called for each entry with its path relative to hostDir;
// return true to exclude the entry (and its subtree for directories).
// Returns the tar buffer and file count. Unreadable entries are silently skipped.
func buildDirTar(hostDir string, skip func(relPath string, isDir bool) bool) (*bytes.Buffer, int, error) {
	base := filepath.Clean(hostDir)

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	fileCount := 0

	err := filepath.Walk(base, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return nil // skip unreadable entries
		}

		relPath, relErr := filepath.Rel(base, path)
		if relErr != nil {
			return nil
		}
		relPath = strings.ReplaceAll(relPath, "\\", "/")

		if relPath == "." {
			return nil
		}

		if skip != nil && skip(relPath, info.IsDir()) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if info.IsDir() {
			return tw.WriteHeader(&tar.Header{
				Typeflag: tar.TypeDir,
				Name:     relPath + "/",
				Mode:     0755,
				ModTime:  info.ModTime(),
			})
		}

		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil // skip unreadable files
		}

		if err := tw.WriteHeader(&tar.Header{
			Name:    relPath,
			Mode:    0600,
			Size:    int64(len(data)),
			ModTime: info.ModTime(),
		}); err != nil {
			return err
		}
		_, writeErr := tw.Write(data)
		if writeErr == nil {
			fileCount++
		}
		return writeErr
	})
	if err != nil {
		return nil, 0, fmt.Errorf("walking %s: %w", hostDir, err)
	}

	if err := tw.Close(); err != nil {
		return nil, 0, err
	}

	return &buf, fileCount, nil
}

// execWithStdin runs a command in the container, piping data to its stdin,
// and returns an error if the command fails.
func execWithStdin(ctx context.Context, cli *client.Client, containerID, user string, cmd []string, stdin []byte) error {
	execCfg := container.ExecOptions{
		User:         user,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          cmd,
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

	if _, err := attachResp.Conn.Write(stdin); err != nil {
		return fmt.Errorf("writing to exec stdin: %w", err)
	}
	if err := attachResp.CloseWrite(); err != nil {
		return fmt.Errorf("closing exec stdin: %w", err)
	}

	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, attachResp.Reader); err != nil {
		return fmt.Errorf("reading exec output: %w", err)
	}

	inspect, err := cli.ContainerExecInspect(ctx, execResp.ID)
	if err != nil {
		return fmt.Errorf("inspecting exec: %w", err)
	}
	if inspect.ExitCode != 0 {
		return fmt.Errorf("exec exited with code %d: %s", inspect.ExitCode, stderr.String())
	}

	return nil
}

// CopyDirToContainer recursively copies an entire host directory tree into a
// container directory, preserving the subdirectory structure.
func CopyDirToContainer(ctx context.Context, cli *client.Client, containerID, hostDir, containerDir string) error {
	buf, fileCount, err := buildDirTar(hostDir, nil)
	if err != nil {
		return err
	}
	if fileCount == 0 {
		return nil
	}

	containerDir = strings.ReplaceAll(containerDir, "\\", "/")
	return cli.CopyToContainer(ctx, containerID, containerDir, buf, container.CopyToContainerOptions{})
}

// CopyDirToContainerExec copies a host directory tree into a container path
// by piping a tar archive through docker exec. This is the Docker-recommended
// approach for writing to tmpfs mounts, where the archive API (CopyToContainer)
// silently writes to the hidden layer beneath the mount.
//
// The optional skip function is called for each entry with its path relative
// to hostDir. Return true to skip the entry (and its subtree for directories).
func CopyDirToContainerExec(ctx context.Context, cli *client.Client, containerID, hostDir, containerDir string, skip func(relPath string, isDir bool) bool) error {
	buf, fileCount, err := buildDirTar(hostDir, skip)
	if err != nil {
		return err
	}
	if fileCount == 0 {
		return nil
	}

	containerDir = strings.ReplaceAll(containerDir, "\\", "/")
	return execWithStdin(ctx, cli, containerID, RootUser,
		[]string{"tar", "xf", "-", "-C", containerDir}, buf.Bytes())
}

// CopyContentToContainer writes in-memory content as a single file into the
// container using exec + base64. This avoids the Docker archive API
// (CopyToContainer) which silently fails on tmpfs mounts.
//
// For small payloads the base64 data is passed as a command-line argument. For
// large payloads it is piped via stdin to avoid Linux's MAX_ARG_STRLEN limit.
func CopyContentToContainer(ctx context.Context, cli *client.Client, containerID string, content []byte, containerPath string) error {
	encoded := base64.StdEncoding.EncodeToString(content)
	containerPath = strings.ReplaceAll(containerPath, "\\", "/")

	if len(content) <= maxArgSize {
		_, err := ExecNonInteractive(ctx, cli, containerID, RootUser,
			[]string{"sh", "-c", "echo \"$1\" | base64 -d > \"$2\"", "_", encoded, containerPath})
		return err
	}

	return execWithStdin(ctx, cli, containerID, RootUser,
		[]string{"sh", "-c", "base64 -d > \"$1\"", "_", containerPath}, []byte(encoded))
}
