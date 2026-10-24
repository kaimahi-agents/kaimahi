//go:build windows

package localpreview

import (
	"os"

	"golang.org/x/sys/windows"
)

func tryLock(f *os.File) bool {
	var overlapped windows.Overlapped
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped) == nil
}

func unlock(f *os.File) {
	var overlapped windows.Overlapped
	_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &overlapped)
}
