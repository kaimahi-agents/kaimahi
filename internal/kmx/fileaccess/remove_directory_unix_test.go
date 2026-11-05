//go:build !windows

package fileaccess

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRemovePrivateDirectoryIdentity(t *testing.T) {
	for _, kind := range []string{"empty", "replacement", "nonempty", "symlink", "traversal", "missing", "unsafe", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			path, parent := privateTestDirectory(t)
			child, err := CreatePrivateDirectoryAt(t.Context(), parent, "child")
			if err != nil {
				t.Fatal(err)
			}
			defer child.Close()
			target := filepath.Join(path, "child")
			name := "child"
			ctx := t.Context()
			switch kind {
			case "replacement", "symlink":
				if err := os.Rename(target, target+"-original"); err != nil {
					t.Fatal(err)
				}
				if kind == "replacement" {
					err = os.Mkdir(target, 0700)
				} else {
					err = os.Symlink(target+"-original", target)
				}
				if err != nil {
					t.Fatal(err)
				}
			case "nonempty":
				if err := os.WriteFile(filepath.Join(target, "witness"), []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
			case "traversal":
				name = "../child"
			case "missing":
				name = "absent"
			case "unsafe":
				if err := os.Chmod(target, 0755); err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			err = RemovePrivateDirectoryAt(ctx, parent, name, child)
			if kind == "empty" {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := os.Lstat(target); !os.IsNotExist(err) {
					t.Fatalf("child remains: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("unsafe removal succeeded")
			}
			if kind == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation = %v", err)
			}
			if _, err := os.Lstat(target); err != nil {
				t.Fatalf("refusal removed child: %v", err)
			}
			if kind == "nonempty" {
				if b, err := os.ReadFile(filepath.Join(target, "witness")); err != nil || string(b) != "keep" {
					t.Fatal("witness changed", err)
				}
			}
		})
	}
}

func TestRemovePrivateDirectoryRequiredFlushFailure(t *testing.T) {
	path, parent := privateTestDirectory(t)
	child, err := CreatePrivateDirectoryAt(t.Context(), parent, "child")
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()
	old := syncDirectoryHandle
	injected := errors.New("private flush failure")
	syncDirectoryHandle = func(*os.File) error { return injected }
	t.Cleanup(func() { syncDirectoryHandle = old })
	if err := RemovePrivateDirectoryAt(t.Context(), parent, "child", child); !errors.Is(err, ErrUnreadable) || errors.Is(err, injected) {
		t.Fatalf("required flush = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(path, "child")); !os.IsNotExist(err) {
		t.Fatalf("deletion effect lost: %v", err)
	}
	if err := RemovePrivateDirectoryAt(t.Context(), parent, "child", child); !errors.Is(err, ErrMissing) {
		t.Fatalf("missing deletion is not success: %v", err)
	}
}
