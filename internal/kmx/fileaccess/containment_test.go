//go:build !windows

package fileaccess

import (
	"path/filepath"
	"testing"
)

func TestPhysicalContainmentUnixLexicalContract(t *testing.T) {
	root := t.TempDir()
	inside, err := PhysicallyContains(t.Context(), root, filepath.Join(root, "new", "file"))
	if err != nil || !inside {
		t.Fatalf("inside = %t, %v", inside, err)
	}
	outside, err := PhysicallyContains(t.Context(), root, filepath.Join(filepath.Dir(root), "other"))
	if err != nil || outside {
		t.Fatalf("outside = %t, %v", outside, err)
	}
}
