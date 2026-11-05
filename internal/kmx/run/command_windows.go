//go:build windows

package run

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const windowsTreeWait = 5 * time.Second

type platformCommand struct {
	mu          sync.Mutex
	ready       chan struct{}
	readyOnce   sync.Once
	job         windows.Handle
	monitorDone chan struct{}
	setupErr    error
	cleanupErr  error
	rootExited  bool
	terminated  bool
	cancelled   bool
	closed      bool
	resume      bool
}

func (c *Command) initPlatform() {
	c.platform.ready = make(chan struct{})
	// startPlatform installs tree-aware cancellation after process setup.
	// Until then cancellation is intentionally inert; a context that wins
	// before setup completes is checked and cleaned up synchronously.
	c.Cancel = nil
}

func (c *Command) startPlatform() error {
	c.configureWindowsCommand()

	job, err := createWindowsJob()
	if err != nil {
		err = fmt.Errorf("create subprocess job: %w", err)
		c.platform.finishSetup(err)
		return err
	}
	c.platform.mu.Lock()
	c.platform.job = job
	c.platform.mu.Unlock()

	if err := c.Cmd.Start(); err != nil {
		c.platform.finishSetup(err)
		return joinPlatformError(err, c.platform.release())
	}

	assigned := false
	var root windows.Handle
	var assignErr, duplicateErr error
	handleErr := c.Process.WithHandle(func(raw uintptr) {
		process := windows.Handle(raw)
		assignErr = windows.AssignProcessToJobObject(job, process)
		if assignErr != nil {
			return
		}
		assigned = true
		duplicateErr = windows.DuplicateHandle(
			windows.CurrentProcess(), process,
			windows.CurrentProcess(), &root,
			windows.SYNCHRONIZE, false, 0,
		)
	})
	if handleErr != nil {
		return c.failWindowsStart(fmt.Errorf("access subprocess handle: %w", handleErr), assigned, root)
	}
	if assignErr != nil {
		return c.failWindowsStart(fmt.Errorf("assign subprocess to job: %w", assignErr), assigned, root)
	}
	if duplicateErr != nil {
		return c.failWindowsStart(fmt.Errorf("retain subprocess handle: %w", duplicateErr), assigned, root)
	}

	done := make(chan struct{})
	c.platform.mu.Lock()
	c.platform.monitorDone = done
	c.platform.mu.Unlock()
	go c.platform.monitorRoot(root, done)

	if err := c.ctx.Err(); err != nil {
		return c.failWindowsStart(err, true, 0)
	}
	if c.platform.resume {
		if err := resumeInitialThread(c.Process.Pid); err != nil {
			return c.failWindowsStart(fmt.Errorf("resume subprocess: %w", err), true, 0)
		}
	}
	c.platform.finishSetup(nil)
	return nil
}

func (c *Command) configureWindowsCommand() {
	if c.SysProcAttr == nil {
		c.SysProcAttr = &syscall.SysProcAttr{}
	} else {
		attributes := *c.SysProcAttr
		c.SysProcAttr = &attributes
	}
	// os/exec does not expose PROC_THREAD_ATTRIBUTE_JOB_LIST. Starting the
	// primary thread suspended is therefore the narrow documented route that
	// prevents the child from spawning an unowned descendant before assignment.
	if c.SysProcAttr.CreationFlags&windows.CREATE_SUSPENDED == 0 {
		c.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
		c.platform.resume = true
	}
	c.Cancel = c.cancelWindows
}

func commandContextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func createWindowsJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)),
		uint32(unsafe.Sizeof(limits)),
	); err != nil {
		_ = windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
}

