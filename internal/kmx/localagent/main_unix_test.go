//go:build !windows

package localagent

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestMain(m *testing.M) {
	// macOS /var is a symlink. Resolve the fixture root before t.TempDir creates
	// state consumed by the production no-symlink readers and worker subprocesses.
	root, err := filepath.EvalSymlinks(os.TempDir())
	if err == nil {
		err = os.Setenv("TMPDIR", root)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "resolve localagent test temporary root:", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}
