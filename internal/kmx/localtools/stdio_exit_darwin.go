package localtools

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// A zero-timeout process event observes exit without reaping the leader. ESRCH
// on registration also means our still-unreaped child has already exited.
func stdioLeaderExited(pid int) (exited bool, err error) {
	syscall.ForkLock.RLock()
	fd, err := unix.Kqueue()
	if err == nil {
		unix.CloseOnExec(fd)
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		return false, err
	}
	defer func() {
		if unix.Close(fd) != nil {
			err = ErrStdioCleanup
		}
	}()
	var events [1]unix.Kevent_t
	changes := []unix.Kevent_t{{Ident: uint64(pid), Filter: unix.EVFILT_PROC, Flags: unix.EV_ADD | unix.EV_ENABLE, Fflags: unix.NOTE_EXIT}}
	n, err := unix.Kevent(fd, changes, events[:], &unix.Timespec{})
	if err == unix.ESRCH {
		return true, nil
	}
	if err != nil || n == 0 {
		return false, err
	}
	if events[0].Flags&unix.EV_ERROR != 0 {
		if syscall.Errno(events[0].Data) == unix.ESRCH {
			return true, nil
		}
		return false, ErrStdioCleanup
	}
	return events[0].Fflags&unix.NOTE_EXIT != 0, nil
}
