//go:build windows

package fileaccess

import (
	"context"
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var fileInformationByHandleEx = windows.GetFileInformationByHandleEx

type fileIDInfo struct {
	VolumeSerialNumber uint64
	FileID             [16]byte
}

// PhysicalIdentity returns the volume serial and full 128-bit file identifier
// of the local NTFS object behind file.
func PhysicalIdentity(ctx context.Context, object Object) (string, error) {
	if err := checkContext(ctx); err != nil {
		return "", wrapAccess("identify object", err)
	}
	var id fileIDInfo
	err := withHandle(rawObject(object), func(handle windows.Handle) error {
		if err := qualifyHandleAny(ctx, handle); err != nil {
			return err
		}
		if err := checkContext(ctx); err != nil {
			return err
		}
		if err := fileInformationByHandleEx(handle, windows.FileIdInfo, (*byte)(unsafe.Pointer(&id)), uint32(unsafe.Sizeof(id))); err != nil {
			return unreadableError(err)
		}
		return checkContext(ctx)
	})
	if err != nil {
		return "", wrapAccess("identify object", err)
	}
	return fmt.Sprintf("%016x-%x", id.VolumeSerialNumber, id.FileID), nil
}

func canonicalObjectPath(ctx context.Context, object Object) (string, error) {
	if err := checkContext(ctx); err != nil {
		return "", err
	}
	var result string
	err := withHandle(rawObject(object), func(handle windows.Handle) error {
		if err := qualifyHandleAny(ctx, handle); err != nil {
			return err
		}
		var id fileIDInfo
		if err := fileInformationByHandleEx(handle, windows.FileIdInfo, (*byte)(unsafe.Pointer(&id)), uint32(unsafe.Sizeof(id))); err != nil {
			return unreadableError(err)
		}
		buffer := make([]uint16, 32768)
		n, err := windows.GetFinalPathNameByHandle(handle, &buffer[0], uint32(len(buffer)), volumeNameNone)
		if err != nil || n == 0 || int(n) >= len(buffer) {
			return unreadableError(err)
		}
		result = fmt.Sprintf("%016x:%s", id.VolumeSerialNumber, windows.UTF16ToString(buffer[:n]))
		return checkContext(ctx)
	})
	if err != nil || result == "" {
		return "", wrapAccess("identify object path", err)
	}
	result = strings.ReplaceAll(result, "/", `\`)
	for strings.Contains(result, `\\`) {
		result = strings.ReplaceAll(result, `\\`, `\`)
	}
	return strings.ToLower(strings.TrimSuffix(result, `\`)), nil
}
