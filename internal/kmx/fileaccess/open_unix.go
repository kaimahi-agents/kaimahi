//go:build !windows

package fileaccess

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// These are the descriptor flags used by KMX's existing checked walks: never
// follow a final symbolic link, never block on a FIFO, and never leak a
// descriptor into a child process.
const (
	directoryFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_CLOEXEC
	fileFlags      = unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_CLOEXEC
)

// OpenDirectory opens an existing directory one component at a time from the
// filesystem root. It applies no ownership or mode policy; callers use
// CheckSafeDirectory or CheckPrivate on the returned handle.
func OpenDirectory(ctx context.Context, path string) (*Directory, error) {
	if err := checkContext(ctx); err != nil {
		return nil, wrapAccess("open directory", err)
	}
	if err := validateUnixPath(path); err != nil {
		return nil, wrapAccess("open directory", err)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, wrapAccess("open directory", err)
	}
	fd, err := unix.Open("/", directoryFlags, 0)
	if err != nil {
		return nil, wrapAccess("open directory", err)
	}
	dir := os.NewFile(uintptr(fd), "/")
	for _, part := range strings.Split(strings.TrimPrefix(absolute, "/"), "/") {
		if part == "" {
			continue
		}
		if err := checkContext(ctx); err != nil {
			dir.Close()
			return nil, wrapAccess("open directory", err)
		}
		next, err := openAt(ctx, dir, part, directoryFlags)
		dir.Close()
		if err != nil {
			return nil, wrapAccess("open directory", err)
		}
		dir = next
	}
	return &Directory{file: dir}, nil
}

// OpenDirectoryAt opens one existing directory immediately below parent.
func OpenDirectoryAt(ctx context.Context, parent *Directory, name string) (*Directory, error) {
	file, err := openAt(ctx, rawObject(parent), name, directoryFlags)
	if err != nil {
		return nil, wrapAccess("open directory entry", err)
	}
	return &Directory{file: file}, nil
}

// OpenRegularAt opens one existing regular file immediately below parent for
// reading only.
func OpenRegularAt(ctx context.Context, parent *Directory, name string) (*File, error) {
	file, err := openAt(ctx, rawObject(parent), name, fileFlags)
	if err != nil {
		return nil, wrapAccess("open regular file", err)
	}
	if err := requireRegular(ctx, file); err != nil {
		file.Close()
		return nil, wrapAccess("open regular file", err)
	}
	return &File{file: file}, nil
}

func openAt(ctx context.Context, parent *os.File, name string, flags int) (*os.File, error) {
	return openAtMode(ctx, parent, name, flags, 0)
}

func openAtMode(ctx context.Context, parent *os.File, name string, flags int, mode uint32) (*os.File, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	if !singleUnixComponent(name) {
		return nil, unsafeCause("entry name is not one component")
	}
	if parent == nil {
		return nil, unsafeCause("parent handle is absent")
	}
	conn, err := parent.SyscallConn()
	if err != nil {
		return nil, unreadableCause("parent handle is unavailable")
	}
	fd := -1
	var openErr error
	if err := conn.Control(func(dirfd uintptr) {
		fd, openErr = unix.Openat(int(dirfd), name, flags, mode)
	}); err != nil {
		return nil, err
	}
	if openErr != nil {
		if errors.Is(openErr, unix.ELOOP) || errors.Is(openErr, unix.ENOTDIR) || errors.Is(openErr, unix.EINVAL) {
			return nil, &classifiedCause{class: ClassUnsafe, cause: openErr}
		}
		return nil, openErr
	}
	file := os.NewFile(uintptr(fd), filepath.Join(parent.Name(), name))
	if err := checkContext(ctx); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func validateUnixPath(path string) error {
	if path == "" || strings.IndexByte(path, 0) >= 0 {
		return unsafeCause("path is empty or contains NUL")
	}
	if path != "/" && filepath.Clean(path) != path {
		return unsafeCause("path is not clean")
	}
	for _, part := range strings.Split(path, "/") {
		if part == "." || part == ".." {
			return unsafeCause("path contains traversal")
		}
	}
	return nil
}

func singleUnixComponent(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\x00")
}

func requireRegular(ctx context.Context, file *os.File) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if err := checkContext(ctx); err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return unsafeCause("object is not a regular file")
	}
	return nil
}
