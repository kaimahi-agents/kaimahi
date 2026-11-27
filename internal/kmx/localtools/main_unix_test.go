//go:build unix

package localtools

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestMain(m *testing.M) {
	// Helper subprocesses deliberately test exact/empty launch environments.
	if slices.Contains(os.Args, "--") {
		os.Exit(m.Run())
	}
	// macOS /var is a symlink. Resolve fixture roots, retaining the production
	// readers' no-symlink checks and tests that explicitly construct unsafe paths.
	root, err := filepath.EvalSymlinks(os.TempDir())
	if err == nil {
		err = os.Setenv("TMPDIR", root)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "resolve localtools test temporary root:", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}
