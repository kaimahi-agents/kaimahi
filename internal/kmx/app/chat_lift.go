package app

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"time"
)

type chatLiftTarget struct{ Subscription string }

// Azure subprocesses are prepared by the Runner and then given their own
// deadline, the same shape orkaCapture uses for kubectl. Preparing through the
// Runner is what carries Runner.Env and Runner.Unset — a raw exec.Command
// inherits the process environment but cannot take a variable away, and a
// worker that removed one has a reason. Nothing is echoed: these calls run
// underneath a loading pane, and Capture's rule already says a read that
// printed every query would be unreadable.
//
// Raw stderr is never propagated: az reports subscription and resource
// identifiers in its diagnostics.

func (a *App) liftDiscovery(ctx context.Context, command string, args ...string) ([]byte, error) {
	return a.azureCapture(ctx, 30*time.Second, command, args,
		fmt.Sprintf("%s discovery failed; check login and read access", command))
}

// liftAzureWrite carries the longer deadline Azure writes need. Key reads go
// through liftDiscovery instead, which is why its output is bounded and never
// echoed.

func (a *App) azureCapture(ctx context.Context, timeout time.Duration, command string, args []string, failure string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	prepared := a.Run.Command(command, args...)
	cmd := exec.CommandContext(callCtx, prepared.Path, prepared.Args[1:]...)
	cmd.Env = prepared.Env
	cmd.WaitDelay = time.Second
	out := &orkaBoundedBuffer{remaining: 4 << 20}
	cmd.Stdout, cmd.Stderr = out, io.Discard
	if err := cmd.Run(); err != nil {
		if callCtx.Err() != nil {
			return nil, callCtx.Err()
		}
		return nil, fmt.Errorf("%s", failure)
	}
	return out.buffer.Bytes(), nil
}
