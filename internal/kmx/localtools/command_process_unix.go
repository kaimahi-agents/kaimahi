//go:build unix

package localtools

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"slices"
	"sync"
	"syscall"
	"time"
)

type commandCapture struct {
	mu        sync.Mutex
	remaining int
	overflow  chan struct{}
	once      sync.Once
	stdout    bytes.Buffer
	stderr    bytes.Buffer
}

func newCommandCapture() *commandCapture {
	return &commandCapture{remaining: commandOutputLimit, overflow: make(chan struct{})}
}

func (c *commandCapture) copy(dst *bytes.Buffer, src io.Reader) {
	buf := make([]byte, 4096)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			c.mu.Lock()
			keep := min(n, c.remaining)
			if keep > 0 {
				_, _ = dst.Write(buf[:keep])
				c.remaining -= keep
			}
			if keep != n {
				c.once.Do(func() { close(c.overflow) })
			}
			c.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func runCommand(ctx context.Context, cfg CommandConfig, check func(...string) error) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	checkIdentity := func() bool {
		commandInfo, commandErr := os.Stat(cfg.Command)
		dirInfo, dirErr := os.Stat(cfg.Dir)
		return commandErr == nil && dirErr == nil && cfg.commandInfo != nil && cfg.dirInfo != nil && os.SameFile(cfg.commandInfo, commandInfo) && os.SameFile(cfg.dirInfo, dirInfo) && commandInfo.Mode().IsRegular() && commandInfo.Mode().Perm()&0111 != 0 && dirInfo.IsDir()
	}
	if !checkIdentity() {
		return nil, ErrCommand
	}
	cmd := exec.Command(cfg.Command, cfg.Args...)
	cmd.Dir = cfg.Dir
	cmd.Env = []string{}
	keys := make([]string, 0, len(cfg.Env))
	for key := range cfg.Env {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		cmd.Env = append(cmd.Env, key+"="+cfg.Env[key])
	}
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		return nil, ErrCommand
	}
	defer devNull.Close()
	cmd.Stdin = devNull
	stdout, childOut, err := os.Pipe()
	if err != nil {
		return nil, ErrCommand
	}
	stderr, childErr, err := os.Pipe()
	if err != nil {
		_ = stdout.Close()
		_ = childOut.Close()
		return nil, ErrCommand
	}
	cmd.Stdout, cmd.Stderr = childOut, childErr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		_ = stdout.Close()
		_ = childOut.Close()
		_ = stderr.Close()
		_ = childErr.Close()
		return nil, ErrCommand
	}
	_ = childOut.Close()
	_ = childErr.Close()
	if !checkIdentity() {
		killErr := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = stdout.Close()
		_ = stderr.Close()
		waitErr := waitCommandExit(cmd.Wait)
		if errors.Is(waitErr, ErrCommandCleanup) || killErr != nil && !errors.Is(killErr, syscall.ESRCH) {
			return nil, ErrCommandCleanup
		}
		return nil, ErrCommand
	}
	capture := newCommandCapture()
	var readers sync.WaitGroup
	readers.Add(2)
	go func() { defer readers.Done(); capture.copy(&capture.stdout, stdout) }()
	go func() { defer readers.Done(); capture.copy(&capture.stderr, stderr) }()

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	completed := false
	cleanupFailure := false
	leaderExited := false
	for !completed {
		select {
		case <-ctx.Done():
			completed = true
		case <-capture.overflow:
			completed = true
		case <-ticker.C:
			exited, observeErr := stdioLeaderExited(cmd.Process.Pid)
			if observeErr != nil {
				cleanupFailure = true
				completed = true
			} else if exited {
				leaderExited = true
				completed = true
			}
		}
	}
	killErr := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if killErr != nil && !errors.Is(killErr, syscall.ESRCH) {
		cleanupFailure = true
	}
	readDone := make(chan struct{})
	go func() { readers.Wait(); close(readDone) }()
	select {
	case <-readDone:
	case <-time.After(time.Second):
		cleanupFailure = true
		_ = stdout.Close()
		_ = stderr.Close()
	}
	_ = stdout.Close()
	_ = stderr.Close()
	waitErr := waitCommandExit(cmd.Wait)
	if cleanupFailure || errors.Is(waitErr, ErrCommandCleanup) {
		return nil, ErrCommandCleanup
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case <-capture.overflow:
		return nil, ErrCommand
	default:
	}
	exitCode := 0
	if waitErr != nil {
		var exit *exec.ExitError
		if !errors.As(waitErr, &exit) {
			return nil, ErrCommandCleanup
		}
		status, ok := exit.Sys().(syscall.WaitStatus)
		if !ok {
			return nil, ErrCommand
		}
		if status.Signaled() {
			return nil, ErrCommand
		}
		exitCode = exit.ExitCode()
		if exitCode < 0 {
			return nil, ErrCommand
		}
	}
	if !leaderExited {
		return nil, ErrCommand
	}
	capture.mu.Lock()
	out, diagnostic := slices.Clone(capture.stdout.Bytes()), slices.Clone(capture.stderr.Bytes())
	capture.mu.Unlock()
	return commandResult(exitCode, out, diagnostic, check)
}

// waitCommandExit bounds reaping independently of the already-expired invocation
// context. On timeout the sole waiter remains responsible for eventual reaping;
// callers must withhold completion and never signal a potentially reused PID.
func waitCommandExit(wait func() error) error {
	waited := make(chan error, 1)
	go func() { waited <- wait() }()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case err := <-waited:
		return err
	case <-timer.C:
		return ErrCommandCleanup
	}
}
