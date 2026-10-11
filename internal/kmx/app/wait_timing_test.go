package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

// fastWaitTiming changes retry pacing only. Tests that exercise expiry inject
// a deadline at the specific phase, leaving preflight and subprocess setup alone.
// Allow real subprocess reads to settle instead of busy-spawning fake kubectl.
func fastWaitTiming() *waitTiming {
	return &waitTiming{pollInterval: 10 * time.Millisecond}
}

func TestWaitTimingPreservesProductionDeadlines(t *testing.T) {
	a := &App{}
	parent := t.Context()
	ctx, cancel := a.waitContext(parent, "readiness", 5*time.Minute)
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) < 4*time.Minute || time.Until(deadline) > 5*time.Minute {
		t.Fatalf("production deadline changed: %v, present=%v", deadline, ok)
	}
	inherited, stop := a.waitContext(parent, "result", 0)
	defer stop()
	if inherited != parent {
		t.Fatal("parent-bounded phase acquired a new production deadline")
	}
}

func TestWaitTimingInjectionIsLocalAndSurvivesAppCopy(t *testing.T) {
	var phase string
	var duration time.Duration
	a := &App{Run: &run.Runner{}, waitTiming: fastWaitTiming()}
	a.waitTiming.timeout = func(parent context.Context, name string, original time.Duration) (context.Context, context.CancelFunc) {
		phase, duration = name, original
		return context.WithTimeout(parent, time.Millisecond)
	}
	worker := a.withRunContext(t.Context())
	ctx, cancel := worker.waitContext(t.Context(), "admission", 5*time.Minute)
	defer cancel()
	<-ctx.Done()
	if phase != "admission" || duration != 5*time.Minute || !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("phase=%q duration=%v err=%v", phase, duration, ctx.Err())
	}
	other := &App{}
	otherCtx, otherCancel := other.waitContext(t.Context(), "admission", 5*time.Minute)
	defer otherCancel()
	if otherCtx.Err() != nil {
		t.Fatal("test deadline leaked into another App")
	}
	deadline, _ := otherCtx.Deadline()
	if time.Until(deadline) < 4*time.Minute {
		t.Fatal("test deadline changed another App's production default")
	}
}

func TestWaitTimingPauseHonorsCancellation(t *testing.T) {
	for _, a := range []*App{{}, {waitTiming: fastWaitTiming()}} {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := a.pause(ctx, time.Second); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled pause: %v", err)
		}
	}
}

func TestWaitTimingPauseUsesInjectedInterval(t *testing.T) {
	a := &App{waitTiming: fastWaitTiming()}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := a.pause(ctx, time.Minute); err != nil {
		t.Fatalf("injected pause used production interval: %v", err)
	}
}
