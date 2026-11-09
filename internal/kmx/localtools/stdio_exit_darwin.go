package localtools

import (
	"syscall"

	"golang.org/x/sys/unix"
)

const stdioKeventInterruptLimit = 8

// A persistent kqueue observes leader exit without reaping it. Holding one
// registration closes the race where a short-lived process exits between
// independent registration attempts; Cmd.Wait remains the sole reaper after
// the process group has been killed.
func newStdioLeaderObserver(pid int) (*stdioExitObserver, error) {
	syscall.ForkLock.RLock()
	fd, err := unix.Kqueue()
	if err == nil {
		unix.CloseOnExec(fd)
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		return nil, err
	}
	closed := false
	closeObserver := func() error {
		if closed {
			return nil
		}
		closed = true
		if unix.Close(fd) != nil {
			return ErrStdioCleanup
		}
		return nil
	}
	var events [1]unix.Kevent_t
	changes := []unix.Kevent_t{{Ident: uint64(pid), Filter: unix.EVFILT_PROC, Flags: unix.EV_ADD | unix.EV_ENABLE | unix.EV_CLEAR, Fflags: unix.NOTE_EXIT}}
	n, err := stdioKeventNoIntr(func(timeout *unix.Timespec) (int, error) {
		return unix.Kevent(fd, changes, events[:], timeout)
	})
	if err == unix.ESRCH {
		return &stdioExitObserver{observe: func() (bool, error) { return true, nil }, close: closeObserver}, nil
	}
	if err != nil {
		_ = closeObserver()
		return nil, err
	}
	exited := false
	if n == 1 {
		event := events[0]
		if event.Flags&unix.EV_ERROR != 0 {
			if errno := syscall.Errno(event.Data); errno == unix.ESRCH {
				exited = true
			} else if errno != 0 {
				_ = closeObserver()
				return nil, ErrStdioCleanup
			}
		} else {
			exited = event.Fflags&unix.NOTE_EXIT != 0
		}
	}
	return &stdioExitObserver{observe: func() (bool, error) {
		if exited {
			return true, nil
		}
		n, err := stdioKeventNoIntr(func(timeout *unix.Timespec) (int, error) { return unix.Kevent(fd, nil, events[:], timeout) })
		if err != nil {
			return false, err
		}
		if n == 0 {
			return false, nil
		}
		event := events[0]
		if event.Flags&unix.EV_ERROR != 0 {
			if errno := syscall.Errno(event.Data); errno == unix.ESRCH {
				exited = true
				return true, nil
			} else if errno != 0 {
				return false, ErrStdioCleanup
			}
		}
		exited = event.Fflags&unix.NOTE_EXIT != 0
		return exited, nil
	}, close: closeObserver}, nil
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
