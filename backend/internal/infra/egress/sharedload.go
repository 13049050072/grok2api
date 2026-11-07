package egress

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"

	"golang.org/x/sync/singleflight"
)

// sharedSnapshotLoadTimeout bounds repository reads shared through singleflight.
const sharedSnapshotLoadTimeout = 30 * time.Second

// sharedLoad runs load once per key for all concurrent callers. The shared work
// must not inherit the starting caller's cancellation, or one aborted request
// fails every caller joined to it; it is bounded by timeout instead. Each caller
// still stops waiting as soon as its own context ends.
func sharedLoad[T any](ctx context.Context, group *singleflight.Group, key string, timeout time.Duration, load func(context.Context) (T, error)) (T, error) {
	var zero T
	// A caller that has already given up must not start detached work.
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	results := group.DoChan(key, func() (value any, err error) {
		// DoChan re-raises a panic on a fresh goroutine, which no caller can recover.
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.Default().Error("egress_shared_load_panic", "panic", fmt.Sprint(recovered), "stack", string(debug.Stack()))
				value, err = nil, fmt.Errorf("出口共享加载异常中断: %v", recovered)
			}
		}()
		loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
		defer cancel()
		return load(loadCtx)
	})
	select {
	case result := <-results:
		if result.Err != nil {
			return zero, result.Err
		}
		return result.Val.(T), nil
	case <-ctx.Done():
		return zero, ctx.Err()
	}
}
