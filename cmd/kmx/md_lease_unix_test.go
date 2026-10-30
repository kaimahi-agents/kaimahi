//go:build !windows

package main

import (
	"golang.org/x/sys/unix"
	"os"
)

func testMDLease(f *os.File, lock bool) error {
	flags := unix.LOCK_UN
	if lock {
		flags = unix.LOCK_EX | unix.LOCK_NB
	}
	return unix.Flock(int(f.Fd()), flags)
}
