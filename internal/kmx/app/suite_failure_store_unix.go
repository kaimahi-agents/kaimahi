//go:build !windows

package app

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"golang.org/x/sys/unix"
)

func suiteFailureOwned(info os.FileInfo) bool {
	if info == nil {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}

func safeSuiteFailureDirectory(info os.FileInfo) bool {
	return suiteFailureOwned(info) && info.IsDir() && info.Mode().Perm() == 0700
}

func safeSuiteFailureFile(info os.FileInfo) bool {
	if !suiteFailureOwned(info) || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > maxSuiteFailureRecordBytes {
		return false
	}
	return info.Sys().(*syscall.Stat_t).Nlink == 1
}

func openSuiteFailureStore(workspaceRoot string, create bool) (_ *suiteFailureStore, err error) {
	workspaceRoot, err = filepath.EvalSymlinks(workspaceRoot)
	if err != nil {
		return nil, errSuiteFailureStore
	}
	workspaceRoot, err = filepath.Abs(workspaceRoot)
	if err != nil {
		return nil, errSuiteFailureStore
	}
	info, e := os.Stat(workspaceRoot)
	if e != nil || !info.IsDir() {
		return nil, errSuiteFailureStore
	}
	state, e := config.StateDir()
	if e != nil {
		return nil, errSuiteFailureStore
	}
	state, e = filepath.Abs(state)
	if e != nil {
		return nil, errSuiteFailureStore
	}
	base := filepath.Join(state, "eval-failures")
	path := filepath.Join(base, fmt.Sprintf("%x", sha256.Sum256([]byte(workspaceRoot))))
	if suiteReceiptInside(base, workspaceRoot) {
		return nil, errSuiteFailureStore
	}
	if create {
		for i, p := range []string{state, base, path} {
			if ensureSuitePassDirectory(p, i > 0) != nil {
				return nil, errSuiteFailureStore
			}
		}
	}
	for _, p := range []string{base, path} {
		info, e := os.Lstat(p)
		if os.IsNotExist(e) && !create {
			return nil, errSuiteFailureMissing
		}
		if e != nil || !safeSuiteFailureDirectory(info) {
			return nil, errSuiteFailureStore
		}
	}
	root, _, _, e := openSessionReferenceDirectory(filepath.Join(path, "lock"))
	if e != nil {
		return nil, errSuiteFailureStore
	}
	s := &suiteFailureStore{root: root, path: path, writeFile: (*os.File).Write, closeFile: (*os.File).Close, rename: root.Rename}
	defer func() {
		if err != nil {
			_ = s.Close()
		}
	}()
	dir, e := root.Open(".")
	if e != nil {
		return nil, errSuiteFailureStore
	}
	flags := unix.O_RDWR | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_CLOEXEC
	if create {
		flags |= unix.O_CREAT
	}
	fd, e := unix.Openat(int(dir.Fd()), "lock", flags, 0600)
	closeErr := dir.Close()
	if e != nil || closeErr != nil {
		if e == nil {
			_ = unix.Close(fd)
		}
		return nil, errSuiteFailureStore
	}
	s.lock = os.NewFile(uintptr(fd), "lock")
	s.lockInfo, e = s.lock.Stat()
	if e != nil || !safeSuiteFailureFile(s.lockInfo) || s.lockInfo.Size() != 0 {
		return nil, errSuiteFailureStore
	}
	if unix.Flock(int(s.lock.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil {
		return nil, errSuiteFailureBusy
	}
	if s.unchanged() != nil {
		return nil, errSuiteFailureStore
	}
	return s, nil
}
