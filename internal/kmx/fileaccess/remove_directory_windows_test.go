//go:build windows

package fileaccess

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsRemovePrivateDirectoryRemainsUnsupported(t *testing.T) {
	path, parent := privateTestDirectory(t)
	child, err := CreatePrivateDirectoryAt(t.Context(), parent, "child")
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()
	if err := RemovePrivateDirectoryAt(t.Context(), parent, "child", child); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unsupported removal = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := RemovePrivateDirectoryAt(ctx, parent, "child", child); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled removal = %v", err)
	}
	if _, err := os.Stat(filepath.Join(path, "child")); err != nil {
		t.Fatal("unsupported operation changed state", err)
	}
}
