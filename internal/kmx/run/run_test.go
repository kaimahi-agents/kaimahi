package run

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunnerContextCancelsActiveCommand(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := &Runner{Context: ctx}
	marker := filepath.Join(t.TempDir(), "started")
	done := make(chan error, 1)
	go func() {
		done <- r.Run(os.Args[0], "-test.run=^TestRunnerBlockingHelper$", "--", "block", marker)
	}()
	deadline := time.Now().Add(10 * time.Second)
	var earlyErr error
	earlyDone := false
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("inspect helper marker: %v", err)
		}
		select {
		case earlyErr = <-done:
			earlyDone = true
			break
		default:
		}
		if earlyDone {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("helper command did not start: %v", earlyErr)
		}
		time.Sleep(time.Millisecond)
	}
	if earlyDone {
		t.Fatalf("helper command did not start: %v", earlyErr)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled command error=%v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancelled command remained active")
	}
}

func TestRunnerBlockingHelper(t *testing.T) {
	for i, arg := range os.Args {
		if arg == "--" && i+2 < len(os.Args) && os.Args[i+1] == "block" {
			if err := os.WriteFile(os.Args[i+2], nil, 0600); err != nil {
				t.Fatal(err)
			}
			for {
				time.Sleep(time.Hour)
			}
		}
	}
}

// Poll is bounded by ATTEMPTS, not by wall-clock — the shell's
// `for _ in $(seq 1 N)`. The difference bites exactly where it matters: each
// check is a real API call, and on a slow path (kind on podman inside a VM)
// those calls are what gets slower, so a wall-clock budget would quietly turn
// "120 tries" into "however few tries fit in 120 seconds" on the machines
// that need the wait to be longest.
func TestPollRunsEveryAttemptHoweverSlowTheCheckIs(t *testing.T) {
	calls := 0
	slow := func() bool {
		calls++
		time.Sleep(5 * time.Millisecond) // each check outlives the interval
		return false
	}
	if Poll(6, time.Millisecond, slow) {
		t.Error("a check that never succeeds must not report success")
	}
	if calls != 6 {
		t.Errorf("check ran %d times, want 6 — the budget is attempts, not elapsed time", calls)
	}
}

func TestPollStopsAtTheFirstSuccess(t *testing.T) {
	calls := 0
	if !Poll(10, time.Millisecond, func() bool { calls++; return calls == 3 }) {
		t.Error("want success on the third try")
	}
	if calls != 3 {
		t.Errorf("check ran %d times after succeeding on the third", calls)
	}
}

// A zero-attempt poll is a poll that never checked; it must not read as
// success.
func TestPollWithNoAttemptsFails(t *testing.T) {
	if Poll(0, time.Millisecond, func() bool { return true }) {
		t.Error("zero attempts must not report success")
	}
}

func TestPollContextCancelsRetryDelay(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	checked := make(chan struct{})
	done := make(chan bool, 1)
	go func() {
		done <- PollContext(ctx, 60, time.Hour, func() bool {
			close(checked) // A second check would panic.
			return false
		})
	}()
	<-checked
	cancel()
	select {
	case ready := <-done:
		if ready {
			t.Fatal("cancelled poll reported success")
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not interrupt retry delay")
	}
}
