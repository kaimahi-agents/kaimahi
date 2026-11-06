//go:build windows

package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

type windowsHelperProcesses struct {
	Parent int `json:"parent"`
	Child  int `json:"child"`
}

func TestWindowsCommandOwnerDeathAfterCreateReapsChild(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	owner := exec.Command(os.Args[0], windowsHelperArgs("owner-window", pidFile)...)
	owner.Stdout, owner.Stderr = io.Discard, io.Discard
	if err := owner.Start(); err != nil {
		t.Fatalf("start owner: %v", err)
	}
	ownerDone := false
	t.Cleanup(func() {
		if !ownerDone {
			_ = owner.Process.Kill()
			_ = owner.Wait()
		}
	})

	childPID := readWindowsHelperPID(t, pidFile)
	child := openWindowsProcessForTermination(t, childPID)
	if err := owner.Process.Kill(); err != nil {
		t.Fatalf("kill owner: %v", err)
	}
	if err := owner.Wait(); err == nil {
		t.Fatal("killed owner exited successfully")
	}
	ownerDone = true
	waitWindowsProcess(t, "creation-window child", child)
}

func TestWindowsRunnerCancellationReapsDescendantTree(t *testing.T) {
	tests := []struct {
		name string
		run  func(*Runner, string, []string) error
	}{
		{name: "Run", run: func(r *Runner, name string, args []string) error {
			return r.Run(name, args...)
		}},
		{name: "RunStdin", run: func(r *Runner, name string, args []string) error {
			return r.RunStdin([]byte("input"), name, args...)
		}},
		{name: "Capture", run: func(r *Runner, name string, args []string) error {
			_, err := r.Capture(name, args...)
			return err
		}},
		{name: "Quiet", run: func(r *Runner, name string, args []string) error {
			succeeded := r.Quiet(name, args...)
			if succeeded {
				return errors.New("cancelled command succeeded")
			}
			if err := r.Context.Err(); err != nil {
				return err
			}
			return errors.New("command failed before cancellation")
		}},
		{name: "CommandStartWait", run: func(r *Runner, name string, args []string) error {
			cmd := r.Command(name, args...)
			if err := cmd.Start(); err != nil {
				return err
			}
			return cmd.Wait()
		}},
		{name: "CommandRun", run: func(r *Runner, name string, args []string) error {
			return r.Command(name, args...).Run()
		}},
		{name: "CommandOutput", run: func(r *Runner, name string, args []string) error {
			_, err := r.Command(name, args...).Output()
			return err
		}},
		{name: "CommandCombinedOutput", run: func(r *Runner, name string, args []string) error {
			_, err := r.Command(name, args...).CombinedOutput()
			return err
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			pidFile := filepath.Join(t.TempDir(), "processes.json")
			args := windowsHelperArgs("spawn", pidFile)
			done := make(chan error, 1)
			go func() {
				done <- test.run(&Runner{Context: ctx, Stdout: io.Discard, Stderr: io.Discard}, os.Args[0], args)
			}()

			processes := readWindowsHelperProcessesWhileRunning(t, pidFile, done)
			parent := openWindowsProcess(t, processes.Parent)
			child := openWindowsProcess(t, processes.Child)
			cancel()

			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancelled command error = %v, want context.Canceled", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("cancelled command did not return")
			}
			waitWindowsProcess(t, "parent", parent)
			waitWindowsProcess(t, "child", child)
		})
	}
}

func TestWindowsCommandNaturalParentExitReapsDescendantTree(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "processes.json")
	cmd := (&Runner{}).Command(os.Args[0], windowsHelperArgs("orphan", pidFile)...)
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	processes := readWindowsHelperProcesses(t, pidFile)
	child := openWindowsProcess(t, processes.Child)
	if err := cmd.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	waitWindowsProcess(t, "child", child)
}

func TestWindowsCommandDirectProcessKillReapsDescendantTree(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "processes.json")
	cmd := (&Runner{}).Command(os.Args[0], windowsHelperArgs("spawn", pidFile)...)
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	processes := readWindowsHelperProcesses(t, pidFile)
	child := openWindowsProcess(t, processes.Child)
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("Process.Kill: %v", err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("Wait after Process.Kill succeeded")
	}
	waitWindowsProcess(t, "child", child)
}

