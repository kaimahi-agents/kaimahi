//go:build windows

package fileaccess

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows paths are parsed before Win32 normalization, then opened one
// component at a time relative to retained parent handles. Every opened object
// is checked on that handle, so a reparse point, device, stream, or path alias
// cannot redirect the walk.

const (
	directoryAccess = windows.FILE_GENERIC_READ | windows.FILE_TRAVERSE
	regularAccess   = windows.FILE_GENERIC_READ | windows.FILE_READ_ATTRIBUTES | windows.READ_CONTROL
	shareAll        = windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE
	syncOptions     = windows.FILE_OPEN_REPARSE_POINT | windows.FILE_SYNCHRONOUS_IO_NONALERT

	fileDeviceDisk   = 0x7
	fileRemoteDevice = 0x10
	fsDeviceInfo     = 4
	volumeNameNone   = 0x4
)

var procNtQueryVolumeInformationFile = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtQueryVolumeInformationFile")

type localPath struct {
	drive byte
	parts []string
}

func (p localPath) String() string {
	return string(p.drive) + `:\` + strings.Join(p.parts, `\`)
}

func parseLocalPath(path string) (localPath, error) {
	if path == "" || strings.IndexByte(path, 0) >= 0 {
		return localPath{}, unsafeCause("path is empty or contains NUL")
	}
	if rest, ok := strings.CutPrefix(path, `\\?\`); ok {
		if len(rest) < 3 || !driveLetter(rest[0]) || rest[1] != ':' || rest[2] != '\\' || strings.IndexByte(rest, '/') >= 0 {
			return localPath{}, unsupportedCause("extended path is not a local drive path")
		}
		parsed := localPath{drive: upper(rest[0])}
		parts, err := windowsPathComponents(rest[3:])
		if err != nil {
			return localPath{}, err
		}
		parsed.parts = append(parsed.parts, parts...)
		return parsed, nil
	}
	if isSeparator(path[0]) {
		return localPath{}, unsupportedCause("UNC, device, and root-relative paths are unsupported")
	}
	if len(path) >= 2 && path[1] == ':' {
		if !driveLetter(path[0]) {
			return localPath{}, unsafeCause("invalid drive letter")
		}
		if len(path) == 2 || !isSeparator(path[2]) {
			return localPath{}, unsupportedCause("drive-relative path is unsupported")
		}
		return appendWindowsComponents(localPath{drive: upper(path[0])}, path[3:])
	}
	if err := validateWindowsComponents(path); err != nil {
		return localPath{}, err
	}
	wd, err := os.Getwd()
	if err != nil {
		return localPath{}, unreadableError(err)
	}
	if !filepath.IsAbs(wd) {
		return localPath{}, unsupportedCause("working directory is not a local absolute path")
	}
	base, err := parseLocalPath(wd)
	if err != nil {
		return localPath{}, err
	}
	return appendWindowsComponents(base, path)
}

func appendWindowsComponents(base localPath, path string) (localPath, error) {
	parts, err := windowsPathComponents(path)
	if err != nil {
		return localPath{}, err
	}
	base.parts = append(base.parts, parts...)
	return base, nil
}

func validateWindowsComponents(path string) error {
	_, err := windowsPathComponents(path)
	return err
}

func windowsPathComponents(path string) ([]string, error) {
	normalized := strings.ReplaceAll(path, "/", `\`)
	if normalized == "" {
		return nil, nil
	}
	if normalized == `\` || strings.Contains(normalized, `\\`) {
		return nil, unsafeCause("path contains an empty component")
	}
	normalized = strings.TrimSuffix(normalized, `\`)
	if normalized == "" {
		return nil, nil
	}
	parts := strings.Split(normalized, `\`)
	for _, part := range parts {
		if err := checkWindowsComponent(part); err != nil {
			return nil, err
		}
	}
	return parts, nil
}

func checkWindowsComponent(name string) error {
	if name == "" || name == "." || name == ".." {
		return unsafeCause("path contains an invalid or traversal component")
	}
	for _, r := range name {
		if r < 0x20 || strings.ContainsRune(`<>:"/\|?*`, r) {
			return unsafeCause("path component has a reserved character or stream separator")
		}
	}
	if last := name[len(name)-1]; last == '.' || last == ' ' {
		return unsafeCause("path component has an ambiguous suffix")
	}
	if reservedWindowsName(name) {
		return unsafeCause("path component is a reserved device name")
	}
	return nil
}

