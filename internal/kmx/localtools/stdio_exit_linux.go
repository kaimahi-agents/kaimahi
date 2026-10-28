package localtools

import "golang.org/x/sys/unix"

// WNOWAIT observes an exited leader without releasing the PID that identifies
// its group. Cmd.Wait remains the only reaper, after the group has been killed.
func stdioLeaderExited(pid int) (bool, error) {
	var info unix.Siginfo
	for {
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOHANG|unix.WNOWAIT, nil)
		if err == unix.EINTR {
			continue
		}
		return info.Signo != 0, err
	}
}
