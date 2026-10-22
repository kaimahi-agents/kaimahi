//go:build windows

package toolchain

import (
	"errors"
	"fmt"
)

func install(path string, binary []byte) error {
	return fmt.Errorf("install managed tool: native Windows not supported yet: %w", errors.ErrUnsupported)
}
func relink(link, target string) error {
	return fmt.Errorf("link managed tool: native Windows not supported yet: %w", errors.ErrUnsupported)
}
