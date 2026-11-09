//go:build linux || darwin

package localtools

import (
	"fmt"
	"os"
	"syscall"
)

func commandFileIdentity(info os.FileInfo) (string, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("%x:%x", uint64(stat.Dev), uint64(stat.Ino)), true
}
