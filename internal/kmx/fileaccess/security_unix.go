//go:build !windows

package fileaccess

import (
	"errors"
	"os"
)

// CheckPrivate refuses file unless it is a regular file or directory with no
// group or other permission bits, the exact mode rule KMX applies to private
// state. root has no extra meaning on Unix, matching AgentSuite.
func CheckPrivate(file *os.File, _ bool) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() && !info.IsDir() {
		return errors.New("private object is not a regular file or directory")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("private object grants group or other access")
	}
	return nil
}

// CheckSafeDirectory refuses file unless it is a directory without group or
// other write permission; a sticky directory is tolerated as an ancestor but
// never as an immediate parent. This is KMX's existing ancestor rule.
func CheckSafeDirectory(file *os.File, immediate bool) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("object is not a directory")
	}
	if info.Mode().Perm()&0o022 != 0 && (immediate || info.Mode()&os.ModeSticky == 0) {
		return errors.New("directory is writable by group or other")
	}
	return nil
}
