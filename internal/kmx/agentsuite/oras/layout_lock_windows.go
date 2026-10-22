//go:build windows

package oras

import (
	"context"
	"errors"
	"fmt"
)

func lockLayoutPublication(ctx context.Context, destination string) (func() error, error) {
	return nil, fmt.Errorf("lock layout publication: native Windows not supported yet: %w", errors.ErrUnsupported)
}
