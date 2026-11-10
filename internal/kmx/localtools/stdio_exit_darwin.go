package localtools

import (
	"syscall"

	"golang.org/x/sys/unix"
)

const stdioKeventInterruptLimit = 8

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
	n, err := stdioKeventNoIntr(func(timeout *unix.Timespec) (int, error) {
		return unix.Kevent(fd, changes, events[:], timeout)
	})
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

func stdioKeventNoIntr(call func(*unix.Timespec) (int, error)) (int, error) {
	for range stdioKeventInterruptLimit {
		n, err := call(&unix.Timespec{})
		if err == unix.EINTR {
			continue
		}
		return n, err
	}
	return 0, unix.EINTR
}
