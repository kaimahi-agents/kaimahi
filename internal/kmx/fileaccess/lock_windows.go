//go:build windows

package fileaccess

import (
	"context"
	"errors"

	"golang.org/x/sys/windows"
)

// TryLock takes a nonblocking process-held exclusive lock on file. The lock is
// released when the private file handle closes or the process exits.
func TryLock(ctx context.Context, file *PrivateFile) error {
	if err := checkContext(ctx); err != nil {
		return wrapAccess("lock private file", err)
	}
	err := withHandle(rawObject(file), func(handle windows.Handle) error {
		return windows.LockFileEx(handle, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
			0, ^uint32(0), ^uint32(0), new(windows.Overlapped))
	})
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		err = unsafeCause("private file is already locked")
	}
	return wrapAccess("lock private file", err)
}
