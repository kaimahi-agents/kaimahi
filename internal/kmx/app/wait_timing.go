package app

import (
	"context"
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
	if a.waitTiming != nil && a.waitTiming.pollInterval > 0 {
		interval = a.waitTiming.pollInterval
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
