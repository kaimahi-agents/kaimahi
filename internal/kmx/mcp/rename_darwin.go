package mcp

import (
	"golang.org/x/sys/unix"
	"os"
)

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
	return unix.RenameatxNp(int(dir.Fd()), from, int(dir.Fd()), to, unix.RENAME_EXCL)
}
