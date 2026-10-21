package localpreview

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	goruntime "runtime"
	"slices"
	"strings"
	"time"
)

// MaxJournals bounds retained preview runs. Opening a new run first removes
// the oldest runs beyond MaxJournals-1. Each run is one SQLite journal holding
// the instructions, inputs, model identity, answers and terminal records of
// that run's Sessions in cleartext; credentials are never written.
const MaxJournals = 20

var errNoJournal = refuse(RefusedJournal, "no local preview journal exists", "run a local preview first")

var runIDPattern = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{12}$`)

// journalSuffixes are the SQLite files that make up one run's journal.
var journalSuffixes = []string{".db", ".db-wal", ".db-shm", ".db-journal"}

// JournalPath returns the journal file for runID inside dir.
func JournalPath(dir, runID string) (string, error) {
	if !runIDPattern.MatchString(runID) {
		return "", refuse(RefusedJournal, "the run ID is not a local preview run ID", "list runs and select one exactly")
	}
	return filepath.Join(dir, runID+".db"), nil
}

// privateDir validates or creates dir as an owner-only directory without
// following a symlink at dir itself. Existing permissive directories are
// refused rather than changed. Windows relies on the per-user profile ACLs of
// the kmx state directory; mode bits are not meaningful there.
func privateDir(dir string, create bool) error {
	invalid := refuse(RefusedJournal, "the local preview journal directory is not a private directory",
		"use an absolute owner-only directory under the kmx state directory, or remove it so kmx can recreate it")
	if dir == "" || !filepath.IsAbs(dir) {
		return invalid
	}
	info, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) && create {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return refuse(RefusedJournal, "the local preview journal directory could not be created", "check the kmx state directory is writable")
		}
		info, err = os.Lstat(dir)
	}
	if errors.Is(err, fs.ErrNotExist) {
		return errNoJournal
	}
	if err != nil || !info.IsDir() || info.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
		return invalid
	}
	if goruntime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return invalid
	}
	return nil
}

// newJournal creates an empty owner-only journal file for a new run after
// pruning older runs, and returns its run ID and path.
func newJournal(dir string, now time.Time) (string, string, error) {
	if err := privateDir(dir, true); err != nil {
		return "", "", err
	}
	runs, err := ListRuns(dir)
	if err != nil {
		return "", "", err
	}
	for len(runs) >= MaxJournals {
		// Oldest first. A journal still open in another process may fail to
		// delete on Windows; leave it and continue rather than block this run.
		_ = removeRun(dir, runs[0])
		runs = runs[1:]
	}
	var random [6]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", "", refuse(RefusedJournal, "a run ID could not be generated", "retry")
	}
	runID := now.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(random[:])
	path := filepath.Join(dir, runID+".db")
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", "", refuse(RefusedJournal, "the preview journal could not be created", "check the kmx state directory is writable")
	}
	if err := file.Close(); err != nil {
		return "", "", refuse(RefusedJournal, "the preview journal could not be created", "check the kmx state directory is writable")
	}
	return runID, path, nil
}

// ListRuns returns retained run IDs in dir, oldest first. Unrelated entries
// are ignored and never removed.
func ListRuns(dir string) ([]string, error) {
	if err := privateDir(dir, false); err != nil {
		if err == errNoJournal {
			return nil, nil
		}
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, refuse(RefusedJournal, "the local preview journal directory could not be read", "check its permissions")
	}
	var runs []string
	for _, entry := range entries {
		if id, ok := strings.CutSuffix(entry.Name(), ".db"); ok && runIDPattern.MatchString(id) && entry.Type().IsRegular() {
			runs = append(runs, id)
		}
	}
	slices.Sort(runs)
	return runs, nil
}

// Clear deletes one retained run, or every retained run when runID is empty.
// It removes only files named like preview journals and leaves the directory.
func Clear(dir, runID string) error {
	if runID != "" {
		if _, err := JournalPath(dir, runID); err != nil {
			return err
		}
		if err := privateDir(dir, false); err != nil {
			return err
		}
		return removeRun(dir, runID)
	}
	runs, err := ListRuns(dir)
	if err != nil {
		return err
	}
	var errs []error
	for _, id := range runs {
		errs = append(errs, removeRun(dir, id))
	}
	if errors.Join(errs...) != nil {
		return refuse(RefusedJournal, "some local preview journals could not be removed", "close other kmx processes and clear again")
	}
	return nil
}

func removeRun(dir, runID string) error {
	var errs []error
	for _, suffix := range journalSuffixes {
		if err := os.Remove(filepath.Join(dir, runID+suffix)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	if errors.Join(errs...) != nil {
		return refuse(RefusedJournal, "the local preview journal could not be removed", "close other kmx processes and clear again")
	}
	return nil
}
