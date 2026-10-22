//go:build windows

package fileaccess

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows paths are parsed strictly before Win32 normalization and then
// opened one component at a time with NtCreateFile, relative to the retained
// parent handle. Each step opens the named object itself (never a reparse
// target) and checks the opened handle, so a junction, symbolic link, mount
// point, device or alternate data stream cannot redirect a walk. No step
// re-resolves a pathname, and none uses backup intent.

const (
	// FILE_GENERIC_READ plus traverse, so a directory handle can anchor the
	// next relative open and expose its security descriptor.
	directoryAccess = windows.FILE_GENERIC_READ | windows.FILE_TRAVERSE
	shareAll        = windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE
	syncOptions     = windows.FILE_OPEN_REPARSE_POINT | windows.FILE_SYNCHRONOUS_IO_NONALERT

	fileDeviceDisk   = 0x7  // FILE_DEVICE_DISK
	fileRemoteDevice = 0x10 // FILE_REMOTE_DEVICE characteristic
	fsDeviceInfo     = 4    // FileFsDeviceInformation
	volumeNameNone   = 0x4  // VOLUME_NAME_NONE
)

var procNtQueryVolumeInformationFile = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtQueryVolumeInformationFile")

// localPath is a parsed absolute path on a local drive letter volume.
type localPath struct {
	drive byte
	parts []string
}

func (p localPath) String() string {
	return string(p.drive) + `:\` + strings.Join(p.parts, `\`)
}

// parseLocalPath accepts ordinary relative paths, drive-absolute paths and
// extended-length \\?\X:\ paths. It refuses UNC, device, NT-namespace,
// drive-relative and root-relative forms, and refuses any component that
// Win32 would silently rewrite or reinterpret (alternate data streams,
// trailing dots or spaces, reserved DOS device names).
func parseLocalPath(path string) (localPath, error) {
	if path == "" || strings.IndexByte(path, 0) >= 0 {
		return localPath{}, errors.New("empty path or path containing NUL")
	}
	if rest, ok := strings.CutPrefix(path, `\\?\`); ok {
		// Extended-length paths are literal: no separators other than
		// backslash and no dot components are normalized away.
		if len(rest) < 3 || !driveLetter(rest[0]) || rest[1] != ':' || rest[2] != '\\' || strings.IndexByte(rest, '/') >= 0 {
			return localPath{}, errors.New("extended-length path is not a local drive path")
		}
		p := localPath{drive: upper(rest[0])}
		// One trailing backslash names the same directory.
		if rest = strings.TrimSuffix(rest[3:], `\`); rest == "" {
			return p, nil
		}
		for _, part := range strings.Split(rest, `\`) {
			if err := checkComponent(part); err != nil {
				return localPath{}, err
			}
			p.parts = append(p.parts, part)
		}
		return p, nil
	}
	if isSeparator(path[0]) {
		if len(path) > 1 && isSeparator(path[1]) {
			return localPath{}, errors.New("UNC and device paths are not supported")
		}
		return localPath{}, errors.New("root-relative path without a drive is not supported")
	}
	if len(path) >= 2 && path[1] == ':' {
		if !driveLetter(path[0]) {
			return localPath{}, errors.New("invalid drive letter")
		}
		if len(path) == 2 || !isSeparator(path[2]) {
			return localPath{}, errors.New("drive-relative path is not supported")
		}
		return appendLexical(localPath{drive: upper(path[0])}, path[3:])
	}
	// Validate the caller's components before consulting the working
	// directory, so nothing here is rewritten by Win32 normalization.
	for _, part := range splitComponents(path) {
		if part != "." && part != ".." {
			if err := checkComponent(part); err != nil {
				return localPath{}, err
			}
		}
	}
	wd, err := os.Getwd()
	if err != nil {
		return localPath{}, err
	}
	if !filepath.IsAbs(wd) {
		return localPath{}, fmt.Errorf("working directory %q is not absolute", wd)
	}
	base, err := parseLocalPath(wd)
	if err != nil {
		return localPath{}, fmt.Errorf("working directory is not a supported local path: %w", err)
	}
	return appendLexical(base, path)
}

// appendLexical applies ordinary-path components, resolving "." and ".."
// lexically as Win32 does. Walking above the volume root is refused.
func appendLexical(p localPath, rel string) (localPath, error) {
	parts := append([]string(nil), p.parts...)
	for _, part := range splitComponents(rel) {
		switch part {
		case ".":
		case "..":
			if len(parts) == 0 {
				return localPath{}, errors.New("path escapes the volume root")
			}
			parts = parts[:len(parts)-1]
		default:
			if err := checkComponent(part); err != nil {
				return localPath{}, err
			}
			parts = append(parts, part)
		}
	}
	p.parts = parts
	return p, nil
}

// splitComponents splits an ordinary path on either separator, dropping the
// empty components Win32 collapses.
func splitComponents(path string) []string {
	return strings.FieldsFunc(path, func(r rune) bool { return r == '\\' || r == '/' })
}

// checkComponent refuses names Win32 would reinterpret. It runs on the
// caller's spelling, before any normalization could hide the problem.
func checkComponent(name string) error {
	if name == "" || name == "." || name == ".." {
		return fmt.Errorf("invalid path component %q", name)
	}
	for _, r := range name {
		if r < 0x20 || strings.ContainsRune(`<>:"/\|?*`, r) {
			return fmt.Errorf("path component %q contains a reserved character or stream separator", name)
		}
	}
	if last := name[len(name)-1]; last == '.' || last == ' ' {
		return fmt.Errorf("path component %q ends with a dot or space", name)
	}
	if reservedName(name) {
		return fmt.Errorf("path component %q is a reserved device name", name)
	}
	return nil
}

