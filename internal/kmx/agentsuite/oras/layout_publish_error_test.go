//go:build linux || darwin

package oras

import (
	"errors"
	"golang.org/x/sys/unix"
	"io/fs"
	"testing"
)

func TestCreateOnlyUnsupportedClassification(t *testing.T) {
	for _, err := range []error{unix.EINVAL, unix.ENOTSUP, unix.ENOSYS} {
		if !errors.Is(noReplaceError(err), errors.ErrUnsupported) {
			t.Fatalf("unsupported syscall not classified: %v", err)
		}
	}
	for _, err := range []error{nil, unix.EEXIST, unix.EACCES, unix.EIO} {
		if noReplaceError(err) != err {
			t.Fatalf("ordinary outcome changed: %v", err)
		}
	}
	if !errors.Is(noReplaceError(unix.EEXIST), fs.ErrExist) {
		t.Fatal("collision classification lost")
	}
}
