//go:build windows

package fileaccess

import (
	"context"
	"errors"

	"golang.org/x/sys/windows"
)

func tryLock(ctx context.Context, file *PrivateFile) (bool, error) {
	if err := checkContext(ctx); err != nil {
		return false, err
	}
	err := withHandle(rawObject(file), func(handle windows.Handle) error {
		return windows.LockFileEx(handle, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, ^uint32(0), ^uint32(0), new(windows.Overlapped))
	})
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return false, nil
	}
	return err == nil, err
}