func TestWindowsCommandCloseReapsDescendantTree(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "processes.json")
	cmd := (&Runner{}).Command(os.Args[0], windowsHelperArgs("spawn", pidFile)...)
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	processes := readWindowsHelperProcesses(t, pidFile)
	parent := openWindowsProcess(t, processes.Parent)
	child := openWindowsProcess(t, processes.Child)
	if err := cmd.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("Wait after Close succeeded")
	}
	waitWindowsProcess(t, "parent", parent)
	waitWindowsProcess(t, "child", child)
}

func TestWindowsCommandConcurrentCloseAndWaitReapDescendantTree(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "processes.json")
	cmd := (&Runner{}).Command(os.Args[0], windowsHelperArgs("spawn", pidFile)...)
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	processes := readWindowsHelperProcesses(t, pidFile)
	parent := openWindowsProcess(t, processes.Parent)
	child := openWindowsProcess(t, processes.Child)
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	if err := cmd.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := <-waitDone; err == nil {
		t.Fatal("Wait after Close succeeded")
	}
	waitWindowsProcess(t, "parent", parent)
	waitWindowsProcess(t, "child", child)
}

func TestWindowsCommandStartWaitCompletes(t *testing.T) {
	cmd := (&Runner{}).Command(os.Args[0], windowsHelperArgs("exit")...)
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestWindowsCommandPreservesPreCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cmd := (&Runner{Context: ctx}).Command(os.Args[0], windowsHelperArgs("exit")...)
	if err := cmd.Run(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
}

func TestWindowsCommandPreservesDeadlineExceeded(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	<-ctx.Done()
	cmd := (&Runner{Context: ctx}).Command(os.Args[0], windowsHelperArgs("exit")...)
	if err := cmd.Run(); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run error = %v, want context.DeadlineExceeded", err)
	}
}

func TestWindowsCommandOutputMethodsUseOwnedLifecycle(t *testing.T) {
	if output, err := (&Runner{}).Command(os.Args[0], windowsHelperArgs("print")...).Output(); err != nil || string(output) != "out" {
		t.Fatalf("Output = %q, %v", output, err)
	}
	if output, err := (&Runner{}).Command(os.Args[0], windowsHelperArgs("print")...).CombinedOutput(); err != nil || string(output) != "outerr" {
		t.Fatalf("CombinedOutput = %q, %v", output, err)
	}
}

