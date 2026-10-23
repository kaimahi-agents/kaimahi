//go:build windows

package app

import (
	"errors"
	"testing"
)

func TestAgentRunProfileWindowsPreflightKeepsUnsupported(t *testing.T) {
	_, err := preflightDetachedExport(t.Context(), "profile.json", false)
	if !errors.Is(err, errors.ErrUnsupported) || !errors.Is(err, errSuitePassingFile) {
		t.Fatalf("Windows refusal lost its unsupported or existing file-safety classification: %v", err)
	}
}
