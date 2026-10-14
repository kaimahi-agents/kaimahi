package app

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// imageLiftCluster is transport only. Root release apply owns evidence, consent
// and write ordering; there is no alternate image-lift application entry point.
type imageLiftCluster struct{ app *App }

func (c imageLiftCluster) Call(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	worker := c.app.withRunContext(ctx)
	cmd := worker.Run.Command("kubectl", worker.kubectl(args...)...)
	cmd.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	execute := worker.captureCommand
	if execute == nil {
		execute = func(_ context.Context, cmd *exec.Cmd) error { return cmd.Run() }
	}
	if err := execute(ctx, cmd); err != nil {
		return nil, fmt.Errorf("kubectl operation failed: %w", err)
	}
	return []byte(strings.TrimSpace(stdout.String())), nil
}
