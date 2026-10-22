package oras

import (
	"errors"
	"fmt"
)

func finishLayoutPublication(release func() error, operationError *error) {
	if err := release(); err != nil {
		*operationError = errors.Join(*operationError, fmt.Errorf("local layout publication may have completed but writer exclusion cleanup failed: %w", err))
	}
}
