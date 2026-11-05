package oras

import "golang.org/x/sys/unix"

func publishLayoutNoReplace(stage, destination string) error {
	return noReplaceError(unix.RenamexNp(stage, destination, unix.RENAME_EXCL))
}
