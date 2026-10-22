//go:build !windows

package app

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

// readRunPromptFile opens a regular file without following a substituted link.
// The size check applies to bytes actually read, even if the file grows.
func readRunPromptFile(path string) (string, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return "", fmt.Errorf("read prompt file (must be regular, not a link): %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("inspect prompt file: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return "", fmt.Errorf("prompt file must be regular and no larger than 1 MiB")
	}
	body, err := io.ReadAll(io.LimitReader(file, 1<<20+1))
	if err != nil {
		return "", fmt.Errorf("read prompt file: %w", err)
	}
	if len(body) > 1<<20 {
		return "", fmt.Errorf("prompt file exceeds 1 MiB")
	}
	return string(body), nil
}

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

// Public inputs need not have private permissions. Open the parent as a
// directory, then use the existing nofollow/nonblocking final-component seam;
// neither a final FIFO nor a FIFO masquerading as a parent can block open.
func readSuiteEvaluationVerifyFile(path string) ([]byte, error) {
	dir, err := os.OpenFile(filepath.Dir(path), os.O_RDONLY|unix.O_DIRECTORY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, suiteEvaluationRefuse("missing")
		}
		return nil, suiteEvaluationRefuse("malformed")
	}
	file, openErr := openKagentFile(dir, filepath.Base(path))
	closeErr := dir.Close()
	if openErr != nil {
		if closeErr == nil && errors.Is(openErr, os.ErrNotExist) {
			return nil, suiteEvaluationRefuse("missing")
		}
		return nil, suiteEvaluationRefuse("malformed")
	}
	raw, readErr := readSuiteEvaluationVerifyOpened(file)
	fileCloseErr := file.Close()
	if closeErr != nil || fileCloseErr != nil {
		return nil, suiteEvaluationRefuse("malformed")
	}
	return raw, readErr
}
