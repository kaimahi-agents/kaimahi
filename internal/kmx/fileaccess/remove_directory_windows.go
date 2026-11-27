//go:build windows

package fileaccess

import (
	"context"
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// RemovePrivateDirectoryAt removes only an empty private child still identifying
// expected, then flushes its anchored parent. An error may follow observed
// deletion; callers must never retry an ambiguous effect.
func RemovePrivateDirectoryAt(ctx context.Context, parent *Directory, name string, expected *Directory) error {
	return wrapAccess("remove private directory", removePrivateWindowsDirectory(ctx, parent, name, expected))
}

func removePrivateWindowsDirectory(ctx context.Context, parent *Directory, name string, expected *Directory) error {
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
	if err := checkEmptyPrivateWindowsDirectory(ctx, parent, name, identity); err != nil {
		return err
	}
	var handle windows.Handle
	parentFile := rawObject(parent)
	err = withHandle(parentFile, func(parentHandle windows.Handle) error {
		opened, openErr := ntOpen(parentHandle, name, privateDirectoryCreate, windows.FILE_DIRECTORY_FILE)
		if openErr != nil {
			return openErr
		}
		if err := qualifyHandle(ctx, opened, true); err != nil {
			closeErr := windows.CloseHandle(opened)
			return creationFailure(err, closeErr)
		}
		handle = opened
		return nil
	})
	if err != nil {
		return err
	}
	opened := &Directory{file: os.NewFile(uintptr(handle), childWindowsName(parentFile, name))}
	if err := CheckPrivate(ctx, opened, true); err != nil {
		return errors.Join(err, opened.Close())
	}
	got, identityErr := PhysicalIdentity(ctx, opened)
	entries, readErr := opened.ReadDir(1)
	if identityErr != nil || readErr != nil || got != identity || len(entries) != 0 {
		closeErr := opened.Close()
		if got != identity {
			identityErr = unsafeCause("private directory identity changed")
		}
		if len(entries) != 0 {
			readErr = unsafeCause("private directory is not empty")
		}
		return errors.Join(identityErr, readErr, closeErr)
	}
	removeErr := withHandle(rawObject(opened), discardWindowsHandle)
	closeErr := opened.Close()
	if removeErr != nil || closeErr != nil {
		return unreadableCause("private directory removal is uncertain")
	}
	return errors.Join(SyncDirectory(context.WithoutCancel(ctx), parent), checkContext(ctx))
}

func checkEmptyPrivateWindowsDirectory(ctx context.Context, parent *Directory, name, identity string) (err error) {
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