func TestWindowsCommandPipeUsesOwnedLifecycle(t *testing.T) {
	cmd := (&Runner{}).Command(os.Args[0], windowsHelperArgs("print")...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	output, err := io.ReadAll(stdout)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if string(output) != "out" {
		t.Fatalf("stdout = %q", output)
	}
}

func TestWindowsRunHelper(t *testing.T) {
	args := helperArguments(os.Args)
	if len(args) == 0 {
		return
	}
	switch args[0] {
	case "exit":
		return
	case "print":
		_, _ = os.Stdout.WriteString("out")
		_, _ = os.Stderr.WriteString("err")
		os.Exit(0)
	case "leaf":
		for {
			time.Sleep(time.Hour)
		}
	case "spawn":
		if len(args) != 2 {
			t.Fatalf("spawn arguments = %q", args)
		}
		child := exec.Command(os.Args[0], windowsHelperArgs("leaf")...)
		child.Stdout, child.Stderr = io.Discard, io.Discard
		if err := child.Start(); err != nil {
			t.Fatalf("start child: %v", err)
		}
		raw, err := json.Marshal(windowsHelperProcesses{Parent: os.Getpid(), Child: child.Process.Pid})
		if err != nil {
			t.Fatalf("marshal process IDs: %v", err)
		}
		if err := os.WriteFile(args[1], raw, 0600); err != nil {
			t.Fatalf("write process IDs: %v", err)
		}
		if err := child.Wait(); err != nil {
			t.Fatalf("wait child: %v", err)
		}
	case "orphan":
		if len(args) != 2 {
			t.Fatalf("orphan arguments = %q", args)
		}
		child := exec.Command(os.Args[0], windowsHelperArgs("leaf")...)
		child.Stdout, child.Stderr = io.Discard, io.Discard
		if err := child.Start(); err != nil {
			t.Fatalf("start child: %v", err)
		}
		raw, err := json.Marshal(windowsHelperProcesses{Parent: os.Getpid(), Child: child.Process.Pid})
		if err != nil {
			t.Fatalf("marshal process IDs: %v", err)
		}
		if err := os.WriteFile(args[1], raw, 0600); err != nil {
			t.Fatalf("write process IDs: %v", err)
		}
		if err := child.Process.Release(); err != nil {
			t.Fatalf("release child: %v", err)
		}
		return
	case "owner-window":
		if len(args) != 2 {
			t.Fatalf("owner-window arguments = %q", args)
		}
		cmd := (&Runner{}).Command(os.Args[0], windowsHelperArgs("leaf")...)
		cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
		cmd.platform.afterCreate = func(pid int) {
			if err := os.WriteFile(args[1], []byte(strconv.Itoa(pid)), 0600); err != nil {
				os.Exit(3)
			}
			select {}
		}
		if err := cmd.Start(); err != nil {
			t.Fatalf("start creation-window child: %v", err)
		}
		t.Fatal("creation-window hook returned")
	default:
		t.Fatalf("unknown helper mode %q", args[0])
	}
}

func windowsHelperArgs(args ...string) []string {
	return append([]string{"-test.run=^TestWindowsRunHelper$", "--"}, args...)
}

func helperArguments(args []string) []string {
	for i, arg := range args {
		if arg == "--" {
			return args[i+1:]
		}
	}
	return nil
}

func readWindowsHelperProcesses(t *testing.T, path string) windowsHelperProcesses {
	return readWindowsHelperProcessesWhileRunning(t, path, nil)
}

func readWindowsHelperProcessesWhileRunning(t *testing.T, path string, done <-chan error) windowsHelperProcesses {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(path)
		if err == nil {
			var processes windowsHelperProcesses
			if json.Unmarshal(raw, &processes) == nil && processes.Parent > 0 && processes.Child > 0 {
				return processes
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read helper process IDs: %v", err)
		}
		if done != nil {
			select {
			case err := <-done:
				t.Fatalf("helper exited before publishing process IDs: %v", err)
			default:
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("helper did not publish process IDs at %s", path)
	return windowsHelperProcesses{}
}

func readWindowsHelperPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(string(raw))
			if parseErr == nil && pid > 0 {
				return pid
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read helper process ID: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("helper did not publish process ID at %s", path)
	return 0
}

func openWindowsProcess(t *testing.T, pid int) windows.Handle {
	t.Helper()
	if pid <= 0 || uint64(pid) > uint64(^uint32(0)) {
		t.Fatalf("invalid process ID %d", pid)
	}
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		t.Fatalf("open process %d: %v", pid, err)
	}
	t.Cleanup(func() {
		if err := windows.CloseHandle(handle); err != nil {
			t.Errorf("close process %d handle: %v", pid, err)
		}
	})
	return handle
}

func openWindowsProcessForTermination(t *testing.T, pid int) windows.Handle {
	t.Helper()
	if pid <= 0 || uint64(pid) > uint64(^uint32(0)) {
		t.Fatalf("invalid process ID %d", pid)
	}
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		t.Fatalf("open process %d: %v", pid, err)
	}
	t.Cleanup(func() {
		if event, _ := windows.WaitForSingleObject(handle, 0); event != windows.WAIT_OBJECT_0 {
			_ = windows.TerminateProcess(handle, 1)
		}
		if err := windows.CloseHandle(handle); err != nil {
			t.Errorf("close process %d handle: %v", pid, err)
		}
	})
	return handle
}

func waitWindowsProcess(t *testing.T, name string, handle windows.Handle) {
	t.Helper()
	event, err := windows.WaitForSingleObject(handle, 5_000)
	if err != nil {
		t.Fatalf("wait for %s process: %v", name, err)
	}
	if event != windows.WAIT_OBJECT_0 {
		t.Fatalf("%s process remained active: wait status %d", name, event)
	}
}

func TestWindowsHelperArguments(t *testing.T) {
	got := helperArguments([]string{"test.exe", "-test.run=x", "--", "spawn", "file"})
	if fmt.Sprint(got) != "[spawn file]" {
		t.Fatalf("helper arguments = %q", got)
	}
}
