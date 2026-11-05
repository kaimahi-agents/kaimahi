//go:build windows

package app

import (
	"errors"
	"os"
	"testing"
)

func TestRetainedWindowsRefusesBeforeSourceOrState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KMX_HOME", home)
	_, err := (&App{}).PrepareRetainedFile(t.Context(), RetainedPrepareOptions{File: "missing.agent.md", ExpectedRevision: "unused"})
	if !errors.Is(err, errRetainedUnsupported) {
		t.Fatalf("unsupported = %v", err)
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 0 {
		t.Fatal("unsupported platform changed state", err)
	}
}
