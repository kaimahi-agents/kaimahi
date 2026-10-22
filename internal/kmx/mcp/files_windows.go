package mcp

import (
	"errors"
	"golang.org/x/sys/windows"
	"os"
	"unsafe"
)

func openTarget(root *os.Root, name string) (*os.File, error) { return root.Open(name) }
func singleLink(f *os.File) bool {
	var info windows.ByHandleFileInformation
	return windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info) == nil && info.NumberOfLinks == 1
}
func lockFailure(err error) error {
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return refusal("busy")
	}
	return refusal("lock_unavailable")
}

func lockTarget(f *os.File) (func(), error) {
	// All cooperating writers lock the same byte beyond the bounded source,
	// so Windows' mandatory range locks do not obstruct source readers.
	overlap := &windows.Overlapped{Offset: maxFileBytes + 1}
	h := windows.Handle(f.Fd())
	if err := windows.LockFileEx(h, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, overlap); err != nil {
		return nil, err
	}
	return func() { _ = windows.UnlockFileEx(h, 0, 1, 0, overlap) }, nil
}
func renameExclusive(root *os.Root, from, to string) error {
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	source, err := windows.NewNTUnicodeString(from)
	if err != nil {
		return err
	}
	attrs := windows.OBJECT_ATTRIBUTES{RootDirectory: windows.Handle(dir.Fd()), ObjectName: source, Attributes: windows.OBJ_CASE_INSENSITIVE}
	attrs.Length = uint32(unsafe.Sizeof(attrs))
	var h windows.Handle
	err = windows.NtCreateFile(&h, windows.DELETE|windows.SYNCHRONIZE, &attrs, &windows.IO_STATUS_BLOCK{}, nil, 0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_OPEN,
		windows.FILE_OPEN_REPARSE_POINT|windows.FILE_NON_DIRECTORY_FILE|windows.FILE_SYNCHRONOUS_IO_NONALERT, 0, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	// FILE_RENAME_INFORMATION: ReplaceIfExists remains zero. Both paths are
	// single names resolved against the already-confined parent handle.
	info := struct {
		Replace uint32
		Root    windows.Handle
		Length  uint32
		Name    [256]uint16
	}{Root: windows.Handle(dir.Fd())}
	name, err := windows.UTF16FromString(to)
	if err != nil {
		return err
	}
	if len(name) > len(info.Name) {
		return windows.ERROR_FILENAME_EXCED_RANGE
	}
	copy(info.Name[:], name)
	info.Length = uint32((len(name) - 1) * 2)
	err = windows.NtSetInformationFile(h, &windows.IO_STATUS_BLOCK{}, (*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), windows.FileRenameInformation)
	if errors.Is(err, windows.STATUS_OBJECT_NAME_COLLISION) {
		return os.ErrExist
	}
	return err
}
