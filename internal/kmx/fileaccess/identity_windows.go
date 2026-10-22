//go:build windows

package fileaccess

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

var fileInformationByHandleEx = windows.GetFileInformationByHandleEx

// fileIDInfo is FILE_ID_INFO: the 64-bit volume serial number and the
// 128-bit file identifier that ReFS and NTFS report for FileIdInfo.
type fileIDInfo struct {
	VolumeSerialNumber uint64
	FileID             [16]byte
}

// PhysicalIdentity returns a stable key for the object behind file: its
// volume serial number and 128-bit file identifier. Path spelling, case,
// extended-length and 8.3 aliases and hard links of one object share a key.
// Only NTFS is qualified, since other filesystems may not keep file
// identifiers stable.
func PhysicalIdentity(file *os.File) (string, error) {
	var id fileIDInfo
	err := withHandle(file, func(h windows.Handle) error {
		if err := checkObjectAny(h); err != nil {
			return err
		}
		if err := requireNTFS(h); err != nil {
			return err
		}
		return fileInformationByHandleEx(h, windows.FileIdInfo, (*byte)(unsafe.Pointer(&id)), uint32(unsafe.Sizeof(id)))
	})
	if err != nil {
		return "", fmt.Errorf("query physical identity: %w", err)
	}
	return fmt.Sprintf("%016x-%x", id.VolumeSerialNumber, id.FileID), nil
}

// checkObjectAny refuses anything but an on-disk, non-reparse, non-device
// file or directory.
func checkObjectAny(handle windows.Handle) error {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return err
	}
	return checkObject(handle, info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0)
}
