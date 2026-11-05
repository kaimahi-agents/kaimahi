//go:build linux || darwin

package oras

import (
	"errors"
	"golang.org/x/sys/unix"
)

func noReplaceError(err error) error {
	if errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.ENOSYS) {
		return errors.Join(errors.ErrUnsupported, err)
	}
	return err
}
