package app

import (
	"context"
	"sync/atomic"
	"time"
)

// waitTiming is an invocation-local test seam for retry pacing and phase
// deadlines. No flag, environment variable or configuration populates it.
// Tests shorten the phase they exercise, not the preflight that reaches it.
// App copies retain the hook just as they retain other invocation state.
type waitTiming struct {
	pollInterval time.Duration
	timeout      func(context.Context, string, time.Duration) (context.Context, context.CancelFunc)
}

// waitPausesKey lets a test's timeout hook give a phase context a number of
// pauses (an *atomic.Int32); the pause after them reports the phase deadline
// as passed. A test expecting a wait to run out then ends it after a fixed
// number of complete observations instead of racing a clock. Only honored
// while waitTiming is set.
type waitPausesKey struct{}

func (a *App) waitContext(parent context.Context, phase string, timeout time.Duration) (context.Context, context.CancelFunc) {
	if a.waitTiming != nil && a.waitTiming.timeout != nil {
		return a.waitTiming.timeout(parent, phase, timeout)
	}
	if timeout == 0 {
		// This phase was bounded only by its caller before timing injection.
		return parent, func() {}
	}
	return context.WithTimeout(parent, timeout)
}

func (a *App) pause(ctx context.Context, interval time.Duration) error {
	if a.waitTiming != nil {
		if a.waitTiming.pollInterval > 0 {
			interval = a.waitTiming.pollInterval
		}
		if left, ok := ctx.Value(waitPausesKey{}).(*atomic.Int32); ok && left.Add(-1) < 0 {
			return context.DeadlineExceeded
		}
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