func reservedName(name string) bool {
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

// ntOpen opens one existing component relative to parent; it never creates.
func ntOpen(parent windows.Handle, name string, access, options uint32) (windows.Handle, error) {
	ntName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return windows.InvalidHandle, err
	}
	attributes := windows.OBJECT_ATTRIBUTES{
		RootDirectory: parent,
		ObjectName:    ntName,
		Attributes:    windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
	}
	attributes.Length = uint32(unsafe.Sizeof(attributes))
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&handle, access|windows.SYNCHRONIZE, &attributes, &status, nil,
		windows.FILE_ATTRIBUTE_NORMAL, shareAll, windows.FILE_OPEN, options|syncOptions, 0, 0)
	if err != nil {
		return windows.InvalidHandle, normalizeNTStatus(err)
	}
	return handle, nil
}

// normalizeNTStatus converts an NTSTATUS to the Win32 error Go's os package
// classifies, so os.IsNotExist, os.IsExist and os.IsPermission keep working.
func normalizeNTStatus(err error) error {
	var status windows.NTStatus
	if errors.As(err, &status) {
		return status.Errno()
	}
	return err
}

// openVolumeRoot opens the root directory of a local drive letter volume.
// The DOS device name is an object-manager link, so it cannot carry
// OBJ_DONT_REPARSE; instead the opened handle must be the root directory of a
// local disk volume, which refuses SUBST-style aliases and network drives.
func openVolumeRoot(drive byte) (windows.Handle, error) {
	name, err := windows.NewNTUnicodeString(`\??\` + string(drive) + `:\`)
	if err != nil {
		return windows.InvalidHandle, err
	}
	attributes := windows.OBJECT_ATTRIBUTES{ObjectName: name, Attributes: windows.OBJ_CASE_INSENSITIVE}
	attributes.Length = uint32(unsafe.Sizeof(attributes))
	var handle windows.Handle
	err = windows.NtCreateFile(&handle, directoryAccess|windows.SYNCHRONIZE, &attributes, &windows.IO_STATUS_BLOCK{}, nil,
		windows.FILE_ATTRIBUTE_NORMAL, shareAll, windows.FILE_OPEN, windows.FILE_DIRECTORY_FILE|syncOptions, 0, 0)
	if err != nil {
		return windows.InvalidHandle, normalizeNTStatus(err)
	}
	if err := checkVolumeRoot(handle); err != nil {
		windows.CloseHandle(handle)
		return windows.InvalidHandle, err
	}
	return handle, nil
}

func checkVolumeRoot(handle windows.Handle) error {
	if err := checkObject(handle, true); err != nil {
		return err
	}
	var device struct{ DeviceType, Characteristics uint32 }
	var status windows.IO_STATUS_BLOCK
	r, _, _ := procNtQueryVolumeInformationFile.Call(uintptr(handle), uintptr(unsafe.Pointer(&status)),
		uintptr(unsafe.Pointer(&device)), unsafe.Sizeof(device), fsDeviceInfo)
	if r != 0 {
		return fmt.Errorf("query volume device: %w", windows.NTStatus(r).Errno())
	}
	if device.DeviceType != fileDeviceDisk || device.Characteristics&fileRemoteDevice != 0 {
		return errors.New("volume is not a local disk")
	}
	buf := make([]uint16, windows.MAX_PATH)
	n, err := windows.GetFinalPathNameByHandle(handle, &buf[0], uint32(len(buf)), volumeNameNone)
	if err != nil {
		return fmt.Errorf("query volume root path: %w", err)
	}
	if n >= uint32(len(buf)) || string(utf16.Decode(buf[:n])) != `\` {
		return errors.New("drive letter does not name a volume root directory")
	}
	return nil
}

// checkObject refuses a handle that is not an on-disk, non-reparse,
// non-device object of the wanted kind.
func checkObject(handle windows.Handle, wantDir bool) error {
	kind, err := windows.GetFileType(handle)
	if err != nil {
		return fmt.Errorf("query object type: %w", err)
	}
	if kind != windows.FILE_TYPE_DISK {
		return errors.New("object is not a disk file or directory")
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return fmt.Errorf("query object attributes: %w", err)
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DEVICE) != 0 {
		return errors.New("object is a reparse point or device")
	}
	if isDir := info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0; isDir != wantDir {
		if wantDir {
			return errors.New("object is not a directory")
		}
		return errors.New("object is not a regular file")
	}
	return nil
}

// openChecked opens one component as the wanted kind and checks the handle.
func openChecked(parent windows.Handle, name string, access uint32, dir bool) (windows.Handle, error) {
	if err := checkComponent(name); err != nil {
		return windows.InvalidHandle, err
	}
	options := uint32(windows.FILE_NON_DIRECTORY_FILE)
	if dir {
		options = windows.FILE_DIRECTORY_FILE
	}
	handle, err := ntOpen(parent, name, access, options)
	if err != nil {
		return windows.InvalidHandle, err
	}
	if err := checkObject(handle, dir); err != nil {
		windows.CloseHandle(handle)
		return windows.InvalidHandle, err
	}
	return handle, nil
}

// walkDirectory opens the directory named by p from its volume root.
func walkDirectory(p localPath) (windows.Handle, error) {
	dir, err := openVolumeRoot(p.drive)
	if err != nil {
		return windows.InvalidHandle, err
	}
	for i := 0; ; i++ {
		if i == len(p.parts) {
			return dir, nil
		}
		next, err := openChecked(dir, p.parts[i], directoryAccess, true)
		windows.CloseHandle(dir)
		if err != nil {
			return windows.InvalidHandle, err
		}
		dir = next
	}
}

func pathError(op, path string, err error) error {
	if err == nil {
		return nil
	}
	return &os.PathError{Op: op, Path: path, Err: err}
}

// withHandle runs fn with file's handle while keeping file open.
func withHandle(file *os.File, fn func(windows.Handle) error) error {
	if file == nil {
		return os.ErrInvalid
	}
	conn, err := file.SyscallConn()
	if err != nil {
		return err
	}
	var fnErr error
	if err := conn.Control(func(fd uintptr) { fnErr = fn(windows.Handle(fd)) }); err != nil {
		return err
	}
	return fnErr
}

// OpenDirectory opens the local directory named by path, walking one
// component at a time from the volume root. It refuses UNC, device and
// ambiguous path forms, and any reparse point, device or non-directory along
// the way. It applies no ownership or ACL policy; callers use
// CheckSafeDirectory or CheckPrivate on the result.
func OpenDirectory(path string) (*os.File, error) {
	p, err := parseLocalPath(path)
	if err != nil {
		return nil, pathError("open", path, err)
	}
	handle, err := walkDirectory(p)
	if err != nil {
		return nil, pathError("open", path, err)
	}
	return os.NewFile(uintptr(handle), p.String()), nil
}

// OpenDirectoryAt opens the single-component directory name below parent.
func OpenDirectoryAt(parent *os.File, name string) (*os.File, error) {
	var handle windows.Handle
	err := withHandle(parent, func(h windows.Handle) (err error) {
		handle, err = openChecked(h, name, directoryAccess, true)
		return err
	})
	if err != nil {
		return nil, pathError("openat", name, err)
	}
	return os.NewFile(uintptr(handle), childName(parent, name)), nil
}

// OpenRegularAt opens the existing single-component regular file name below
// parent. flags holds only an access mode (os.O_RDONLY, os.O_WRONLY or
// os.O_RDWR); creation and truncation are not supported.
func OpenRegularAt(parent *os.File, name string, flags int) (*os.File, error) {
	access, err := fileAccess(flags)
	if err != nil {
		return nil, pathError("openat", name, err)
	}
	var handle windows.Handle
	err = withHandle(parent, func(h windows.Handle) (err error) {
		handle, err = openChecked(h, name, access, false)
		return err
	})
	if err != nil {
		return nil, pathError("openat", name, err)
	}
	return os.NewFile(uintptr(handle), childName(parent, name)), nil
}

// fileAccess maps an access mode to file rights. Every handle can read its
// attributes and security descriptor for later checks.
func fileAccess(flags int) (uint32, error) {
	if flags&^(os.O_RDONLY|os.O_WRONLY|os.O_RDWR) != 0 {
		return 0, fmt.Errorf("unsupported open flags %#x", flags)
	}
	base := uint32(windows.FILE_READ_ATTRIBUTES | windows.READ_CONTROL | windows.SYNCHRONIZE)
	switch flags & (os.O_RDONLY | os.O_WRONLY | os.O_RDWR) {
	case os.O_RDONLY:
		return base | windows.FILE_GENERIC_READ, nil
	case os.O_WRONLY:
		return base | windows.FILE_GENERIC_WRITE, nil
	case os.O_RDWR:
		return base | windows.FILE_GENERIC_READ | windows.FILE_GENERIC_WRITE, nil
	}
	return 0, fmt.Errorf("unsupported access mode %#x", flags)
}

func childName(parent *os.File, name string) string {
	return parent.Name() + `\` + name
}