func reservedWindowsName(name string) bool {
	base, _, _ := strings.Cut(name, ".")
	base = strings.ToUpper(strings.TrimRight(base, " "))
	switch base {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
		return true
	}
	if len(base) >= 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) {
		switch base[3:] {
		case "0", "1", "2", "3", "4", "5", "6", "7", "8", "9", "¹", "²", "³":
			return true
		}
	}
	return false
}

func driveLetter(c byte) bool { return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' }
func upper(c byte) byte {
	if 'a' <= c && c <= 'z' {
		return c - 'a' + 'A'
	}
	return c
}
func isSeparator(c byte) bool { return c == '\\' || c == '/' }

func ntOpen(parent windows.Handle, name string, access, options uint32) (windows.Handle, error) {
	handle, _, err := ntCreate(parent, name, access, options, windows.FILE_OPEN, nil)
	return handle, err
}

func ntCreate(parent windows.Handle, name string, access, options, disposition uint32, descriptor *windows.SECURITY_DESCRIPTOR) (windows.Handle, uintptr, error) {
	ntName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return windows.InvalidHandle, 0, &classifiedCause{class: ClassUnsafe, cause: err}
	}
	attributes := windows.OBJECT_ATTRIBUTES{
		RootDirectory:      parent,
		ObjectName:         ntName,
		Attributes:         windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
		SecurityDescriptor: descriptor,
	}
	attributes.Length = uint32(unsafe.Sizeof(attributes))
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&handle, access|windows.SYNCHRONIZE, &attributes, &status, nil,
		windows.FILE_ATTRIBUTE_NORMAL, shareAll, disposition, options|syncOptions, 0, 0)
	if err != nil {
		return windows.InvalidHandle, 0, normalizeNTStatus(err)
	}
	return handle, status.Information, nil
}

func normalizeNTStatus(err error) error {
	var status windows.NTStatus
	if errors.As(err, &status) {
		err = status.Errno()
		switch status {
		case windows.STATUS_OBJECT_NAME_INVALID,
			windows.STATUS_OBJECT_PATH_SYNTAX_BAD,
			windows.STATUS_OBJECT_TYPE_MISMATCH,
			windows.STATUS_FILE_IS_A_DIRECTORY,
			windows.STATUS_NOT_A_DIRECTORY,
			windows.STATUS_REPARSE_POINT_NOT_RESOLVED,
			windows.STATUS_REPARSE_ATTRIBUTE_CONFLICT,
			windows.STATUS_REPARSE_POINT_ENCOUNTERED:
			return &classifiedCause{class: ClassUnsafe, cause: err}
		}
	}
	switch err {
	case windows.ERROR_REPARSE,
		windows.ERROR_REPARSE_OBJECT,
		windows.ERROR_CANT_ACCESS_FILE,
		windows.ERROR_REPARSE_POINT_ENCOUNTERED,
		windows.ERROR_DIRECTORY,
		windows.ERROR_INVALID_NAME,
		windows.ERROR_BAD_PATHNAME:
		return &classifiedCause{class: ClassUnsafe, cause: err}
	}
	return err
}

