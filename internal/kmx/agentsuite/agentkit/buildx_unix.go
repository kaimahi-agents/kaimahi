//go:build !windows

package agentkit

import (
	"context"
	"io"
	"os"
	"os/exec"
	"time"
)

func (localBuildxRunner) Run(
	ctx context.Context,
	stdout io.Writer,
	stderr io.Writer,
	name string,
	args ...string,
) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Cancel = func() error {
		return command.Process.Signal(os.Interrupt)
	}
	command.WaitDelay = 10 * time.Second
	command.Stdout = stdout
	command.Stderr = stderr
	return command.Run()
}
