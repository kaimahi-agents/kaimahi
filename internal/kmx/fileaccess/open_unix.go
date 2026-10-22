//go:build !windows

package fileaccess

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// These are the descriptor flags KMX's existing single-component openat
// walks use: never follow a final symbolic link, never block on a FIFO and
// never leak descriptors into children.
const (
	directoryFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_CLOEXEC
	fileFlags      = unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_CLOEXEC
)

// OpenDirectory opens the directory named by path, walking one component at
// a time from the filesystem root without following symbolic links. It
// applies no ownership or mode policy; callers use CheckSafeDirectory or
// CheckPrivate on the result.
func OpenDirectory(path string) (*os.File, error) {
	if path == "" || strings.IndexByte(path, 0) >= 0 {
		return nil, &os.PathError{Op: "open", Path: path, Err: unix.EINVAL}
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	fd, err := unix.Open("/", directoryFlags, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: "/", Err: err}
	}
	dir := os.NewFile(uintptr(fd), "/")
	for _, part := range strings.Split(strings.TrimPrefix(absolute, "/"), "/") {
		if part == "" {
			continue
		}
		next, err := OpenDirectoryAt(dir, part)
		dir.Close()
		if err != nil {
			return nil, &os.PathError{Op: "open", Path: path, Err: errors.Unwrap(err)}
		}
		dir = next
	}
	return dir, nil
}

// OpenDirectoryAt opens the single-component directory name below parent,
// refusing a symbolic link.
func OpenDirectoryAt(parent *os.File, name string) (*os.File, error) {
	return openAt(parent, name, directoryFlags, 0)
}

// OpenRegularAt opens the existing single-component regular file name below
// parent, refusing a symbolic link and any non-regular file. flags holds only
// an access mode (os.O_RDONLY, os.O_WRONLY or os.O_RDWR); creation and
// truncation are not supported by this operation.
func OpenRegularAt(parent *os.File, name string, flags int) (*os.File, error) {
	if flags != os.O_RDONLY && flags != os.O_WRONLY && flags != os.O_RDWR {
		return nil, &os.PathError{Op: "openat", Path: name, Err: unix.EINVAL}
	}
	file, err := openAt(parent, name, flags|fileFlags, 0)
	if err != nil {
		return nil, err
	}
	if err := requireRegular(file); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func openAt(parent *os.File, name string, flags int, perm uint32) (*os.File, error) {
	if !singleComponent(name) {
		return nil, &os.PathError{Op: "openat", Path: name, Err: unix.EINVAL}
	}
	if parent == nil {
		return nil, &os.PathError{Op: "openat", Path: name, Err: os.ErrInvalid}
	}
	conn, err := parent.SyscallConn()
	if err != nil {
		return nil, &os.PathError{Op: "openat", Path: name, Err: err}
	}
	fd := -1
	var openErr error
	if err := conn.Control(func(dirfd uintptr) {
		fd, openErr = unix.Openat(int(dirfd), name, flags, perm)
	}); err != nil {
		return nil, &os.PathError{Op: "openat", Path: name, Err: err}
	}
	if openErr != nil {
		return nil, &os.PathError{Op: "openat", Path: name, Err: openErr}
	}
	return os.NewFile(uintptr(fd), filepath.Join(parent.Name(), name)), nil
}

// singleComponent refuses names openat would resolve through more than one
// directory entry.
func singleComponent(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\x00")
}

func requireRegular(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return &os.PathError{Op: "openat", Path: file.Name(), Err: errors.New("not a regular file")}
	}
	return nil
}
