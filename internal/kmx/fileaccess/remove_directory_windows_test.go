//go:build windows

package fileaccess

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsRemovePrivateDirectory(t *testing.T) {
	path, parent := privateTestDirectory(t)
	child, err := CreatePrivateDirectoryAt(t.Context(), parent, "child")
	if err != nil {
		t.Fatal(err)
	}
	if err := RemovePrivateDirectoryAt(t.Context(), parent, "child", child); err != nil {
		t.Fatalf("remove = %v", err)
	}
	if err := child.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(path, "child")); !os.IsNotExist(err) {
		t.Fatalf("removed directory remains: %v", err)
	}

	child, err = CreatePrivateDirectoryAt(t.Context(), parent, "child")
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := RemovePrivateDirectoryAt(ctx, parent, "child", child); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled removal = %v", err)
	}
	if _, err := os.Stat(filepath.Join(path, "child")); err != nil {
		t.Fatal("cancelled operation changed state", err)
	}
}

func TestWindowsRemovePrivateDirectoryRefusesNonemptyAndSubstitution(t *testing.T) {
	path, parent := privateTestDirectory(t)
	child, err := CreatePrivateDirectoryAt(t.Context(), parent, "child")
	if err != nil {
		t.Fatal(err)
	}
	file, err := CreatePrivateFileAt(t.Context(), child, "entry")
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := RemovePrivateDirectoryAt(t.Context(), parent, "child", child); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("nonempty removal = %v", err)
	}
	if err := os.Remove(filepath.Join(path, "child", "entry")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(path, "child")); err != nil {
		t.Fatal(err)
	}
	if err := child.Close(); err != nil {
		t.Fatal(err)
	}
	replacement, err := CreatePrivateDirectoryAt(t.Context(), parent, "child")
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	other, err := CreatePrivateDirectoryAt(t.Context(), parent, "other")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := RemovePrivateDirectoryAt(t.Context(), parent, "child", other); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("substituted removal = %v", err)
	}
	if _, err := os.Stat(filepath.Join(path, "child")); err != nil {
		t.Fatal("identity mismatch removed replacement", err)
	}
}
