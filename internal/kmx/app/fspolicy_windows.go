//go:build windows

package app

import (
	"errors"
	"fmt"
	"os"
)

func readRunPromptFile(path string) (string, error) {
	return "", fmt.Errorf("read prompt file: native Windows not supported yet: %w", errors.ErrUnsupported)
}
func openKagentFile(dir *os.File, name string) (*os.File, error) {
	return nil, fmt.Errorf("open Kagent file: native Windows not supported yet: %w", errors.ErrUnsupported)
}
func readSuiteEvaluationVerifyFile(path string) ([]byte, error) {
	return nil, fmt.Errorf("read evaluation verification file: native Windows not supported yet: %w", errors.ErrUnsupported)
}
