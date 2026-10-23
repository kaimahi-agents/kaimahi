//go:build !windows

package fileaccess

import (
	"context"
	"errors"

	"golang.org/x/sys/unix"
)

// TryLock takes a nonblocking process-held exclusive lock on file. The lock is
// released when the private file handle closes or the process exits.
func TryLock(ctx context.Context, file *PrivateFile) error {
	if err := checkContext(ctx); err != nil {
		return wrapAccess("lock private file", err)
	}
	raw := rawObject(file)
	if raw == nil {
		return wrapAccess("lock private file", unsafeCause("private file handle is absent"))
	}
	conn, err := raw.SyscallConn()
	if err != nil {
		return wrapAccess("lock private file", err)
	}
	var lockErr error
	if err := conn.Control(func(fd uintptr) { lockErr = unix.Flock(int(fd), unix.LOCK_EX|unix.LOCK_NB) }); err != nil {
		return wrapAccess("lock private file", err)
	}
	if errors.Is(lockErr, unix.EWOULDBLOCK) || errors.Is(lockErr, unix.EAGAIN) {
		lockErr = unsafeCause("private file is already locked")
	}
	return wrapAccess("lock private file", lockErr)
}
