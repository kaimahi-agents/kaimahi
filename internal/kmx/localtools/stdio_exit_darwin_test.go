package localtools

import (
	"testing"

	"golang.org/x/sys/unix"
)

func TestStdioKeventRetriesInterrupts(t *testing.T) {
	calls := 0
	n, err := stdioKeventNoIntr(func(timeout *unix.Timespec) (int, error) {
		calls++
		if *timeout != (unix.Timespec{}) {
			t.Fatalf("retry timeout = %+v", *timeout)
		}
		if calls < 3 {
			timeout.Sec = 1
			return 0, unix.EINTR
		}
		return 1, nil
	})
	if err != nil || n != 1 || calls != 3 {
		t.Fatalf("kevent retry = n=%d err=%v calls=%d", n, err, calls)
	}
}

func TestStdioKeventBoundsInterrupts(t *testing.T) {
	calls := 0
	n, err := stdioKeventNoIntr(func(*unix.Timespec) (int, error) {
		calls++
		return 0, unix.EINTR
	})
	if n != 0 || err != unix.EINTR || calls != stdioKeventInterruptLimit {
		t.Fatalf("kevent interrupt bound = n=%d err=%v calls=%d", n, err, calls)
	}
}

func TestStdioKeventDoesNotRetryOtherErrors(t *testing.T) {
	calls := 0
	n, err := stdioKeventNoIntr(func(*unix.Timespec) (int, error) {
		calls++
		return 0, unix.EINVAL
	})
	if n != 0 || err != unix.EINVAL || calls != 1 {
		t.Fatalf("kevent error = n=%d err=%v calls=%d", n, err, calls)
	}
}
