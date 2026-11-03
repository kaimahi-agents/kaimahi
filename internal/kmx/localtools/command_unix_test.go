//go:build unix

package localtools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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

func TestCommandHostAcceptsCompleteRetainedOutputBudget(t *testing.T) {
	cfg := commandFixture(t, "boundary")
	host, err := NewCommandHost([]CommandConfig{cfg})
	if err != nil {
		t.Fatal(err)
	}
	result, err := executeCommandTool(t.Context(), host)
	if err != nil || len(result.Output["stdout"].(string)) != commandOutputLimit || result.Output["succeeded"] != true {
		t.Fatalf("boundary result: %+v %v", result, err)
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
	first, err := os.ReadFile(heartbeat)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	last, err := os.ReadFile(heartbeat)
	if err != nil || string(first) != string(last) {
		t.Fatal("descendant survived", err)
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
		_, _ = os.Stdout.WriteString(strings.Repeat("x", commandOutputLimit))
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
	for i := 0; ; i++ {
		_ = os.WriteFile(path, []byte(strings.Repeat("x", i+1)), 0600)
		time.Sleep(10 * time.Millisecond)
	}
}
