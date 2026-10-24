//go:build windows

package fileaccess

import (
	"context"
	"unsafe"

	"golang.org/x/sys/windows"
)

var flushDirectoryHandle = windows.FlushFileBuffers

// SyncDirectory flushes the anchored private directory after entry changes.
// Windows requires a separately opened write-capable handle, which is opened
// relative to and identity-checked against the retained directory.
func SyncDirectory(ctx context.Context, directory *Directory) error {
	if err := checkContext(ctx); err != nil {
		return wrapAccess("sync private directory", err)
	}
	if err := CheckPrivate(ctx, directory, true); err != nil {
		return wrapAccess("sync private directory", err)
	}
	err := withHandle(rawObject(directory), func(source windows.Handle) error {
		reopened, err := reopenDirectoryForSync(source)
		if err != nil {
			return err
		}
		operationErr := checkSyncDirectory(ctx, source, reopened)
		if operationErr == nil {
			operationErr = checkContext(ctx)
		}
		if operationErr == nil {
			if err := flushDirectoryHandle(reopened); err != nil {
				operationErr = unreadableError(err)
			}
		}
		if operationErr == nil {
			operationErr = checkContext(ctx)
		}
		if closeErr := windows.CloseHandle(reopened); closeErr != nil {
			return unreadableCause("sync directory handle close is uncertain")
		}
		return operationErr
	})
	return wrapAccess("sync private directory", err)
}

func reopenDirectoryForSync(source windows.Handle) (windows.Handle, error) {
	name, err := windows.NewNTUnicodeString("")
	if err != nil {
		return windows.InvalidHandle, unreadableError(err)
	}
	attributes := windows.OBJECT_ATTRIBUTES{
		RootDirectory: source,
		ObjectName:    name,
		Attributes:    windows.OBJ_DONT_REPARSE,
	}
	attributes.Length = uint32(unsafe.Sizeof(attributes))
	var handle windows.Handle
	err = windows.NtCreateFile(&handle, windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE|windows.SYNCHRONIZE,
		&attributes, &windows.IO_STATUS_BLOCK{}, nil, windows.FILE_ATTRIBUTE_NORMAL, shareAll,
		windows.FILE_OPEN, windows.FILE_DIRECTORY_FILE|syncOptions, 0, 0)
	if err != nil {
		return windows.InvalidHandle, unreadableError(normalizeNTStatus(err))
	}
	return handle, nil
}

func checkSyncDirectory(ctx context.Context, source, reopened windows.Handle) error {
	if err := qualifyHandle(ctx, source, true); err != nil {
		return err
	}
	if err := qualifyHandle(ctx, reopened, true); err != nil {
		return err
	}
	var sourceID, reopenedID fileIDInfo
	for _, query := range []struct {
		handle windows.Handle
		id     *fileIDInfo
	}{{source, &sourceID}, {reopened, &reopenedID}} {
		if err := fileInformationByHandleEx(query.handle, windows.FileIdInfo,
			(*byte)(unsafe.Pointer(query.id)), uint32(unsafe.Sizeof(*query.id))); err != nil {
			return unreadableError(err)
		}
	}
	if sourceID != reopenedID {
		return unsafeCause("sync handle identifies a different directory")
	}
	return nil
}
