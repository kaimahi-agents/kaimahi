package app

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const kagentVersionMarker = "kaimahi.dev/runtime-version"

const maxKagentFileBytes = 1 << 20

// Root.OpenFile follows even in-root symlinks. Use single-component openat
// calls instead, retaining each checked directory descriptor for the next step.
// O_DIRECTORY and O_NONBLOCK prevent a substituted FIFO from blocking an open.

func openKagentFile(dir *os.File, name string) (*os.File, error) {
	if name == "." || name == ".." || filepath.Base(name) != name {
		return nil, fmt.Errorf("Kagent file name must be a single path component")
	}
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open Kagent bounded regular file, not a link: %w", err)
	}
	return os.NewFile(uintptr(fd), name), nil
}

// The size limit applies to bytes actually read, not just a pre-read stat.
// Revalidate the same descriptor to refuse growth, truncation or permission
// changes; pathname substitution cannot redirect this read to another file.

func readKagentOpenedFile(file *os.File, before os.FileInfo, private bool) ([]byte, error) {
	if !before.Mode().IsRegular() || before.Size() > maxKagentFileBytes || private && before.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("Kagent file must be a bounded regular file (private for receipts), not a link")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxKagentFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read Kagent file: %w", err)
	}
	after, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect opened Kagent file: %w", err)
	}
	if len(raw) > maxKagentFileBytes || int64(len(raw)) != before.Size() || after.Size() != before.Size() || after.Mode() != before.Mode() || !after.ModTime().Equal(before.ModTime()) {
		return nil, fmt.Errorf("Kagent file changed or exceeded its bound during read")
	}
	return raw, nil
}
