//go:build !windows

package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
	"golang.org/x/sys/unix"
)

// openSessionReferenceDirectory walks from the volume root one component at a
// time. Each handle is anchored before descending; symlinks are never accepted,
// even if they point inside the same tree. Sticky shared ancestors such as /tmp
// are allowed, but the immediate parent must not be group/other-writable.
func openSessionReferenceDirectory(path string) (*os.Root, string, os.FileInfo, error) {
	if path == "" || strings.IndexByte(path, 0) >= 0 {
		return nil, "", nil, errSessionReferenceDirectory
	}
	absolute, err := filepath.Abs(path)
	if err != nil || filepath.Base(absolute) == "." || filepath.Base(absolute) == string(filepath.Separator) {
		return nil, "", nil, errSessionReferenceDirectory
	}
	parent := filepath.Dir(absolute)
	volume := filepath.VolumeName(parent) + string(filepath.Separator)
	root, err := os.OpenRoot(volume)
	if err != nil {
		return nil, "", nil, errSessionReferenceDirectory
	}
	parts := strings.Split(strings.TrimPrefix(parent, volume), string(filepath.Separator))
	for _, part := range parts {
		if part == "" {
			continue
		}
		info, err := root.Lstat(part)
		if err != nil || !safeSessionReferenceDirectory(info, false) {
			root.Close()
			return nil, "", nil, errSessionReferenceDirectory
		}
		next, err := root.OpenRoot(part)
		if err != nil {
			root.Close()
			return nil, "", nil, errSessionReferenceDirectory
		}
		opened, err := next.Stat(".")
		current, currentErr := root.Lstat(part)
		root.Close()
		if err != nil || currentErr != nil || !safeSessionReferenceDirectory(current, false) || !os.SameFile(info, opened) || !os.SameFile(info, current) {
			next.Close()
			return nil, "", nil, errSessionReferenceDirectory
		}
		root = next
	}
	info, err := root.Stat(".")
	if err != nil || !safeSessionReferenceDirectory(info, true) {
		root.Close()
		return nil, "", nil, errSessionReferenceDirectory
	}
	return root, filepath.Base(absolute), info, nil
}

func safeSessionReferenceDirectory(info os.FileInfo, immediate bool) bool {
	return info != nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 &&
		(info.Mode().Perm()&0022 == 0 || !immediate && info.Mode()&os.ModeSticky != 0)
}

func safeSessionReferenceFile(info os.FileInfo) bool {
	return info != nil && info.Mode().IsRegular() && info.Mode().Perm()&0077 == 0 && info.Size() <= maxSessionReferenceManifestBytes
}

// The lock is deliberately exclusive-create rather than advisory inode locking:
// cooperative writers never race a removed/recreated lock inode. A stale lock
// causes bounded refusal; it is never automatically broken by another writer.
func lockSessionReferenceManifest(ctx context.Context, root *os.Root, name string) (func(), error) {
	ctx, cancel := context.WithTimeout(ctx, sessionReferenceAppendTimeout)
	defer cancel()
	for {
		if ctx.Err() != nil {
			return nil, errSessionReferenceAppend
		}
		file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err == nil {
			info, statErr := file.Stat()
			closeErr := file.Close()
			if statErr != nil || closeErr != nil {
				_ = root.Remove(name)
				return nil, errSessionReferenceAppend
			}
			return func() {
				if current, err := root.Lstat(name); err == nil && os.SameFile(info, current) && safeSessionReferenceFile(current) {
					_ = root.Remove(name)
				}
			}, nil
		}
		if !os.IsExist(err) {
			return nil, errSessionReferenceAppend
		}
		info, err := root.Lstat(name)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || !safeSessionReferenceFile(info) {
			return nil, errSessionReferenceAppend
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, errSessionReferenceAppend
		case <-timer.C:
		}
	}
}

// Recheck original identity and exact bytes immediately before atomic rename.
// This protects cooperative writers and detects intervening replacement or
// in-place edits; it does not claim exclusion against a malicious same-user writer.
func replaceSessionReferenceManifest(root *os.Root, name, stage string, original os.FileInfo, originalRaw []byte) error {
	if original == nil {
		if _, err := root.Lstat(name); !os.IsNotExist(err) {
			return errSessionReferenceChanged
		}
	} else {
		raw, current, err := readSessionReferenceFile(root, name)
		if err != nil || !os.SameFile(original, current) || !bytes.Equal(raw, originalRaw) {
			return errSessionReferenceChanged
		}
	}
	if err := root.Rename(stage, name); err != nil {
		return errSessionReferenceAppend
	}
	return nil
}

// Creation walks anchored directories without following links or changing
// existing permissions. Only managed children must be private; existing KMX
// settings directories can retain their normal, non-writable-to-others modes.
func ensureSuitePassDirectory(path string, private bool) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return errSuitePassStore
	}
	volume := filepath.VolumeName(absolute) + string(filepath.Separator)
	root, err := os.OpenRoot(volume)
	if err != nil {
		return errSuitePassStore
	}
	for _, part := range strings.Split(strings.TrimPrefix(absolute, volume), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		info, err := root.Lstat(part)
		if os.IsNotExist(err) {
			if err := root.Mkdir(part, 0700); err != nil && !os.IsExist(err) {
				root.Close()
				return errSuitePassStore
			}
			info, err = root.Lstat(part)
		}
		if err != nil || !safeSessionReferenceDirectory(info, false) {
			root.Close()
			return errSuitePassStore
		}
		next, err := root.OpenRoot(part)
		if err != nil {
			root.Close()
			return errSuitePassStore
		}
		opened, openErr := next.Stat(".")
		current, currentErr := root.Lstat(part)
		closeErr := root.Close()
		if openErr != nil || currentErr != nil || closeErr != nil || !os.SameFile(info, opened) || !os.SameFile(info, current) {
			next.Close()
			return errSuitePassStore
		}
		root = next
	}
	info, err := root.Stat(".")
	closeErr := root.Close()
	if err != nil || closeErr != nil || !safeSessionReferenceDirectory(info, true) || private && info.Mode().Perm()&0077 != 0 {
		return errSuitePassStore
	}
	return nil
}

