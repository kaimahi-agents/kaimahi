//go:build unix

package localpreview

import (
	"os"

	"golang.org/x/sys/unix"
)

// tryLock takes an exclusive, nonblocking advisory lock on f. It reports false
// when another open file, in this or another process, holds the lock.
func tryLock(f *os.File) bool {
	return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) == nil
}

func unlock(f *os.File) {
	_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
}
