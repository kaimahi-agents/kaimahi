//go:build !windows

package run

import (
	"context"
	"os/exec"
)

// Command is an alias for exec.Cmd outside Windows.
type Command = exec.Cmd

func newCommand(ctx context.Context, name string, args ...string) *Command {
	if ctx != nil {
		return exec.CommandContext(ctx, name, args...)
	}
	return exec.Command(name, args...)
}

// Keep Capture's historical Unix error formatting. Windows needs the original
// context cause because its Job Object cancellation replaces a process-only kill.
func commandContextError(context.Context) error { return nil }