func openVolumeRoot(ctx context.Context, drive byte) (windows.Handle, error) {
	if err := checkContext(ctx); err != nil {
		return windows.InvalidHandle, err
	}
	name, err := windows.NewNTUnicodeString(`\??\` + string(drive) + `:\`)
	if err != nil {
		return windows.InvalidHandle, unreadableError(err)
	}
	attributes := windows.OBJECT_ATTRIBUTES{ObjectName: name, Attributes: windows.OBJ_CASE_INSENSITIVE}
	attributes.Length = uint32(unsafe.Sizeof(attributes))
	var handle windows.Handle
	err = windows.NtCreateFile(&handle, directoryAccess|windows.SYNCHRONIZE, &attributes, &windows.IO_STATUS_BLOCK{}, nil,
		windows.FILE_ATTRIBUTE_NORMAL, shareAll, windows.FILE_OPEN, windows.FILE_DIRECTORY_FILE|syncOptions, 0, 0)
	if err != nil {
		return windows.InvalidHandle, normalizeNTStatus(err)
	}
	if err := checkVolumeRoot(ctx, handle); err != nil {
		windows.CloseHandle(handle)
		return windows.InvalidHandle, err
	}
	if err := checkContext(ctx); err != nil {
		windows.CloseHandle(handle)
		return windows.InvalidHandle, err
	}
	if err := requireQualifiedFilesystem(handle); err != nil {
		windows.CloseHandle(handle)
		return windows.InvalidHandle, err
	}
	return handle, nil
}

func checkVolumeRoot(ctx context.Context, handle windows.Handle) error {
	if err := checkObject(ctx, handle, true); err != nil {
		return err
	}
	if err := checkContext(ctx); err != nil {
		return err
	}
	var device struct{ DeviceType, Characteristics uint32 }
	var status windows.IO_STATUS_BLOCK
	r, _, _ := procNtQueryVolumeInformationFile.Call(uintptr(handle), uintptr(unsafe.Pointer(&status)),
		uintptr(unsafe.Pointer(&device)), unsafe.Sizeof(device), fsDeviceInfo)
	if r != 0 {
		return unreadableError(windows.NTStatus(r).Errno())
	}
	if err := checkDeviceInformation(device.DeviceType, device.Characteristics); err != nil {
		return err
	}
	if err := checkContext(ctx); err != nil {
		return err
	}
	buf := make([]uint16, windows.MAX_PATH)
	n, err := windows.GetFinalPathNameByHandle(handle, &buf[0], uint32(len(buf)), volumeNameNone)
	if err != nil {
		return unreadableError(err)
	}
	if n >= uint32(len(buf)) || string(utf16.Decode(buf[:n])) != `\` {
		return unsupportedCause("drive letter does not identify a volume root")
	}
	return nil
}

func checkDeviceInformation(deviceType, characteristics uint32) error {
	if deviceType != fileDeviceDisk || characteristics&fileRemoteDevice != 0 {
		return unsupportedCause("object is not on a local disk")
	}
	return nil
}

func checkLocalDisk(handle windows.Handle) error {
	var device struct{ DeviceType, Characteristics uint32 }
	var status windows.IO_STATUS_BLOCK
	r, _, _ := procNtQueryVolumeInformationFile.Call(uintptr(handle), uintptr(unsafe.Pointer(&status)),
		uintptr(unsafe.Pointer(&device)), unsafe.Sizeof(device), fsDeviceInfo)
	if r != 0 {
		return unreadableError(windows.NTStatus(r).Errno())
	}
	return checkDeviceInformation(device.DeviceType, device.Characteristics)
}

func qualifyHandle(ctx context.Context, handle windows.Handle, directory bool) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if err := checkObject(ctx, handle, directory); err != nil {
		return err
	}
	if err := checkContext(ctx); err != nil {
		return err
	}
	if err := checkLocalDisk(handle); err != nil {
		return err
	}
	if err := checkContext(ctx); err != nil {
		return err
	}
	return requireQualifiedFilesystem(handle)
}

func qualifyHandleAny(ctx context.Context, handle windows.Handle) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if err := checkObjectAny(ctx, handle); err != nil {
		return err
	}
	if err := checkContext(ctx); err != nil {
		return err
	}
	if err := checkLocalDisk(handle); err != nil {
		return err
	}
	if err := checkContext(ctx); err != nil {
		return err
	}
	return requireQualifiedFilesystem(handle)
}

func checkObject(ctx context.Context, handle windows.Handle, wantDir bool) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	kind, err := windows.GetFileType(handle)
	if err != nil {
		return unreadableError(err)
	}
	if kind != windows.FILE_TYPE_DISK {
		return unsafeCause("object is not a disk file or directory")
	}
	if err := checkContext(ctx); err != nil {
		return err
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return unreadableError(err)
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DEVICE) != 0 {
		return unsafeCause("object is a reparse point or device")
	}
	if isDir := info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0; isDir != wantDir {
		return unsafeCause("object has the wrong type")
	}
	return nil
}

func checkObjectAny(ctx context.Context, handle windows.Handle) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	kind, err := windows.GetFileType(handle)
	if err != nil {
		return unreadableError(err)
	}
	if kind != windows.FILE_TYPE_DISK {
		return unsafeCause("object is not a disk file or directory")
	}
	if err := checkContext(ctx); err != nil {
		return err
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return unreadableError(err)
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DEVICE) != 0 {
		return unsafeCause("object is a reparse point or device")
	}
	return nil
}

func openChecked(ctx context.Context, parent windows.Handle, name string, dir bool) (windows.Handle, error) {
	if err := checkContext(ctx); err != nil {
		return windows.InvalidHandle, err
	}
	if err := checkWindowsComponent(name); err != nil {
		return windows.InvalidHandle, err
	}
	options, access := uint32(windows.FILE_NON_DIRECTORY_FILE), uint32(regularAccess)
	if dir {
		options, access = windows.FILE_DIRECTORY_FILE, directoryAccess
	}
	handle, err := ntOpen(parent, name, access, options)
	if err != nil {
		return windows.InvalidHandle, err
	}
	if err := qualifyHandle(ctx, handle, dir); err != nil {
		windows.CloseHandle(handle)
		return windows.InvalidHandle, err
	}
	return handle, nil
}

func walkDirectory(ctx context.Context, path localPath) (windows.Handle, error) {
	if err := checkContext(ctx); err != nil {
		return windows.InvalidHandle, err
	}
	dir, err := openVolumeRoot(ctx, path.drive)
	if err != nil {
		return windows.InvalidHandle, err
	}
	for _, part := range path.parts {
		if err := checkContext(ctx); err != nil {
			windows.CloseHandle(dir)
			return windows.InvalidHandle, err
		}
		next, err := openChecked(ctx, dir, part, true)
		windows.CloseHandle(dir)
		if err != nil {
			return windows.InvalidHandle, err
		}
		dir = next
	}
	if err := checkContext(ctx); err != nil {
		windows.CloseHandle(dir)
		return windows.InvalidHandle, err
	}
	return dir, nil
}

func withHandle(file *os.File, fn func(windows.Handle) error) error {
	if file == nil {
		return unsafeCause("file handle is absent")
	}
	conn, err := file.SyscallConn()
	if err != nil {
		return unreadableError(err)
	}
	var fnErr error
	if err := conn.Control(func(fd uintptr) { fnErr = fn(windows.Handle(fd)) }); err != nil {
		return unreadableError(err)
	}
	return fnErr
}

// OpenDirectory opens an existing directory through a checked local NTFS walk.
func OpenDirectory(ctx context.Context, path string) (*Directory, error) {
	if err := checkContext(ctx); err != nil {
		return nil, wrapAccess("open directory", err)
	}
	parsed, err := parseLocalPath(path)
	if err != nil {
		return nil, wrapAccess("open directory", err)
	}
	handle, err := walkDirectory(ctx, parsed)
	if err != nil {
		return nil, wrapAccess("open directory", err)
	}
	return &Directory{file: os.NewFile(uintptr(handle), parsed.String())}, nil
}

// OpenDirectoryAt opens one existing directory immediately below parent.
func OpenDirectoryAt(ctx context.Context, parent *Directory, name string) (*Directory, error) {
	if err := checkContext(ctx); err != nil {
		return nil, wrapAccess("open directory entry", err)
	}
	var handle windows.Handle
	parentFile := rawObject(parent)
	err := withHandle(parentFile, func(parentHandle windows.Handle) error {
		if err := qualifyHandle(ctx, parentHandle, true); err != nil {
			return err
		}
		var openErr error
		handle, openErr = openChecked(ctx, parentHandle, name, true)
		return openErr
	})
	if err != nil {
		return nil, wrapAccess("open directory entry", err)
	}
	if err := checkContext(ctx); err != nil {
		windows.CloseHandle(handle)
		return nil, wrapAccess("open directory entry", err)
	}
	return &Directory{file: os.NewFile(uintptr(handle), childWindowsName(parentFile, name))}, nil
}

// OpenRegularAt opens one existing regular file immediately below parent for
// reading only.
func OpenRegularAt(ctx context.Context, parent *Directory, name string) (*File, error) {
	if err := checkContext(ctx); err != nil {
		return nil, wrapAccess("open regular file", err)
	}
	var handle windows.Handle
	parentFile := rawObject(parent)
	err := withHandle(parentFile, func(parentHandle windows.Handle) error {
		if err := qualifyHandle(ctx, parentHandle, true); err != nil {
			return err
		}
		var openErr error
		handle, openErr = openChecked(ctx, parentHandle, name, false)
		return openErr
	})
	if err != nil {
		return nil, wrapAccess("open regular file", err)
	}
	if err := checkContext(ctx); err != nil {
		windows.CloseHandle(handle)
		return nil, wrapAccess("open regular file", err)
	}
	return &File{file: os.NewFile(uintptr(handle), childWindowsName(parentFile, name))}, nil
}

func childWindowsName(parent *os.File, name string) string {
	if parent == nil {
		return name
	}
	return parent.Name() + `\` + name
}
