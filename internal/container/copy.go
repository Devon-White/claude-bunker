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
)

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
func CopyContentToContainer(ctx context.Context, cli *client.Client, containerID string, content []byte, containerPath string) error {
	encoded := base64.StdEncoding.EncodeToString(content)
	containerPath = strings.ReplaceAll(containerPath, "\\", "/")
	_, err := ExecNonInteractive(ctx, cli, containerID, "root",
		[]string{"sh", "-c", "echo \"$1\" | base64 -d > \"$2\"", "_", encoded, containerPath})
	return err
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
