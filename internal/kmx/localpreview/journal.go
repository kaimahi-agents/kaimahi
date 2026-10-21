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
// the oldest finished runs beyond MaxJournals-1; a run that is still executing
// is never removed, so concurrent runs can briefly exceed the bound. Each run
// is one SQLite journal holding the instructions, inputs, model identity,
// answers and terminal records of that run's Sessions in cleartext;
// credentials are never written.
const MaxJournals = 20

var errNoJournal = refuse(RefusedJournal, "no local preview journal exists", "run a local preview first")

var errRunActive = refuse(RefusedJournal, "the local preview run is still executing", "wait for it to finish, then clear again")

// Run IDs start with a fixed-width UTC timestamp in nanoseconds, so their
// lexical order is creation order.
var runIDPattern = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}\.[0-9]{9}Z-[0-9a-f]{12}$`)

const runIDTimeFormat = "20060102T150405.000000000Z"

// journalSuffixes are the SQLite files that make up one run's journal.
var journalSuffixes = []string{".db", ".db-wal", ".db-shm", ".db-journal"}

// lockSuffix names a run's lock file. An executing run holds an exclusive OS
// lock on it from before its journal exists until after the journal closes;
// the OS releases it if the process exits.
const lockSuffix = ".lock"

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

// runLock is a held run lock. Release unlocks it and removes the lock file.
type runLock struct {
	file *os.File
	path string
}

func (l *runLock) release() {
	if l == nil {
		return
	}
	unlock(l.file)
	_ = l.file.Close()
	_ = os.Remove(l.path)
}

// newJournal creates an empty owner-only journal file for a new run after
// pruning older finished runs, and returns its run ID, path and held lock.
func newJournal(dir string, now time.Time) (string, string, *runLock, error) {
	unwritable := refuse(RefusedJournal, "the preview journal could not be created", "check the kmx state directory is writable")
	if err := privateDir(dir, true); err != nil {
		return "", "", nil, err
	}
	runs, err := ListRuns(dir)
	if err != nil {
		return "", "", nil, err
	}
	// Oldest first. Executing runs are skipped, and a journal still open in
	// another process may fail to delete on Windows; leave either and
	// continue rather than block this run.
	for excess, i := len(runs)-(MaxJournals-1), 0; excess > 0 && i < len(runs); i++ {
		if removeFinishedRun(dir, runs[i]) == nil {
			excess--
		}
	}
	var random [6]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", "", nil, refuse(RefusedJournal, "a run ID could not be generated", "retry")
	}
	runID := now.UTC().Format(runIDTimeFormat) + "-" + hex.EncodeToString(random[:])
	// Lock before the journal exists, so every listed journal without a held
	// lock is finished.
	lock := &runLock{path: filepath.Join(dir, runID+lockSuffix)}
	if lock.file, err = os.OpenFile(lock.path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600); err != nil {
		return "", "", nil, unwritable
	}
	if !tryLock(lock.file) {
		_ = lock.file.Close()
		_ = os.Remove(lock.path)
		return "", "", nil, unwritable
	}
	path := filepath.Join(dir, runID+".db")
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		err = file.Close()
	}
	if err != nil {
		lock.release()
		return "", "", nil, unwritable
	}
	return runID, path, lock, nil
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
// A run that is still executing is refused and kept.
func Clear(dir, runID string) error {
	if runID != "" {
		if _, err := JournalPath(dir, runID); err != nil {
			return err
		}
		if err := privateDir(dir, false); err != nil {
			return err
		}
		return removeFinishedRun(dir, runID)
	}
	runs, err := ListRuns(dir)
	if err != nil {
		return err
	}
	var errs []error
	for _, id := range runs {
		errs = append(errs, removeFinishedRun(dir, id))
	}
	if errors.Join(errs...) != nil {
		return refuse(RefusedJournal, "some local preview journals are executing or could not be removed", "wait for running previews, close other kmx processes and clear again")
	}
	return nil
}

// removeFinishedRun removes runID's journal unless its lock is held. A missing
// lock file means the run finished: runs lock before creating the journal and
// remove the lock file last. A stale lock file left by an exited process is
// lockable and removed with the journal.
func removeFinishedRun(dir, runID string) error {
	lockPath := filepath.Join(dir, runID+lockSuffix)
	lock, err := os.OpenFile(lockPath, os.O_RDWR, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return removeRun(dir, runID)
	}
	if err != nil {
		return errRunActive
	}
	if !tryLock(lock) {
		_ = lock.Close()
		return errRunActive
	}
	err = removeRun(dir, runID)
	unlock(lock)
	_ = lock.Close()
	if removeErr := os.Remove(lockPath); removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) && err == nil {
		err = refuse(RefusedJournal, "the local preview journal could not be removed", "close other kmx processes and clear again")
	}
	return err
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
