package container

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
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
		relPath = filepath.ToSlash(relPath)

		if relPath == "." {
			return nil
		}

		if skip != nil && skip(relPath, info.IsDir()) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		// Skip symlinks to prevent symlink-based traversal attacks. A symlink
		// in a host directory could point to a sensitive container path (e.g.
		// /etc/claude-code/managed-settings.json); copying and chown-ing it
		// would change ownership of the target file. filepath.Walk follows
		// symlinks, so we re-stat with Lstat to detect them.
		if linfo, lerr := os.Lstat(path); lerr == nil && linfo.Mode()&os.ModeSymlink != 0 {
			if linfo.IsDir() {
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
	_, stderr, exitCode, err := execCore(ctx, cli, containerID, user, cmd, stdin)
	if err != nil {
		return err
	}
	if exitCode != 0 {
		return fmt.Errorf("exec exited with code %d: %s", exitCode, stderr)
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

	containerDir = filepath.ToSlash(containerDir)
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

	containerDir = filepath.ToSlash(containerDir)
	return execWithStdin(ctx, cli, containerID, RootUser,
		[]string{"tar", "xf", "-", "-C", containerDir}, buf.Bytes())
}

// CopyDirToContainerExecWithChown copies a host directory tree into a container
// path and fixes ownership, all in a single exec call. This combines mkdir -p,
// tar extraction, and chown -R into one shell command, saving ~400ms of Docker
// API overhead compared to separate EnsureContainerDir + CopyDirToContainerExec
// + ChownRecursive calls.
func CopyDirToContainerExecWithChown(ctx context.Context, cli *client.Client, containerID, hostDir, containerDir, ownerGroup string, skip func(relPath string, isDir bool) bool) error {
	buf, fileCount, err := buildDirTar(hostDir, skip)
	if err != nil {
		return err
	}

	containerDir = filepath.ToSlash(containerDir)

	if fileCount == 0 {
		// No files to copy, but still ensure the directory exists with correct ownership.
		_, err := ExecNonInteractive(ctx, cli, containerID, RootUser,
			[]string{"sh", "-c", fmt.Sprintf("mkdir -p %q && chown %s %q", containerDir, ownerGroup, containerDir)})
		return err
	}

	// Single exec: create dir, extract tar, fix ownership
	return execWithStdin(ctx, cli, containerID, RootUser,
		[]string{"sh", "-c", fmt.Sprintf("mkdir -p %q && tar xf - -C %q && chown -R %s %q", containerDir, containerDir, ownerGroup, containerDir)},
		buf.Bytes())
}

// CopyContentToContainerWithMode writes in-memory content as a single file into
// the container and sets the given chmod mode, all in a single exec call. This
// saves a Docker API round-trip (~200ms) compared to separate write + chmod.
func CopyContentToContainerWithMode(ctx context.Context, cli *client.Client, containerID string, content []byte, containerPath, mode string) error {
	encoded := base64.StdEncoding.EncodeToString(content)
	containerPath = filepath.ToSlash(containerPath)

	if len(content) <= maxArgSize {
		_, err := ExecNonInteractive(ctx, cli, containerID, RootUser,
			[]string{"sh", "-c", "echo \"$1\" | base64 -d > \"$2\" && chmod " + mode + " \"$2\"", "_", encoded, containerPath})
		return err
	}

	return execWithStdin(ctx, cli, containerID, RootUser,
		[]string{"sh", "-c", "base64 -d > \"$1\" && chmod " + mode + " \"$1\"", "_", containerPath}, []byte(encoded))
}

// FileEntry describes a file to write into a container via CopyMultipleToContainer.
type FileEntry struct {
	Content []byte
	Path    string // absolute container path
	Mode    int64  // file permission mode (default 0644 if 0)
}

// CopyMultipleToContainer writes multiple files into the container in a single
// exec by piping a tar archive to `tar xf - -C /`. This is significantly faster
// than calling CopyContentToContainer for each file individually (each of which
// requires a separate Docker exec round-trip).
//
// All files are written as root. Callers should follow up with ChownRecursive
// if ownership needs to change.
func CopyMultipleToContainer(ctx context.Context, cli *client.Client, containerID string, files []FileEntry) error {
	if len(files) == 0 {
		return nil
	}

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	for _, f := range files {
		// Strip leading "/" for tar (tar xf -C / will re-root it)
		name := filepath.ToSlash(f.Path)
		if len(name) > 0 && name[0] == '/' {
			name = name[1:]
		}
		mode := f.Mode
		if mode == 0 {
			mode = 0644
		}
		hdr := &tar.Header{
			Name: name,
			Mode: mode,
			Size: int64(len(f.Content)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return fmt.Errorf("tar header for %s: %w", f.Path, err)
		}
		if _, err := tw.Write(f.Content); err != nil {
			return fmt.Errorf("tar content for %s: %w", f.Path, err)
		}
	}

	if err := tw.Close(); err != nil {
		return fmt.Errorf("closing tar: %w", err)
	}

	return execWithStdin(ctx, cli, containerID, RootUser,
		[]string{"tar", "xf", "-", "-C", "/"}, buf.Bytes())
}

// CopyContentToContainer writes in-memory content as a single file into the
// container using exec + base64. This avoids the Docker archive API
// (CopyToContainer) which silently fails on tmpfs mounts.
//
// For small payloads the base64 data is passed as a command-line argument. For
// large payloads it is piped via stdin to avoid Linux's MAX_ARG_STRLEN limit.
//
// For writing multiple files, prefer CopyMultipleToContainer which batches
// all writes into a single Docker exec round-trip.
func CopyContentToContainer(ctx context.Context, cli *client.Client, containerID string, content []byte, containerPath string) error {
	encoded := base64.StdEncoding.EncodeToString(content)
	containerPath = filepath.ToSlash(containerPath)

	if len(content) <= maxArgSize {
		_, err := ExecNonInteractive(ctx, cli, containerID, RootUser,
			[]string{"sh", "-c", "echo \"$1\" | base64 -d > \"$2\"", "_", encoded, containerPath})
		return err
	}

	return execWithStdin(ctx, cli, containerID, RootUser,
		[]string{"sh", "-c", "base64 -d > \"$1\"", "_", containerPath}, []byte(encoded))
}
