//go:build !windows

package fileaccess

import (
	"context"
	"errors"

	"golang.org/x/sys/unix"
)

func tryLock(ctx context.Context, file *PrivateFile) (bool, error) {
	if err := checkContext(ctx); err != nil {
		return false, err
	}
	raw := rawObject(file)
	if raw == nil {
		return false, unsafeCause("private file handle is absent")
	}
	conn, err := raw.SyscallConn()
	if err != nil {
		return false, err
	}
	var lockErr error
	if err := conn.Control(func(fd uintptr) { lockErr = unix.Flock(int(fd), unix.LOCK_EX|unix.LOCK_NB) }); err != nil {
		return false, err
	}
	if errors.Is(lockErr, unix.EWOULDBLOCK) || errors.Is(lockErr, unix.EAGAIN) {
		return false, nil
	}
	return lockErr == nil, lockErr
}
