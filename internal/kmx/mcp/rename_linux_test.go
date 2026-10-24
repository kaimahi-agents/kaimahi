package mcp

import (
	"errors"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

func forcePublicationFallback(t *testing.T, renameErr, linkErr error) {
	t.Helper()
	oldRename, oldLink := renameNoReplace, linkPublication
	t.Cleanup(func() { renameNoReplace, linkPublication = oldRename, oldLink })
	renameNoReplace = func(int, string, string) error { return renameErr }
	if linkErr != nil {
		linkPublication = func(*os.Root, string, string) error { return linkErr }
	}
}

// Removing either fallback breaks missing-target creation; using ordinary
// rename or non-exclusive copy breaks the existing-target checks.
func TestPublicationFallbacks(t *testing.T) {
	for _, renameErr := range []error{unix.EINVAL, unix.ENOSYS, unix.ENOTSUP} {
		for _, linkErr := range []error{nil, unix.EPERM, unix.EINVAL, unix.ENOSYS, unix.ENOTSUP} {
			t.Run(renameErr.Error()+"/link="+errorLabel(linkErr), func(t *testing.T) {
				forcePublicationFallback(t, renameErr, linkErr)
				t.Run("create-edit", TestFilesCreateEdit)
				t.Run("competing-creators", TestFilesConcurrentCreate)
				t.Run("existing-targets", testPublicationExistingTargets)
				t.Run("private-history-mode", testPublicationPrivateMode)
			})
		}
	}
}

func errorLabel(err error) string {
	if err == nil {
		return "supported"
	}
	return err.Error()
}

func testPublicationExistingTargets(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, symlink := range []bool{false, true} {
		if err := root.WriteFile("source", []byte("new"), 0600); err != nil {
			t.Fatal(err)
		}
		if symlink {
			err = root.Symlink("missing", "target")
		} else {
			err = root.WriteFile("target", []byte("original"), 0600)
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := renameExclusive(root, "source", "target"); !errors.Is(err, os.ErrExist) {
			t.Fatalf("existing target: %v", err)
		}
		if symlink {
			if got, err := root.Readlink("target"); err != nil || got != "missing" {
				t.Fatalf("symlink changed: %q %v", got, err)
			}
		} else if got, err := root.ReadFile("target"); err != nil || string(got) != "original" {
			t.Fatalf("existing bytes changed: %q %v", got, err)
		}
		if got, err := root.ReadFile("source"); err != nil || string(got) != "new" {
			t.Fatalf("source changed on failure: %q %v", got, err)
		}
		if err := root.Remove("target"); err != nil {
			t.Fatal(err)
		}
	}
}

func testPublicationPrivateMode(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := root.WriteFile(".pending", []byte("private history"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := renameExclusive(root, ".pending", "record.json"); err != nil {
		t.Fatal(err)
	}
	info, err := root.Stat("record.json")
	if err != nil || !historyOwned(info, false) {
		t.Fatalf("history must stay private and single-link: %v %v", info, err)
	}
	if got, err := root.ReadFile("record.json"); err != nil || string(got) != "private history" {
		t.Fatalf("history bytes: %q %v", got, err)
	}
	if _, err := root.Lstat(".pending"); !os.IsNotExist(err) {
		t.Fatalf("source not removed: %v", err)
	}
}

func TestPublicationFailedCopyRemovesPartialTarget(t *testing.T) {
	forcePublicationFallback(t, unix.EINVAL, unix.ENOTSUP)
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	// A directory opens and stats successfully, but cannot be copied as bytes.
	if err := root.Mkdir("source", 0700); err != nil {
		t.Fatal(err)
	}
	if err := renameExclusive(root, "source", "target"); !errors.Is(err, unix.EISDIR) {
		t.Fatalf("want directory read error, got %v", err)
	}
	if _, err := root.Lstat("target"); !os.IsNotExist(err) {
		t.Fatalf("partial target left behind: %v", err)
	}
	if info, err := root.Stat("source"); err != nil || !info.IsDir() {
		t.Fatalf("source changed on failure: %v %v", info, err)
	}
}

func TestPublicationOtherErrorsDoNotFallback(t *testing.T) {
	for _, linkFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "rename", true: "link"}[linkFailure], func(t *testing.T) {
			if linkFailure {
				forcePublicationFallback(t, unix.EINVAL, unix.EIO)
			} else {
				forcePublicationFallback(t, unix.EIO, nil)
			}
			root, err := os.OpenRoot(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			if err := root.WriteFile("source", []byte("new"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := renameExclusive(root, "source", "target"); !errors.Is(err, unix.EIO) {
				t.Fatalf("want original error, got %v", err)
			}
			if _, err := root.Lstat("target"); !os.IsNotExist(err) {
				t.Fatalf("unexpected target: %v", err)
			}
		})
	}
}
