//go:build !windows

package fileaccess

import (
	"context"
	"errors"
	"golang.org/x/sys/unix"
)

// RemovePrivateDirectoryAt removes only an empty private child still identifying
// expected, then flushes its anchored parent. The caller retains expected until
// return. An error may follow observed deletion; never retry an ambiguous effect
// or treat a missing entry as success. This is not a recursive deletion API.
func RemovePrivateDirectoryAt(ctx context.Context, parent *Directory, name string, expected *Directory) error {
	return wrapAccess("remove private directory", removePrivateDirectory(ctx, parent, name, expected))
}

func removePrivateDirectory(ctx context.Context, parent *Directory, name string, expected *Directory) error {
	if err := CheckPrivate(ctx, parent, true); err != nil {
		return err
	}
	if err := checkPrivateComponent(name); err != nil {
		return err
	}
	if err := CheckPrivate(ctx, expected, true); err != nil {
		return err
	}
	identity, err := PhysicalIdentity(ctx, expected)
	if err != nil {
		return err
	}
	if err := checkEmptyPrivateDirectory(ctx, parent, name, identity); err != nil {
		return err
	}
	conn, err := rawObject(parent).SyscallConn()
	if err != nil {
		return err
	}
	if err := checkContext(ctx); err != nil {
		return err
	}
	var removeErr error
	if err := conn.Control(func(fd uintptr) { removeErr = unix.Unlinkat(int(fd), name, unix.AT_REMOVEDIR) }); err != nil {
		return err
	}
	if removeErr != nil {
		return removeErr
	}
	// Attempt required local durability even if cancellation followed the effect.
	return errors.Join(SyncDirectory(context.WithoutCancel(ctx), parent), checkContext(ctx))
}

func checkEmptyPrivateDirectory(ctx context.Context, parent *Directory, name, identity string) (err error) {
	opened, err := OpenDirectoryAt(ctx, parent, name)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, opened.Close()) }()
	if err := CheckPrivate(ctx, opened, true); err != nil {
		return err
	}
	got, err := PhysicalIdentity(ctx, opened)
	if err != nil {
		return err
	}
	if got != identity {
		return unsafeCause("private directory identity changed")
	}
	entries, err := opened.ReadDir(1)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return unsafeCause("private directory is not empty")
	}
	return nil
}
