package platform

import (
	"context"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

// delayedResize performs a single resize after a short delay to handle initial
// terminal sizing. It is shared by both Unix and Windows resize listeners.
func delayedResize(ctx context.Context, cli *client.Client, execID string, done <-chan struct{}) {
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
}
