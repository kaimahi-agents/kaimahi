package app

import (
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const darwinPathCaseSensitive = 11

func fileFailureLeaf(name string) string {
	leaf := filepath.Base(name)
	sensitive, err := unix.Pathconf(filepath.Dir(name), darwinPathCaseSensitive)
	if err != nil || sensitive != 0 {
		return leaf
	}
	return strings.ToLower(leaf)
}
