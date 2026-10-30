package mcp

import (
	"errors"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

var renameNoReplace = func(dir int, from, to string) error {
	return unix.Renameat2(dir, from, dir, to, unix.RENAME_NOREPLACE)
}

var linkPublication = (*os.Root).Link

func renameExclusive(root *os.Root, from, to string) error {
	return renameExclusiveObserved(root, from, to, nil)
}

// Keep the published object's identity, not a later observation of its name.
func renameExclusiveObserved(root *os.Root, from, to string, published *os.FileInfo) error {
	if published != nil {
		info, err := root.Lstat(from)
		if err != nil {
			return err
		}
		*published = info
	}
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	err = renameNoReplace(int(dir.Fd()), from, to)
	if !publicationUnsupported(err) {
		return err
	}
	if err = linkPublication(root, from, to); err == nil {
		// Both callers also defer temp cleanup. Once published, a cleanup
		// failure must not misreport that the target was never created.
		_ = root.Remove(from)
		return nil
	}
	if !publicationUnsupported(err) && !errors.Is(err, unix.EPERM) {
		return err
	}
	return copyExclusive(root, from, to, published)
}

func publicationUnsupported(err error) bool {
	return errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.ENOTSUP)
}

func copyExclusive(root *os.Root, from, to string, published *os.FileInfo) error {
	source, err := root.Open(from)
	if err != nil {
		return err
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		return err
	}
	// Some mounts support neither no-replace rename nor hard links. Exclusive
	// creation still prevents overwrite, but this last fallback is not atomic:
	// readers (or a crash) can observe an incomplete target until copy finishes.
	target, err := root.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return err
	}
	opened, statErr := target.Stat()
	if published != nil {
		*published = opened
	}
	_, copyErr := io.Copy(target, source)
	syncErr := target.Sync()
	closeErr := target.Close()
	if err := errors.Join(statErr, copyErr, syncErr, closeErr); err != nil {
		// Best-effort cleanup; external filesystem mutation is not contained.
		if current, statErr := root.Lstat(to); statErr == nil && opened != nil && os.SameFile(current, opened) {
			_ = root.Remove(to)
		}
		return err
	}
	_ = root.Remove(from)
	return nil
}
