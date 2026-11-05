package app

import (
	"path/filepath"
	"testing"
)

func modelLifecycleFixture(t *testing.T) (*App, map[string]map[string]any, *[]string) {
	t.Helper()
	a, objects, calls := modelConfigureFixture(t)
	// The real store must create its private root; t.TempDir follows the host umask.
	t.Setenv("KMX_HOME", filepath.Join(t.TempDir(), "state"))
	return a, objects, calls
}
