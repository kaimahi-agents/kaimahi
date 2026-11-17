//go:build unix && !darwin

package localtools

import "syscall"

func killCommandGroup(pid int) error {
	return syscall.Kill(-pid, syscall.SIGKILL)
}
