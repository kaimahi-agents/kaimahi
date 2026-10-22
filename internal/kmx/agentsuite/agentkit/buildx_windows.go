//go:build windows

package agentkit

import (
	"context"
	"errors"
	"fmt"
	"io"
)

func (localBuildxRunner) Run(ctx context.Context, stdout io.Writer, stderr io.Writer, name string, args ...string) error {
	return fmt.Errorf("run Docker buildx: native Windows not supported yet: %w", errors.ErrUnsupported)
}
