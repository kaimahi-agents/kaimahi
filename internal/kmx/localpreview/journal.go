package localpreview

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/fileaccess"
)

// MaxJournals bounds retained preview runs. Opening a new run first removes
// the oldest finished runs beyond MaxJournals-1; a run that is still executing
// is never removed. Executing runs, and starts that overlap and count the same
// runs, can leave more than MaxJournals runs until the next run starts or
// Clear runs. Each run is one SQLite journal holding the instructions, inputs,
// model identity, answers and terminal records of that run's Sessions in
// cleartext; credentials are never written.
const MaxJournals = 20

var errNoJournal = refuse(RefusedJournal, "no local preview journal exists", "run a local preview first")

var errRunActive = refuse(RefusedJournal, "the local preview run is still executing", "wait for it to finish, then clear again")

var errLockCleared = refuse(RefusedJournal, "a concurrent clear removed the new run's lock before it was held", "run the preview again")

// lockFileOpened runs between creating and locking a run lock file. Tests
// replace it to interleave a concurrent Clear.
var lockFileOpened = func(string) {}

// lockReleased runs right after a held run lock is closed. Tests replace it to
// interleave a starting run with clear.
var lockReleased = func(string) {}

var syncJournalDirectory = fileaccess.SyncDirectory

// Run IDs start with a fixed-width UTC timestamp in nanoseconds, so their
// lexical order is creation order.
var runIDPattern = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}\.[0-9]{9}Z-[0-9a-f]{12}$`)

const runIDTimeFormat = "20060102T150405.000000000Z"

// journalSuffixes are the SQLite files that make up one run's journal.
var journalSuffixes = []string{".db", ".db-wal", ".db-shm", ".db-journal"}

// journalCreateSuffixes are every known SQLite family file. Precreating them
// privately prevents SQLite from inheriting broader platform defaults.
var journalCreateSuffixes = journalSuffixes

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

// privateDir validates or creates dir as a checked owner-only directory.
// Existing permissive directories are refused rather than changed.
func privateDir(dir string, create bool) error {
	invalid := refuse(RefusedJournal, "the local preview journal directory is not a private directory",
		"use an absolute owner-only directory under the kmx state directory, or remove it so kmx can recreate it")
	if dir == "" || !filepath.IsAbs(dir) {
		return invalid
	}
	var opened *fileaccess.Directory
	var err error
	if create {
		opened, err = fileaccess.EnsurePrivateDirectory(context.Background(), dir)
	} else {
		opened, err = fileaccess.OpenPrivateDirectory(context.Background(), dir)
	}
	if !create && errors.Is(err, fileaccess.ErrMissing) {
		return errNoJournal
	}
	if err != nil {
		return invalid
	}
	if err := fileaccess.CheckPrivate(context.Background(), opened, true); err != nil {
		_ = opened.Close()
		return invalid
	}
	if err := opened.Close(); err != nil {
		return invalid
	}
	return nil
}

func openJournalDirectory(dir string, create bool) (*fileaccess.Directory, error) {
	if err := privateDir(dir, create); err != nil {
		return nil, err
	}
	opened, err := fileaccess.OpenPrivateDirectory(context.Background(), dir)
	if err != nil {
		return nil, refuse(RefusedJournal, "the local preview journal directory could not be opened", "check its permissions")
	}
	return opened, nil
}

// runLock is a held run lock. Release closes it and removes the exact lock
// entry only while no later opener holds that entry.
type runLock struct {
	file      *fileaccess.PrivateFile
	directory *fileaccess.Directory
	path      string
	name      string
}

func (l *runLock) release() error {
	if l == nil || l.file == nil {
		return nil
	}
	file, directory := l.file, l.directory
	l.file, l.directory = nil, nil
	removeErr := fileaccess.RemovePrivateFileAt(context.Background(), directory, l.name, file)
	syncErr := error(nil)
	if removeErr == nil {
		syncErr = syncJournalDirectory(context.Background(), directory)
	}
	fileErr := file.Close()
	lockReleased(l.path)
	directoryErr := directory.Close()
	if errors.Join(removeErr, syncErr, fileErr, directoryErr) != nil {
		return refuse(RefusedJournal, "the local preview run lock cleanup is unconfirmed", "inspect retained journals before retrying")
	}
	return nil
}

func (l *runLock) discardRun() {
	if l == nil || l.file == nil {
		return
	}
	if removeRun(l.directory, strings.TrimSuffix(l.name, lockSuffix)) != nil {
		_ = l.file.Close()
		_ = l.directory.Close()
		l.file, l.directory = nil, nil
		return
	}
	_ = l.release()
}

// newJournal creates an empty owner-only journal file for a new run after
// pruning older finished runs, and returns its run ID, path and held lock.
func newJournal(dir string, now time.Time) (string, string, *runLock, error) {
	unwritable := refuse(RefusedJournal, "the preview journal could not be created", "check the kmx state directory is writable")
	directory, err := openJournalDirectory(dir, true)
	if err != nil {
		return "", "", nil, err
	}
	defer func() {
		if directory != nil {
			_ = directory.Close()
		}
	}()
	runs, err := listRunsDirectory(directory)
	if err != nil {
		return "", "", nil, err
	}
	// Oldest first. Executing or unsafe runs are skipped rather than blocking
	// this run; a later start or explicit clear can recover them.
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
	lockName := runID + lockSuffix
	lockFile, err := fileaccess.CreatePrivateFileAt(context.Background(), directory, lockName)
	if err != nil {
		return "", "", nil, unwritable
	}
	lock := &runLock{file: lockFile, directory: directory, path: filepath.Join(dir, lockName), name: lockName}
	directory = nil
	lockFileOpened(lock.path)
	if err := fileaccess.TryLock(context.Background(), lock.file); err != nil {
		_ = lock.release()
		return "", "", nil, unwritable
	}
	if !lockStillLinked(lock) {
		_ = lock.release()
		return "", "", nil, errLockCleared
	}
	for _, suffix := range journalCreateSuffixes {
		journal, createErr := fileaccess.CreatePrivateFileAt(context.Background(), lock.directory, runID+suffix)
		if createErr == nil {
			createErr = journal.Close()
		}
		if createErr != nil {
			_ = removeRun(lock.directory, runID)
			_ = lock.release()
			return "", "", nil, unwritable
		}
	}
	if syncJournalDirectory(context.Background(), lock.directory) != nil {
		_ = removeRun(lock.directory, runID)
		_ = lock.release()
		return "", "", nil, unwritable
	}
	return runID, filepath.Join(dir, runID+".db"), lock, nil
}

// lockStillLinked reports whether lock's path still names its open object.
func lockStillLinked(lock *runLock) bool {
	if lock == nil || lock.file == nil || lock.directory == nil {
		return false
	}
	linked, err := fileaccess.OpenPrivateFileAt(context.Background(), lock.directory, lock.name)
	if err != nil {
		return false
	}
	want, wantErr := fileaccess.PhysicalIdentity(context.Background(), lock.file)
	got, gotErr := fileaccess.PhysicalIdentity(context.Background(), linked)
	closeErr := linked.Close()
	return wantErr == nil && gotErr == nil && closeErr == nil && want == got
}

// ListRuns returns retained run IDs in dir, oldest first. A recognized partial
// journal family remains visible for explicit recovery. Unrelated entries are
// ignored and never removed.
func ListRuns(dir string) ([]string, error) {
	directory, err := openJournalDirectory(dir, false)
	if errors.Is(err, errNoJournal) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	return listRunsDirectory(directory)
}

func listRunsDirectory(directory *fileaccess.Directory) ([]string, error) {
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return nil, refuse(RefusedJournal, "the local preview journal directory could not be read", "check its permissions")
	}
	seen := map[string]bool{}
	var runs []string
	for _, entry := range entries {
		id, ok := journalEntryRunID(entry.Name())
		if !ok || seen[id] {
			continue
		}
		seen[id] = true
		runs = append(runs, id)
	}
	slices.Sort(runs)
	return runs, nil
}

func journalEntryRunID(name string) (string, bool) {
	for _, suffix := range append(slices.Clone(journalSuffixes), lockSuffix) {
		if id, ok := strings.CutSuffix(name, suffix); ok && runIDPattern.MatchString(id) {
			return id, true
		}
	}
	return "", false
}

// Clear deletes one retained run, or every retained run when runID is empty.
// It removes only validated files in a recognized run family and leaves the
// directory. A run that is still executing is refused and kept.
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

// removeFinishedRun removes runID's validated journal family unless its lock
// is held. A missing lock means the run finished. A stale lock left by an
// exited process is lockable and removed with the family.
func removeFinishedRun(dir, runID string) error {
	directory, err := openJournalDirectory(dir, false)
	if err != nil {
		return err
	}
	defer directory.Close()
	lockName := runID + lockSuffix
	lock, err := fileaccess.OpenPrivateFileAt(context.Background(), directory, lockName)
	if err != nil && !errors.Is(err, fileaccess.ErrMissing) {
		return errRunActive
	}
	if err == nil {
		if fileaccess.TryLock(context.Background(), lock) != nil {
			_ = lock.Close()
			return errRunActive
		}
	}
	if err := removeRun(directory, runID); err != nil {
		if lock != nil {
			_ = lock.Close()
		}
		return err
	}
	if lock != nil {
		if removeErr := fileaccess.RemovePrivateFileAt(context.Background(), directory, lockName, lock); removeErr != nil && !errors.Is(removeErr, fileaccess.ErrMissing) {
			_ = lock.Close()
			return refuse(RefusedJournal, "the local preview journal could not be removed", "close other kmx processes and clear again")
		}
		if closeErr := lock.Close(); closeErr != nil {
			return refuse(RefusedJournal, "the local preview journal could not be removed", "close other kmx processes and clear again")
		}
		lockReleased(filepath.Join(dir, lockName))
	}
	if syncJournalDirectory(context.Background(), directory) != nil {
		return refuse(RefusedJournal, "the local preview journal removal is unconfirmed", "inspect retained journals before retrying")
	}
	return nil
}

func removeRun(directory *fileaccess.Directory, runID string) error {
	for _, suffix := range journalSuffixes {
		name := runID + suffix
		file, err := fileaccess.OpenPrivateFileAt(context.Background(), directory, name)
		if errors.Is(err, fileaccess.ErrMissing) {
			continue
		}
		if err != nil {
			return refuse(RefusedJournal, "the local preview journal could not be removed", "close other kmx processes and clear again")
		}
		removeErr := fileaccess.RemovePrivateFileAt(context.Background(), directory, name, file)
		closeErr := file.Close()
		if removeErr != nil || closeErr != nil {
			return refuse(RefusedJournal, "the local preview journal could not be removed", "close other kmx processes and clear again")
		}
	}
	return nil
}
