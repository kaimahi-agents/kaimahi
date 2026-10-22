//go:build windows

package config

import (
	"errors"
	"fmt"
)

// LoadDestinations is unavailable until native Windows file access is supported.
func LoadDestinations(root string) (*Destinations, error) {
	return nil, fmt.Errorf("load destinations: native Windows not supported yet: %w", errors.ErrUnsupported)
}
