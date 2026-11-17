//go:build unix

package localtools

import (
	"errors"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestCommandObserverClosesAfterFailedWait(t *testing.T) {
	closed := 0
	observer := &stdioExitObserver{close: func() error { closed++; return nil }}
	if err := finishCommandWait(func() error { return ErrCommandCleanup }, observer); !errors.Is(err, ErrCommandCleanup) || closed != 1 {
		t.Fatalf("cleanup=%v closes=%d", err, closed)
	}
}

func TestCommandObserverPreservesExitedLeaderStatus(t *testing.T) {
	for range 10 {
		cmd := exec.Command("/bin/sh", "-c", "exit 7")
		cmd.Env = []string{}
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		observer, err := newStdioLeaderObserver(cmd.Process.Pid)
		if err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			t.Fatal(err)
		}
		deadline := time.Now().Add(2 * time.Second)
		for {
			exited, observeErr := observer.observe()
			if observeErr != nil || time.Now().After(deadline) {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				_ = observer.close()
				t.Fatalf("observe exit: exited=%v err=%v", exited, observeErr)
			}
			if exited {
				break
			}
			time.Sleep(time.Millisecond)
		}
		killErr := killCommandGroup(cmd.Process.Pid)
		waitErr := waitCommandExit(cmd.Wait)
		closeErr := observer.close()
		var exit *exec.ExitError
		if killErr != nil && !errors.Is(killErr, syscall.ESRCH) || !errors.As(waitErr, &exit) || exit.ExitCode() != 7 || closeErr != nil {
			t.Fatalf("terminal status: kill=%v wait=%v state=%v close=%v", killErr, waitErr, cmd.ProcessState, closeErr)
		}
	}
}
