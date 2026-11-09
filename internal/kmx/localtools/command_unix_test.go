//go:build unix

package localtools

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
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
)

func commandFixture(t *testing.T, mode string, extra ...string) CommandConfig {
	t.Helper()
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"-test.run=^TestCommandHelperProcess$", "--", mode}
	args = append(args, extra...)
	return CommandConfig{Name: "check", Command: program, Args: args, Dir: t.TempDir(), Env: map[string]string{"GO_WANT_COMMAND_HELPER": "enabled"}}
}

func executeCommandTool(ctx context.Context, host *Host) (api.ToolResult, error) {
	return host.Execute(ctx, controller.ToolCallContext{SessionUID: "session"}, api.ToolCall{ID: "call", Tool: "check", Args: map[string]any{}, IdempotencyKey: "once", Mediation: api.MediationControllerMediated})
}

func commandPlanFixture(t *testing.T, cfg CommandConfig) CommandPlan {
	t.Helper()
	commandInfo, err := os.Stat(cfg.Command)
	if err != nil {
		t.Fatal(err)
	}
	dirInfo, err := os.Stat(cfg.Dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ExecutableID, _ = commandFileIdentity(commandInfo)
	cfg.DirectoryID, _ = commandFileIdentity(dirInfo)
	cfg.ExecutableDigest, err = commandExecutableDigest(cfg.Command)
	if err != nil {
		t.Fatal(err)
	}
	return CommandPlan{ConfigRevision: "sha256:" + strings.Repeat("a", 64), Revision: "sha256:" + strings.Repeat("b", 64), Commands: []CommandConfig{cfg}}
}

func residentCommandFixture(t *testing.T, mode string) CommandConfig {
	t.Helper()
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "command")
	source := "#!/bin/sh\nGO_WANT_COMMAND_HELPER=enabled exec " + shellCommandQuote(program) + " \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(source), 0700); err != nil {
		t.Fatal(err)
	}
	return CommandConfig{Name: "check", Command: wrapper, Args: []string{"-test.run=^TestCommandHelperProcess$", "--", mode}, Dir: dir, Env: map[string]string{}}
}

func shellCommandQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func TestCommandHostFixedEnvironmentAndResults(t *testing.T) {
	t.Setenv("COMMAND_AMBIENT_CANARY", "must-not-inherit")
	for _, mode := range []string{"success", "failure"} {
		t.Run(mode, func(t *testing.T) {
			cfg := commandFixture(t, mode)
			cfg.Env["COMMAND_EXPLICIT"] = "present"
			host, err := NewCommandHost([]CommandConfig{cfg})
			if err != nil {
				t.Fatal(err)
			}
			cfg.Args[0] = "changed"
			cfg.Env["COMMAND_EXPLICIT"] = "changed"
			result, err := executeCommandTool(t.Context(), host)
			if err != nil {
				t.Fatal(err)
			}
			wantExit := 0
			if mode == "failure" {
				wantExit = 7
			}
			if fmt.Sprint(result.Output["exitCode"]) != fmt.Sprint(wantExit) || result.Output["succeeded"] != (wantExit == 0) || result.Output["stdout"] != "stdout\n" || result.Output["stderr"] != "stderr\n" {
				t.Fatalf("result=%+v", result)
			}
		})
	}
}

func TestCommandHostConstrainedArgumentsCannotEscapeArgv(t *testing.T) {
	cfg := commandFixture(t, "arguments")
	cfg.Args = nil
	cfg.ArgumentPlan = []CommandArgument{{Literal: "-test.run=^TestCommandHelperProcess$"}, {Literal: "--"}, {Literal: "arguments"}, {Parameter: "target"}, {Parameter: "mode"}, {Parameter: "count"}}
	minimum, maximum := int64(1), int64(3)
	cfg.Parameters = map[string]CommandParameter{
		"target": {Type: "path", Description: "Relative target", MaxLength: 64},
		"mode":   {Type: "string", Description: "Mode", Pattern: "^[a-z]{1,8}$", MaxLength: 8},
		"count":  {Type: "integer", Description: "Count", Minimum: &minimum, Maximum: &maximum},
	}
	host, err := NewCommandHost([]CommandConfig{cfg})
	if err != nil {
		t.Fatal(err)
	}
	call := func(args map[string]any) (api.ToolResult, error) {
		return host.Execute(t.Context(), controller.ToolCallContext{SessionUID: "session"}, api.ToolCall{ID: "call", Tool: "check", Args: args, IdempotencyKey: "once", Mediation: api.MediationControllerMediated})
	}
	result, err := call(map[string]any{"target": "sub/file.txt", "mode": "read", "count": float64(2)})
	if err != nil || result.Output["stdout"] != "sub/file.txt|read|2\n" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	for _, args := range []map[string]any{
		{"target": "../secret", "mode": "read", "count": float64(2)},
		{"target": "sub/file", "mode": "-flag", "count": float64(2)},
		{"target": "sub/file", "mode": "read;rm", "count": float64(2)},
		{"target": "sub/file", "mode": "read", "count": float64(4)},
		{"target": "sub/file", "mode": "read", "count": float64(2), "extra": "x"},
	} {
		if result, err := call(args); err == nil || result.Output != nil {
			t.Fatalf("escaped constrained args accepted: %+v result=%+v err=%v", args, result, err)
		}
	}
}

