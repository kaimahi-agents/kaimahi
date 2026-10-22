//go:build linux || darwin

package mcp

import (
	"errors"
	"golang.org/x/sys/unix"
	"os"
	"syscall"
)

func openTarget(root *os.Root, name string) (*os.File, error) {
	return root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
}
func singleLink(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	s, ok := info.Sys().(*syscall.Stat_t)
	return ok && s.Nlink == 1
}
func lockFailure(err error) error {
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return refusal("busy")
	}
	return refusal("lock_unavailable")
}

func lockTarget(f *os.File) (func(), error) {
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return nil, err
	}
	return func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN) }, nil
}
