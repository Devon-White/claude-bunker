//go:build !windows

package platform

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

// StartResizeListener monitors SIGWINCH signals and resizes the exec session.
// Returns a function to stop the listener.
func StartResizeListener(ctx context.Context, cli *client.Client, execID string) func() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)

	done := make(chan struct{})
	go func() {
		defer signal.Stop(sigCh)
		for {
			select {
			case <-sigCh:
				w, h := GetSize()
				if w > 0 && h > 0 {
					_ = cli.ContainerExecResize(ctx, execID, container.ResizeOptions{
						Width:  uint(w),
						Height: uint(h),
					})
				}
			case <-done:
				return
			case <-ctx.Done():
				return
			}
		}
	}()

	// Also do a resize after a short delay to handle initial sizing
	go func() {
		select {
		case <-time.After(100 * time.Millisecond):
			w, h := GetSize()
			if w > 0 && h > 0 {
				_ = cli.ContainerExecResize(ctx, execID, container.ResizeOptions{
					Width:  uint(w),
					Height: uint(h),
				})
			}
		case <-done:
		case <-ctx.Done():
		}
	}()

	return func() {
		close(done)
	}
}
