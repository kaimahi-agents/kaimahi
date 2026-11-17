package localtools

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// killCommandGroup must run before Cmd.Wait: the owned, unreaped leader pins
// the process-group identity. Darwin's killpg1 excludes zombies from its signal
// iteration and can return EPERM when the group has no live members. Accept that
// case only with positive kernel observation of the owned leader and the entire
// group as zombies; EPERM alone is never proof of cleanup.
func killCommandGroup(pid int) error {
	err := syscall.Kill(-pid, syscall.SIGKILL)
	if !errors.Is(err, syscall.EPERM) {
		return err
	}
	members, readErr := unix.SysctlKinfoProcSlice("kern.proc.pgrp", pid)
	if readErr == nil && commandGroupExited(pid, os.Getpid(), members) {
		return nil
	}
	return err
}

func commandGroupExited(pid, parent int, members []unix.KinfoProc) bool {
	const zombie = 5 // SZOMB, Darwin sys/proc.h
	if len(members) == 0 || len(members) > 1024 {
		return false
	}
	leader := false
	for _, member := range members {
		if member.Eproc.Pgid != int32(pid) || member.Proc.P_stat != zombie {
			return false
		}
		if member.Proc.P_pid == int32(pid) {
			if member.Eproc.Ppid != int32(parent) {
				return false
			}
			leader = true
		}
	}
	return leader
}
