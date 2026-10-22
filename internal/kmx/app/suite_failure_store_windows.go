//go:build windows

package app

import (
	"errors"
	"fmt"
	"os"
)

func safeSuiteFailureDirectory(info os.FileInfo) bool { return false }
func safeSuiteFailureFile(info os.FileInfo) bool      { return false }
func openSuiteFailureStore(workspaceRoot string, create bool) (*suiteFailureStore, error) {
	return nil, fmt.Errorf("open evaluation failure store: native Windows not supported yet: %w", errors.ErrUnsupported)
}
