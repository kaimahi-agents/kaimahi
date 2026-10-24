//go:build unix

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestFilePromptOpenPipeInterrupt(t *testing.T) {
	const helper = "KMX_TEST_PROMPT_INTERRUPT"
	if os.Getenv(helper) == "1" {
		var out, diag bytes.Buffer
		deps, _ := testDependencies(&out, &diag)
		// Parent keeps this inherited (blocking) stdin pipe open without EOF.
		cmd := newAgentRunCommand(&commandState{deps: deps})
		cmd.SetContext(context.Background())
		if err := cmd.Flags().Set("prompt-file", "-"); err != nil {
			t.Fatal(err)
		}
		go func() {
			time.Sleep(100 * time.Millisecond)
			_ = syscall.Kill(os.Getpid(), syscall.SIGINT)
		}()
		_ = runFileCommand(cmd, &commandState{deps: deps}, "unread.agent.md", fileCommandOptions{Output: "json"}, "", "-")
		t.Fatal("open-pipe intake returned before interrupt")
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFilePromptOpenPipeInterrupt$")
	child.Env = append(os.Environ(), helper+"=1")
	child.Stdin = reader
	output, err := child.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGINT {
		t.Fatalf("Ctrl-C swallowed during open-pipe intake: %v / %s", err, output)
	}
	if len(output) != 0 {
		t.Fatalf("intake invoked preview or printed output: %s", output)
	}
}
