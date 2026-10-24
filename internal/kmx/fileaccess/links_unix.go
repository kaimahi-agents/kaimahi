//go:build !windows

package fileaccess

import (
	"context"

	"golang.org/x/sys/unix"
)

// LinkCount reports how many directory entries identify object.
func LinkCount(ctx context.Context, object Object) (uint64, error) {
	if err := checkContext(ctx); err != nil {
		return 0, wrapAccess("count object links", err)
	}
	file := rawObject(object)
	if file == nil {
		return 0, wrapAccess("count object links", unsafeCause("file handle is absent"))
	}
	conn, err := file.SyscallConn()
	if err != nil {
		return 0, wrapAccess("count object links", unreadableCause("file handle is unavailable"))
	}
	var stat unix.Stat_t
	var statErr error
	if err := conn.Control(func(fd uintptr) { statErr = unix.Fstat(int(fd), &stat) }); err != nil {
		return 0, wrapAccess("count object links", err)
	}
	if statErr != nil {
		return 0, wrapAccess("count object links", statErr)
	}
	if err := checkContext(ctx); err != nil {
		return 0, wrapAccess("count object links", err)
	}
	return uint64(stat.Nlink), nil
}
