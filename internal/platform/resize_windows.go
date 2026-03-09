//go:build windows

package platform

import (
	"context"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

// StartResizeListener on Windows polls for terminal size changes since
// SIGWINCH is not available.
// Returns a function to stop the listener.
func StartResizeListener(ctx context.Context, cli *client.Client, execID string) func() {
	done := make(chan struct{})

	go func() {
		lastW, lastH := GetSize()
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				w, h := GetSize()
				if w > 0 && h > 0 && (w != lastW || h != lastH) {
					lastW, lastH = w, h
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
	go delayedResize(ctx, cli, execID, done)

	return func() {
		close(done)
	}
}
