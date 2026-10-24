//go:build !windows

package fileaccess

import (
	"context"
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func checkPrivateComponent(name string) error {
	if !singleUnixComponent(name) {
		return unsafeCause("entry name is not one component")
	}
	return nil
}

func openExistingPrivateFileAt(ctx context.Context, parent *Directory, name string) (*PrivateFile, error) {
	if err := CheckSafeDirectory(ctx, parent, true); err != nil {
		return nil, err
	}
	if err := checkPrivateComponent(name); err != nil {
		return nil, err
	}
	file, err := openAtMode(ctx, rawObject(parent), name, privateFileFlags, 0)
	if err != nil {
		return nil, err
	}
	private := &PrivateFile{file: file}
	if err := CheckPrivate(ctx, private, false); err != nil {
		closeErr := file.Close()
		return nil, creationFailure(err, closeErr)
	}
	identity, err := PhysicalIdentity(ctx, private)
	if err != nil {
		closeErr := file.Close()
		return nil, creationFailure(err, closeErr)
	}
	private.identity = identity
	return private, nil
}

func linkPrivateEntry(ctx context.Context, parent *Directory, stage, name, identity string) (bool, error) {
	if err := checkPrivateComponent(stage); err != nil {
		return false, err
	}
	if err := checkPrivateComponent(name); err != nil {
		return false, err
	}
	opened, err := openExistingPrivateFileAt(ctx, parent, stage)
	if err != nil {
		return false, err
	}
	defer opened.Close()
	got, err := PhysicalIdentity(ctx, opened)
	if err != nil {
		return false, err
	}
	if got != identity {
		return false, unsafeCause("private stage identity changed")
	}
	raw := rawObject(parent)
	if raw == nil {
		return false, unsafeCause("parent handle is absent")
	}
	conn, err := raw.SyscallConn()
	if err != nil {
		return false, err
	}
	var linkErr error
	if err := conn.Control(func(fd uintptr) {
		linkErr = unix.Linkat(int(fd), stage, int(fd), name, 0)
	}); err != nil {
		return false, err
	}
	if errors.Is(linkErr, os.ErrExist) {
		return observedPrivateEntry(ctx, parent, name, identity, unsafeCause("publication destination already exists"))
	}
	if linkErr != nil {
		return observedPrivateEntry(context.WithoutCancel(ctx), parent, name, identity, linkErr)
	}
	return true, nil
}

func removePrivateEntry(ctx context.Context, parent *Directory, name, identity string) error {
	opened, err := openExistingPrivateFileAt(ctx, parent, name)
	if err != nil {
		return err
	}
	got, identityErr := PhysicalIdentity(ctx, opened)
	closeErr := opened.Close()
	if identityErr != nil || closeErr != nil {
		return errors.Join(identityErr, closeErr)
	}
	if got != identity {
		return unsafeCause("private stage identity changed")
	}
	raw := rawObject(parent)
	if raw == nil {
		return unsafeCause("parent handle is absent")
	}
	conn, err := raw.SyscallConn()
	if err != nil {
		return err
	}
	var removeErr error
	if err := conn.Control(func(fd uintptr) { removeErr = unix.Unlinkat(int(fd), name, 0) }); err != nil {
		return err
	}
	return removeErr
}
