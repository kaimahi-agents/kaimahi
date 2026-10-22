//go:build windows

package workspace

import (
	"context"
	"errors"
	"fmt"
)

func privateTree(root string, setMode bool) error {
	return fmt.Errorf("check private workspace tree: native Windows not supported yet: %w", errors.ErrUnsupported)
}
func createSourceFiles(ctx context.Context, root string, files map[string][]byte) error {
	return fmt.Errorf("create workspace source files: native Windows not supported yet: %w", errors.ErrUnsupported)
}
func writeFiles(root string, files map[string][]byte) error {
	return fmt.Errorf("write workspace files: native Windows not supported yet: %w", errors.ErrUnsupported)
}
