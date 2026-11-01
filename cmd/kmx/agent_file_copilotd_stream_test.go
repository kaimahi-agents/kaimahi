package main

import (
	"errors"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestCopilotdSnapshotLargeWorkingFile(t *testing.T) {
	root, pair := snapshotFixture(t)
	path := filepath.Join(root, "large")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Truncate(33 << 20); err != nil {
		t.Fatal(err)
	}
	f.Close()
	snap, err := makeCopilotdSnapshot(t.Context(), pair, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Close(t.Context())
	info, err := os.Stat(filepath.Join(snap.Directory, "large"))
	if err != nil || info.Size() != 33<<20 {
		t.Fatalf("%v %v", info, err)
	}
}
func TestCopilotdSnapshotSymlinkTraversal(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "repo")
	destination := filepath.Join(base, "copy", "snap")
	os.MkdirAll(filepath.Join(root, "a"), 0700)
	os.MkdirAll(destination, 0700)
	os.Symlink("..", filepath.Join(root, "a", "d"))
	os.Symlink("d/../../outside", filepath.Join(root, "a", "x"))
	if err := copyCopilotdEntry(t.Context(), root, destination, "a/d", io.Discard, nil); err != nil {
		t.Fatal(err)
	}
	if err := copyCopilotdEntry(t.Context(), root, destination, "a/x", io.Discard, nil); !errors.Is(err, agentpreview.ErrSnapshotUnavailable) {
		t.Fatalf("escaping link copied: %v", err)
	}
}

func TestCopilotdSnapshotGlobalIgnore(t *testing.T) {
	root, pair := snapshotFixture(t)
	home := t.TempDir()
	ignore := filepath.Join(home, "ignore")
	os.WriteFile(ignore, []byte("local-only\n"), 0600)
	os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[core]\nexcludesFile = "+ignore+"\n"), 0600)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	snapshotWrite(t, root, "local-only", "not for the copied repo")
	snap, err := makeCopilotdSnapshot(t.Context(), pair, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Close(t.Context())
	if _, err := os.Stat(filepath.Join(snap.Directory, "local-only")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("global ignored file copied: %v", err)
	}
}

func TestCopilotdSnapshotEmptyGitmodules(t *testing.T) {
	root, pair := snapshotFixture(t)
	snapshotWrite(t, root, ".gitmodules", "# no submodules\n")
	snap, err := makeCopilotdSnapshot(t.Context(), pair, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Close(t.Context())
}