func resumeInitialThread(pid int) (returnErr error) {
	if pid <= 0 || uint64(pid) > uint64(^uint32(0)) {
		return fmt.Errorf("invalid process ID %d", pid)
	}
	processID := uint32(pid)
	// CREATE_SUSPENDED creates one suspended primary thread, but os/exec closes
	// its thread handle before Start returns. Enumerate while the child cannot
	// run and fail closed if an external process with sufficient rights has made
	// that documented one-thread assumption ambiguous. No child-controlled code
	// can run in the assignment-to-enumeration interval.
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return fmt.Errorf("snapshot initial thread: %w", err)
	}
	defer func() {
		if err := windows.CloseHandle(snapshot); err != nil {
			returnErr = joinPlatformError(returnErr, fmt.Errorf("close thread snapshot: %w", err))
		}
	}()

	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	if err := windows.Thread32First(snapshot, &entry); err != nil {
		return fmt.Errorf("read initial thread snapshot: %w", err)
	}
	var threadID uint32
	for {
		if entry.OwnerProcessID == processID {
			if threadID != 0 {
				return fmt.Errorf("multiple initial threads found for process %d", pid)
			}
			threadID = entry.ThreadID
		}
		err = windows.Thread32Next(snapshot, &entry)
		if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
			break
		}
		if err != nil {
			return fmt.Errorf("read initial thread snapshot: %w", err)
		}
	}
	if threadID == 0 {
		return fmt.Errorf("initial thread not found for process %d", pid)
	}

	thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, threadID)
	if err != nil {
		return fmt.Errorf("open initial thread: %w", err)
	}
	defer func() {
		if err := windows.CloseHandle(thread); err != nil {
			returnErr = joinPlatformError(returnErr, fmt.Errorf("close initial thread: %w", err))
		}
	}()
	previous, err := windows.ResumeThread(thread)
	if err != nil {
		return err
	}
	if previous != 1 {
		return fmt.Errorf("initial thread had unexpected suspend count %d", previous)
	}
	return nil
}

