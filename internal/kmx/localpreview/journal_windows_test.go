//go:build windows

package localpreview

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/fileaccess"
)

func requirePrivateJournalObject(t *testing.T, path string, directory bool) {
	t.Helper()
	if directory {
		opened, err := fileaccess.OpenPrivateDirectory(t.Context(), path)
		if err != nil {
			t.Fatal(err)
		}
		if err := fileaccess.CheckPrivate(t.Context(), opened, true); err != nil {
			_ = opened.Close()
			t.Fatal(err)
		}
		if err := opened.Close(); err != nil {
			t.Fatal(err)
		}
		return
	}
	parent, err := fileaccess.OpenPrivateDirectory(t.Context(), filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	file, err := fileaccess.OpenPrivateFileAt(t.Context(), parent, filepath.Base(path))
	if err != nil {
		_ = parent.Close()
		t.Fatal(err)
	}
	if err := fileaccess.CheckPrivate(t.Context(), file, false); err != nil {
		_ = file.Close()
		_ = parent.Close()
		t.Fatal(err)
	}
	if err := errors.Join(file.Close(), parent.Close()); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsJournalFamilyIsPrivateAndReplayable(t *testing.T) {
	dir := journalDir(t)
	result, err := Test(testContext(t), greeter(t), Options{JournalDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := Inspect(testContext(t), dir, result.RunID)
	if err != nil || len(replay.Sessions) != 1 || replay.Sessions[0].Revision != result.Revision || replay.Sessions[0].Model != result.Model {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	requirePrivateJournalObject(t, dir, true)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		requirePrivateJournalObject(t, filepath.Join(dir, entry.Name()), false)
	}
}

func TestWindowsInterruptedJournalFamilyClearRecovers(t *testing.T) {
	dir := journalDir(t)
	result, err := Test(testContext(t), greeter(t), Options{JournalDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	directory, err := fileaccess.OpenPrivateDirectory(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	companion, err := fileaccess.EnsurePrivateFileAt(t.Context(), directory, result.RunID+".db-wal")
	if err != nil {
		_ = directory.Close()
		t.Fatal(err)
	}
	if err := companion.Close(); err != nil {
		_ = directory.Close()
		t.Fatal(err)
	}
	if err := directory.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, result.RunID+".db")); err != nil {
		t.Fatal(err)
	}
	runs, err := ListRuns(dir)
	if err != nil || len(runs) != 1 || runs[0] != result.RunID {
		t.Fatalf("partial family hidden: %v, %v", runs, err)
	}
	if err := Clear(dir, ""); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("partial family remains: %v, %v", entries, err)
	}
}
