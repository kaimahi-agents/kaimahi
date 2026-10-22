//go:build windows

package oras

import (
	"context"
	"errors"
	"fmt"
	"io"
)

func extractContentLayer(ctx context.Context, src io.Reader, destination string) error {
	return fmt.Errorf("extract AgentSuite content: native Windows not supported yet: %w", errors.ErrUnsupported)
}
