package oras

import "golang.org/x/sys/unix"

func publishLayoutNoReplace(stage, destination string) error {
	return noReplaceError(unix.Renameat2(unix.AT_FDCWD, stage, unix.AT_FDCWD, destination, unix.RENAME_NOREPLACE))
}
