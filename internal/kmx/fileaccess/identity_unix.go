//go:build !windows

package fileaccess

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// PhysicalIdentity returns a key for the object behind file: its device and
// inode numbers.
func PhysicalIdentity(file *os.File) (string, error) {
	if file == nil {
		return "", os.ErrInvalid
	}
	conn, err := file.SyscallConn()
	if err != nil {
		return "", err
	}
	var st unix.Stat_t
	var statErr error
	if err := conn.Control(func(fd uintptr) { statErr = unix.Fstat(int(fd), &st) }); err != nil {
		return "", err
	}
	if statErr != nil {
		return "", &os.PathError{Op: "fstat", Path: file.Name(), Err: statErr}
	}
	return fmt.Sprintf("%x-%x", uint64(st.Dev), uint64(st.Ino)), nil
}