func openSuitePassStore(ctx context.Context, workspaceRoot, cache, suiteDigest string) (_ *suitePassStore, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !suitePrefixedDigest(suiteDigest) {
		return nil, errSuitePassStore
	}
	workspaceRoot, err = filepath.EvalSymlinks(workspaceRoot)
	if err != nil {
		return nil, errSuitePassStore
	}
	workspaceRoot, err = filepath.Abs(workspaceRoot)
	if err != nil {
		return nil, errSuitePassStore
	}
	state, err := config.StateDir()
	if err != nil {
		return nil, err
	}
	state, err = filepath.Abs(state)
	if err != nil {
		return nil, errSuitePassStore
	}
	identity := fmt.Sprintf("%x", sha256.Sum256([]byte(workspaceRoot)))
	base := filepath.Join(state, "passing")
	path := filepath.Join(base, identity)
	if scaffold.RefuseKeyShapes(path) != nil || suiteReceiptInside(path, workspaceRoot) || suiteReceiptInside(path, cache) {
		return nil, errSuitePassStore
	}
	for i, p := range []string{state, base, path} {
		if err := ensureSuitePassDirectory(p, i > 0); err != nil {
			return nil, err
		}
	}
	root, _, _, err := openSessionReferenceDirectory(filepath.Join(path, "lock"))
	if err != nil {
		return nil, errSuitePassStore
	}
	s := &suitePassStore{workspace: workspaceRoot, cache: cache, path: path, partition: filepath.Join(path, strings.TrimPrefix(suiteDigest, "sha256:")), identity: identity, suiteDigest: suiteDigest, root: root}
	defer func() {
		if err != nil {
			s.Close()
		}
	}()
	lock, err := root.OpenFile("lock", os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		directory, e := root.Open(".")
		if e != nil {
			return nil, errSuitePassStore
		}
		fd, e := unix.Openat(int(directory.Fd()), "lock", unix.O_RDWR|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		closeErr := directory.Close()
		if e != nil || closeErr != nil {
			if e == nil {
				unix.Close(fd)
			}
			return nil, errSuitePassStore
		}
		lock = os.NewFile(uintptr(fd), "lock")
		err = nil
	}
	if err != nil {
		return nil, errSuitePassStore
	}
	s.lock = lock
	s.lockInfo, err = lock.Stat()
	if err != nil || !s.lockInfo.Mode().IsRegular() || s.lockInfo.Mode().Perm()&0077 != 0 {
		return nil, errSuitePassStore
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return nil, errors.New("another workspace eval or push is active; retry after it exits (no stale lock deletion is needed)")
	}
	if err := ensureSuitePassDirectory(s.partition, true); err != nil {
		return nil, err
	}
	if err := s.unchanged(); err != nil {
		return nil, err
	}
	return s, nil
}

func openReleaseLiftStorage(ctx context.Context) (_ *releaseLiftRecordWriter, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	state, err := config.StateDir()
	if err != nil {
		return nil, releaseLiftRecordError("unsafe storage location")
	}
	base := filepath.Join(state, "release-lifts")
	for i, path := range []string{state, base} {
		if ensureSuitePassDirectory(path, i > 0) != nil {
			return nil, releaseLiftRecordError("unsafe storage directory")
		}
	}
	root, _, _, err := openSessionReferenceDirectory(filepath.Join(base, "lock"))
	if err != nil {
		return nil, releaseLiftRecordError("unsafe storage directory")
	}
	w := &releaseLiftRecordWriter{root: root, path: base}
	defer func() {
		if err != nil {
			err = errors.Join(err, w.Close())
		}
	}()
	dir, err := root.Open(".")
	if err != nil {
		return nil, releaseLiftRecordError("unsafe storage directory")
	}
	fd, openErr := unix.Openat(int(dir.Fd()), "lock", unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0600)
	closeErr := dir.Close()
	if openErr != nil {
		return nil, releaseLiftRecordError("unsafe storage lock")
	}
	w.lock = os.NewFile(uintptr(fd), "lock")
	if closeErr != nil {
		return nil, releaseLiftRecordError("unsafe storage directory")
	}
	w.lockInfo, err = w.lock.Stat()
	if err != nil || !w.lockInfo.Mode().IsRegular() || w.lockInfo.Mode().Perm()&0077 != 0 {
		return nil, releaseLiftRecordError("unsafe storage lock")
	}
	if unix.Flock(int(w.lock.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil {
		return nil, releaseLiftRecordError("another Lift record reservation is active")
	}
	if err := w.unchanged(); err != nil {
		return nil, err
	}
	if err := w.capacity(ctx); err != nil {
		return nil, err
	}
	return w, nil
}