func TestCommandActivationChecksVersionAndStatus(t *testing.T) {
	cfg := residentCommandFixture(t, "success")
	cfg.Checks = []CommandCheck{
		{Name: "compatibility", Args: []string{"-test.run=^TestCommandHelperProcess$", "--", "version"}, TimeoutMilliseconds: 5000, ExitCode: 0, StdoutPattern: `^tool version 1\.[0-9]+\.[0-9]+$`},
		{Name: "readiness", Args: []string{"-test.run=^TestCommandHelperProcess$", "--", "ready"}, TimeoutMilliseconds: 5000, ExitCode: 0, StdoutPattern: `^ready$`},
	}
	plan := commandPlanFixture(t, cfg)
	if err := RunCommandChecks(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	plan.Commands[0].Checks[1].StdoutPattern = `^not-ready$`
	if err := RunCommandChecks(t.Context(), plan); err == nil {
		t.Fatal("mismatched readiness output passed")
	}
}

func TestCommandHostAcceptsCompleteRetainedOutputBudget(t *testing.T) {
	for _, value := range []string{"x", "<", ">", "&", "\t", "\n", "\"", "\\"} {
		t.Run(strconv.Quote(value), func(t *testing.T) {
			cfg := commandFixture(t, "boundary", value)
			host, err := NewCommandHost([]CommandConfig{cfg})
			if err != nil {
				t.Fatal(err)
			}
			result, err := host.Execute(t.Context(), controller.ToolCallContext{SessionUID: "session"}, api.ToolCall{ID: strings.Repeat("c", 64), Tool: "check", Args: map[string]any{}, IdempotencyKey: "once", Mediation: api.MediationControllerMediated})
			if err != nil {
				t.Fatal(err)
			}
			want := strings.Repeat(value, commandOutputLimit/2)
			raw, err := json.Marshal(result)
			if err != nil || len(raw) > 32*1024 || result.Output["stdout"] != want || result.Output["stderr"] != want || result.Output["succeeded"] != true {
				t.Fatalf("boundary result: bytes=%d err=%v", len(raw), err)
			}
		})
	}
}

func TestCommandReapingTimeout(t *testing.T) {
	// A stuck kernel wait cannot be induced reliably. Hold the sole waiter until
	// the cleanup budget expires, then release it to verify eventual reaping.
	release, finished := make(chan struct{}), make(chan struct{})
	defer func() {
		close(release)
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Error("waiter did not finish after release")
		}
	}()
	started := time.Now()
	err := waitCommandExit(func() error {
		<-release
		close(finished)
		return nil
	})
	if !errors.Is(err, ErrCommandCleanup) || time.Since(started) > 3*time.Second {
		t.Fatalf("unbounded or accepted incomplete reaping: %v", err)
	}
	select {
	case <-finished:
		t.Fatal("waiter unexpectedly completed")
	default:
	}
}

func TestCommandHostWithholdsUnsafeIncompleteAndOversizedOutput(t *testing.T) {
	for _, mode := range []string{"secret", "invalid", "oversized", "sleep"} {
		t.Run(mode, func(t *testing.T) {
			cfg := commandFixture(t, mode)
			cfg.Env["COMMAND_SECRET"] = "private-command-canary"
			cfg.CredentialEnv = []string{"COMMAND_SECRET"}
			host, err := NewCommandHost([]CommandConfig{cfg})
			if err != nil {
				t.Fatal(err)
			}
			ctx := t.Context()
			if mode == "sleep" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 50*time.Millisecond)
				defer cancel()
			}
			result, err := executeCommandTool(ctx, host)
			if err == nil || result.Output != nil || strings.Contains(err.Error(), "private-command-canary") {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if mode == "sleep" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("deadline lost: %v", err)
			}
		})
	}
}

