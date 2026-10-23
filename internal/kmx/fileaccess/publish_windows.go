//go:build windows

package fileaccess

import (
	"context"
	"errors"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

const fileLinkInformationLegacy = 11

type fileLinkInformation struct {
	ReplaceIfExists bool
	RootDirectory   windows.Handle
	FileNameLength  uint32
	FileName        [windows.MAX_PATH]uint16
}

type fileDispositionInformationEx struct {
	Flags uint32
}

var setPrivateLinkInformation = func(handle windows.Handle, status *windows.IO_STATUS_BLOCK, info *byte, size uint32) error {
	return windows.NtSetInformationFile(handle, status, info, size, fileLinkInformationLegacy)
}

func checkPrivateComponent(name string) error { return checkWindowsComponent(name) }

func openExistingPrivateFileAt(ctx context.Context, parent *Directory, name string) (*PrivateFile, error) {
	return openExistingPrivateWindowsFileAt(ctx, parent, name, privateFileAccess)
}

func openExistingPrivateWindowsFileAt(ctx context.Context, parent *Directory, name string, access uint32) (*PrivateFile, error) {
	if err := CheckSafeDirectory(ctx, parent, true); err != nil {
		return nil, err
	}
	if err := checkPrivateComponent(name); err != nil {
		return nil, err
	}
	var handle windows.Handle
	parentFile := rawObject(parent)
	err := withHandle(parentFile, func(parentHandle windows.Handle) error {
		if err := qualifyHandle(ctx, parentHandle, true); err != nil {
			return err
		}
		opened, openErr := ntOpen(parentHandle, name, access, windows.FILE_NON_DIRECTORY_FILE)
		if openErr != nil {
			return openErr
		}
		if err := qualifyHandle(ctx, opened, false); err != nil {
			closeErr := windows.CloseHandle(opened)
			return creationFailure(err, closeErr)
		}
		handle = opened
		return nil
	})
	if err != nil {
		return nil, err
	}
	file := &PrivateFile{file: os.NewFile(uintptr(handle), childWindowsName(parentFile, name))}
	if err := CheckPrivate(ctx, file, false); err != nil {
		closeErr := file.Close()
		return nil, creationFailure(err, closeErr)
	}
	identity, err := PhysicalIdentity(ctx, file)
	if err != nil {
		closeErr := file.Close()
		return nil, creationFailure(err, closeErr)
	}
	file.identity = identity
	return file, nil
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
	got, err := PhysicalIdentity(ctx, opened)
	if err != nil {
		closeErr := opened.Close()
		return false, creationFailure(err, closeErr)
	}
	if got != identity {
		closeErr := opened.Close()
		return false, creationFailure(unsafeCause("private stage identity changed"), closeErr)
	}
	encoded, err := windows.UTF16FromString(name)
	if err != nil || len(encoded) > windows.MAX_PATH {
		closeErr := opened.Close()
		return false, creationFailure(unsafeCause("publication name is too long"), closeErr)
	}
	info := fileLinkInformation{RootDirectory: windows.InvalidHandle}
	copy(info.FileName[:], encoded)
	info.FileNameLength = uint32((len(encoded) - 1) * 2)
	parentFile := rawObject(parent)
	linkErr := withHandle(parentFile, func(parentHandle windows.Handle) error {
		info.RootDirectory = parentHandle
		return withHandle(rawObject(opened), func(stageHandle windows.Handle) error {
			var status windows.IO_STATUS_BLOCK
			err := setPrivateLinkInformation(stageHandle, &status, (*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
			if err != nil {
				err = normalizeNTStatus(err)
				if errors.Is(err, os.ErrExist) {
					return unsafeCause("publication destination already exists")
				}
			}
			return err
		})
	})
	closeErr := opened.Close()
	if linkErr == nil {
		if closeErr != nil {
			return true, unreadableCause("publication link handle close is uncertain")
		}
		return true, nil
	}
	visible, observedErr := observedPrivateEntry(context.WithoutCancel(ctx), parent, name, identity, linkErr)
	if closeErr != nil {
		return visible, unreadableCause("publication link handle close is uncertain")
	}
	return visible, observedErr
}

func removePrivateEntry(ctx context.Context, parent *Directory, name, identity string) error {
	opened, err := openExistingPrivateWindowsFileAt(ctx, parent, name, privateFileCreate)
	if err != nil {
		return err
	}
	got, err := PhysicalIdentity(ctx, opened)
	if err != nil {
		closeErr := opened.Close()
		return creationFailure(err, closeErr)
	}
	if got != identity {
		closeErr := opened.Close()
		return creationFailure(unsafeCause("private stage identity changed"), closeErr)
	}
	removeErr := withHandle(rawObject(opened), func(handle windows.Handle) error {
		info := fileDispositionInformationEx{Flags: windows.FILE_DISPOSITION_DELETE |
			windows.FILE_DISPOSITION_POSIX_SEMANTICS |
			windows.FILE_DISPOSITION_FORCE_IMAGE_SECTION_CHECK}
		var status windows.IO_STATUS_BLOCK
		if err := windows.NtSetInformationFile(handle, &status, (*byte)(unsafe.Pointer(&info)),
			uint32(unsafe.Sizeof(info)), windows.FileDispositionInformationEx); err != nil {
			return unreadableError(normalizeNTStatus(err))
		}
		return nil
	})
	closeErr := opened.Close()
	if closeErr != nil {
		return unreadableCause("private stage close is uncertain")
	}
	return removeErr
}