func (c *Command) failWindowsStart(primary error, assigned bool, root windows.Handle) error {
	var cleanupErr error
	c.platform.mu.Lock()
	monitorOwnsRoot := c.platform.monitorDone != nil
	monitorDone := c.platform.monitorDone
	c.platform.mu.Unlock()
	if root != 0 {
		cleanupErr = errors.Join(cleanupErr, windows.CloseHandle(root))
	}
	if assigned {
		cleanupErr = errors.Join(cleanupErr, c.platform.terminate())
	} else if c.Process != nil {
		if err := c.Process.Kill(); !errors.Is(err, os.ErrProcessDone) {
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	c.platform.finishSetup(primary)
	if err := c.Cmd.Wait(); err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) && !errors.Is(err, os.ErrProcessDone) {
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	if monitorOwnsRoot {
		// The monitor closes its duplicated process handle after Wait observes
		// termination. Do not close the job until that monitor has stopped.
		<-monitorDone
	}
	cleanupErr = errors.Join(cleanupErr, c.platform.release())
	return joinPlatformError(primary, cleanupErr)
}

func (p *platformCommand) finishSetup(err error) {
	p.mu.Lock()
	p.setupErr = err
	p.mu.Unlock()
	p.readyOnce.Do(func() { close(p.ready) })
}

func (p *platformCommand) monitorRoot(root windows.Handle, done chan struct{}) {
	defer close(done)
	event, waitErr := windows.WaitForSingleObject(root, windows.INFINITE)
	closeErr := windows.CloseHandle(root)

	p.mu.Lock()
	if waitErr != nil {
		p.cleanupErr = errors.Join(p.cleanupErr, fmt.Errorf("wait for subprocess: %w", waitErr))
	} else if event != windows.WAIT_OBJECT_0 {
		p.cleanupErr = errors.Join(p.cleanupErr, fmt.Errorf("wait for subprocess returned status %d", event))
	} else {
		p.rootExited = true
	}
	if closeErr != nil {
		p.cleanupErr = errors.Join(p.cleanupErr, fmt.Errorf("close retained subprocess handle: %w", closeErr))
	}
	var terminateErr error
	if !p.closed {
		terminateErr = p.startTerminationLocked()
	}
	job := p.job
	p.mu.Unlock()
	if terminateErr == nil && job != 0 {
		terminateErr = waitForEmptyJob(job)
	}
	if terminateErr != nil {
		p.mu.Lock()
		p.cleanupErr = errors.Join(p.cleanupErr, terminateErr)
		p.mu.Unlock()
	}
}

func (c *Command) cancelWindows() error {
	<-c.platform.ready
	c.platform.mu.Lock()
	if c.platform.closed || c.platform.rootExited {
		c.platform.mu.Unlock()
		return os.ErrProcessDone
	}
	if c.platform.setupErr != nil {
		c.platform.mu.Unlock()
		if c.Process == nil {
			return os.ErrProcessDone
		}
		return c.Process.Kill()
	}
	c.platform.cancelled = true
	c.platform.mu.Unlock()
	if err := c.platform.terminate(); err != nil {
		return err
	}
	return c.platform.waitForMonitor()
}

func (p *platformCommand) terminate() error {
	p.mu.Lock()
	if p.closed || p.job == 0 {
		p.mu.Unlock()
		return nil
	}
	if err := p.startTerminationLocked(); err != nil {
		p.mu.Unlock()
		return err
	}
	p.mu.Unlock()
	return nil
}

func (p *platformCommand) startTerminationLocked() error {
	if p.closed || p.job == 0 {
		return nil
	}
	if !p.terminated {
		if err := windows.TerminateJobObject(p.job, 1); err != nil {
			return fmt.Errorf("terminate subprocess job: %w", err)
		}
		p.terminated = true
	}
	return nil
}

func waitForEmptyJob(job windows.Handle) error {
	deadline := time.Now().Add(windowsTreeWait)
	for {
		active, err := activeJobProcesses(job)
		if err != nil {
			return fmt.Errorf("inspect subprocess job: %w", err)
		}
		if active == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("subprocess job still has %d active processes after termination", active)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type jobAccounting struct {
	totalUserTime             int64
	totalKernelTime           int64
	thisPeriodTotalUserTime   int64
	thisPeriodTotalKernelTime int64
	totalPageFaultCount       uint32
	totalProcesses            uint32
	activeProcesses           uint32
	totalTerminatedProcesses  uint32
}

func activeJobProcesses(job windows.Handle) (uint32, error) {
	var accounting jobAccounting
	err := windows.QueryInformationJobObject(
		job,
		windows.JobObjectBasicAccountingInformation,
		uintptr(unsafe.Pointer(&accounting)),
		uint32(unsafe.Sizeof(accounting)),
		nil,
	)
	return accounting.activeProcesses, err
}

func (c *Command) waitPlatform(waitErr error) error {
	cleanupErr := c.platform.release()
	c.platform.mu.Lock()
	cancelled := c.platform.cancelled
	c.platform.mu.Unlock()
	if cancelled && c.ctx.Err() != nil {
		return joinPlatformError(c.ctx.Err(), cleanupErr)
	}
	return joinPlatformError(waitErr, cleanupErr)
}

func (c *Command) closePlatform() error {
	if !c.startCalled.Load() {
		return nil
	}
	<-c.platform.ready
	if err := c.platform.terminate(); err != nil {
		return err
	}
	return c.platform.waitForMonitor()
}

func (p *platformCommand) release() error {
	_ = p.waitForMonitor()

	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.closed && p.job != 0 {
		if err := windows.CloseHandle(p.job); err != nil {
			p.cleanupErr = errors.Join(p.cleanupErr, fmt.Errorf("close subprocess job: %w", err))
		}
		p.job = 0
		p.closed = true
	}
	return p.cleanupErr
}

func (p *platformCommand) waitForMonitor() error {
	p.mu.Lock()
	done := p.monitorDone
	p.mu.Unlock()
	if done != nil {
		<-done
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cleanupErr
}

func joinPlatformError(primary, cleanup error) error {
	if primary == nil {
		return cleanup
	}
	if cleanup == nil {
		return primary
	}
	return errors.Join(primary, cleanup)
}
