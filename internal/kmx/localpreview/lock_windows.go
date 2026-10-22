//go:build windows

package localpreview

import (
	"os"

	"golang.org/x/sys/windows"
)

// tryLock takes an exclusive, nonblocking lock on the first byte of f. It
// reports false when another handle, in this or another process, holds it.
func tryLock(f *os.File) bool {
	var overlapped windows.Overlapped
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped) == nil
}

// unlock releases the lock explicitly: Windows releases a closed handle's
// locks only after an unspecified delay.
func unlock(f *os.File) {
	var overlapped windows.Overlapped
	_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &overlapped)
}

// removeHeldLock releases and closes f, then removes the lock file at path.
// Windows cannot delete a file this handle still has open, because Go opens
// files without FILE_SHARE_DELETE. For the same reason the removal is expected
// to fail while a run that locked the file afterwards keeps it open; this has
// not been verified on native Windows.
func removeHeldLock(f *os.File, path string) error {
	unlock(f)
	lockReleased(path)
	_ = f.Close()
	return os.Remove(path)
}