func TestCommandHostCancellationKillsDescendants(t *testing.T) {
	heartbeat := filepath.Join(t.TempDir(), "heartbeat")
	cfg := commandFixture(t, "descendant", heartbeat)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	host, err := NewCommandHost([]CommandConfig{cfg})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := executeCommandTool(ctx, host); done <- err }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(heartbeat); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("descendant did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	raw, err := os.ReadFile(heartbeat + ".pid")
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(raw))
	if err != nil || pid <= 1 {
		t.Fatalf("invalid descendant pid: %q", raw)
	}
	// An orphan zombie may remain under a container init that does not reap.
	// Require kernel-observed absence or zombie state, never heartbeat silence.
	probeCtx, stopProbe := context.WithTimeout(t.Context(), 2*time.Second)
	defer stopProbe()
	for {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			break
		} else if err != nil {
			t.Fatalf("probe descendant: %v", err)
		}
		state, err := exec.CommandContext(probeCtx, "ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
		if err == nil && strings.HasPrefix(strings.TrimSpace(string(state)), "Z") {
			break
		}
		if probeCtx.Err() != nil {
			t.Fatalf("descendant still running: pid=%d state=%q err=%v", pid, state, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCommandHostRefusesChangedExecutableIdentity(t *testing.T) {
	cfg := commandFixture(t, "success")
	source, err := os.Open(cfg.Command)
	if err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(cfg.Dir, "command-helper")
	target, err := os.OpenFile(program, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0700)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(target, source); err != nil {
		t.Fatal("copy executable", err)
	}
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	cfg.Command = program
	host, err := NewCommandHost([]CommandConfig{cfg})
	if err != nil {
		t.Fatal(err)
	}
	old := program + ".old"
	if err := os.Rename(program, old); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(program, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if result, err := executeCommandTool(t.Context(), host); err == nil || result.Output != nil {
		t.Fatalf("changed executable accepted: %+v %v", result, err)
	}
}

func TestCommandHostCleansUpAfterPostStartIdentityChange(t *testing.T) {
	cfg := commandFixture(t, "sleep")
	source, err := os.Open(cfg.Command)
	if err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(cfg.Dir, "command-helper")
	target, err := os.OpenFile(program, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0700)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(target, source); err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(source.Close(), target.Close()); err != nil {
		t.Fatal(err)
	}
	cfg.Command = program
	cfg.commandInfo, err = os.Stat(cfg.Command)
	if err != nil {
		t.Fatal(err)
	}
	cfg.dirInfo, err = os.Stat(cfg.Dir)
	if err != nil {
		t.Fatal(err)
	}
	pid := 0
	afterStart := func(started int) error {
		pid = started
		if err := os.Rename(program, program+".old"); err != nil {
			return err
		}
		if err := os.WriteFile(program, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
			return err
		}
		return nil
	}
	check, err := validateCommandConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	result, err := runCommandAfterStart(t.Context(), cfg, check, afterStart)
	if err == nil || result != nil || pid <= 1 || time.Since(started) > 3*time.Second {
		t.Fatalf("post-start identity change accepted or cleanup unbounded: pid=%d result=%+v err=%v", pid, result, err)
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("started command not reaped: pid=%d err=%v", pid, err)
	}
}

func TestCommandHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_COMMAND_HELPER") != "enabled" {
		return
	}
	os.Unsetenv("GO_WANT_COMMAND_HELPER")
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) < 2 {
		os.Exit(90)
	}
	switch args[1] {
	case "success", "failure":
		if os.Getenv("COMMAND_AMBIENT_CANARY") != "" || os.Getenv("COMMAND_EXPLICIT") != "present" {
			os.Exit(91)
		}
		_, _ = os.Stdout.WriteString("stdout\n")
		_, _ = os.Stderr.WriteString("stderr\n")
		if args[1] == "failure" {
			os.Exit(7)
		}
	case "secret":
		_, _ = os.Stdout.WriteString(os.Getenv("COMMAND_SECRET"))
	case "invalid":
		_, _ = os.Stdout.Write([]byte{0xff})
	case "oversized":
		_, _ = os.Stdout.WriteString(strings.Repeat("x", commandOutputLimit+1))
	case "boundary":
		if len(args) != 3 {
			os.Exit(92)
		}
		_, _ = os.Stdout.WriteString(strings.Repeat(args[2], commandOutputLimit/2))
		_, _ = os.Stderr.WriteString(strings.Repeat(args[2], commandOutputLimit/2))
	case "arguments":
		if len(args) != 5 {
			os.Exit(92)
		}
		_, _ = fmt.Fprintf(os.Stdout, "%s|%s|%s\n", args[2], args[3], args[4])
	case "version":
		_, _ = os.Stdout.WriteString("tool version 1.2.3\n")
	case "ready":
		_, _ = os.Stdout.WriteString("ready\n")
	case "sleep":
		_, _ = os.Stdout.WriteString("partial")
		time.Sleep(time.Minute)
	case "descendant":
		if len(args) != 3 {
			os.Exit(92)
		}
		cmd := os.Args[0]
		process, err := os.StartProcess(cmd, []string{cmd, "-test.run=^TestCommandHeartbeatProcess$", "--", args[2]}, &os.ProcAttr{Env: append(os.Environ(), "GO_WANT_COMMAND_HEARTBEAT=1"), Files: []*os.File{os.Stdin, os.Stdout, os.Stderr}})
		if err != nil {
			os.Exit(93)
		}
		_ = process.Release()
		time.Sleep(time.Minute)
	default:
		os.Exit(94)
	}
	os.Exit(0)
}

func TestCommandHeartbeatProcess(t *testing.T) {
	if os.Getenv("GO_WANT_COMMAND_HEARTBEAT") != "1" {
		return
	}
	path := os.Args[len(os.Args)-1]
	if err := os.WriteFile(path+".pid", []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		os.Exit(95)
	}
	for i := 0; ; i++ {
		_ = os.WriteFile(path, []byte(strings.Repeat("x", i+1)), 0600)
		time.Sleep(10 * time.Millisecond)
	}
}
