//go:build linux

package app

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"golang.org/x/sys/unix"
)

func TestCreateWizardPTYCancellationRestoresTerminal(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	for _, tc := range []struct {
		name, keys       string
		missingNamespace bool
	}{
		{"description ctrl-c", "\x03", false},
		{"namespace ctrl-c", "\x03", true},
		{"namespace escape", "\x1b", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			master, slave := chatPTY(t, 100)
			before, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
			if err != nil {
				t.Fatal(err)
			}
			opt := CreateOptions{Out: filepath.Join(t.TempDir(), "never.yaml")}
			if tc.missingNamespace {
				opt.Name, opt.Description = "demo", "Demo"
			}
			var stdout bytes.Buffer
			a := &App{Cfg: &config.Config{}, Stdin: slave, Out: &stdout, Err: slave}
			done := make(chan error, 1)
			go func() { done <- a.CreateAgentInteractive(opt) }()
			var captured strings.Builder
			label := "Description"
			if tc.missingNamespace {
				label = "Namespace the Orka controller watches"
			}
			chatPTYReadUntil(t, master, &captured, func(s string) bool { return strings.Contains(s, label) })
			if _, err := io.WriteString(master, tc.keys); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("cancellation returned an error: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("wizard did not cancel")
			}
			after, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
			if err != nil || *before != *after {
				t.Fatalf("wizard did not restore terminal: %v", err)
			}
			if stdout.Len() != 0 {
				t.Fatalf("wizard wrote to stdout: %q", stdout.String())
			}
			if _, err := os.Stat(opt.Out); !os.IsNotExist(err) {
				t.Fatalf("cancellation wrote a manifest: %v", err)
			}
		})
	}
}

func TestCreateWizardPTYNativeCompletionRestoresTerminal(t *testing.T) {
	for _, tc := range []struct {
		name, term string
		invalid    bool
	}{
		{"bubbles", "xterm-256color", false},
		{"plain fallback", "dumb", false},
		{"invalid rate", "xterm-256color", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TERM", tc.term)
			t.Setenv("PATH", t.TempDir())
			master, slave := chatPTY(t, 160)
			before, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
			if err != nil {
				t.Fatal(err)
			}
			var stdout bytes.Buffer
			a := &App{Stdin: slave, Out: &stdout, Err: slave}
			opt := nativeWizardOptions()
			opt.Name, opt.Secret, opt.Out = "", "", "-"
			opt.Task = "Say hello"
			if tc.invalid {
				opt.AgentRequestsPerMinute = "0"
			}
			done := make(chan error, 1)
			go func() { done <- a.CreateAgentInteractive(opt) }()
			var captured strings.Builder
			chatPTYReadUntil(t, master, &captured, func(s string) bool { return strings.Contains(s, "Agent name") })
			if _, err := io.WriteString(master, "\r"); err != nil {
				t.Fatal(err)
			}
			chatPTYReadUntil(t, master, &captured, func(s string) bool { return strings.Contains(s, "Existing Provider Secret name") })
			if _, err := io.WriteString(master, "model-key\r"); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if tc.invalid {
					if err == nil || !strings.Contains(err.Error(), "positive int32") {
						t.Fatalf("final validation error lost: %v", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("wizard did not finish")
			}
			after, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
			if err != nil || *before != *after {
				t.Fatalf("wizard did not restore terminal: %v", err)
			}
			if tc.invalid {
				if stdout.Len() != 0 {
					t.Fatal("invalid completed options emitted a manifest")
				}
				return
			}
			if !strings.Contains(stdout.String(), "core.orka.ai/v1alpha1") || !strings.Contains(stdout.String(), "kind: Task") || strings.Contains(stdout.String(), "kagent.dev") {
				t.Fatal("wrong manifest on stdout")
			}
			if strings.Contains(captured.String(), "ServiceAccount") {
				t.Fatal("offline Task asked for result account")
			}
		})
	}
}

func TestCreateWizardSignalHelper(t *testing.T) {
	if os.Getenv("KMX_WIZARD_SIGNAL_HELPER") != "1" {
		return
	}
	a := &App{Stdin: os.Stdin, Out: os.Stdout, Err: os.Stderr}
	if err := a.CreateAgentInteractive(CreateOptions{Name: "demo", Description: "Demo"}); err != nil {
		os.Exit(3)
	}
	os.Exit(0)
}

func TestCreateWizardPTYSIGTERMRestoresTerminal(t *testing.T) {
	for _, signal := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(signal.String(), func(t *testing.T) {
			master, slave := chatPTY(t, 100)
			before, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
			if err != nil {
				t.Fatal(err)
			}
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, "-test.run=^TestCreateWizardSignalHelper$")
			cmd.Env = append(os.Environ(), "TERM=xterm-256color", "KMX_WIZARD_SIGNAL_HELPER=1")
			cmd.Stdin, cmd.Stderr = slave, slave
			var stdout bytes.Buffer
			cmd.Stdout = &stdout
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = cmd.Process.Kill() }()
			var captured strings.Builder
			chatPTYReadUntil(t, master, &captured, func(s string) bool { return strings.Contains(s, "Namespace the Orka controller watches") })
			if err := cmd.Process.Signal(signal); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Wait(); err != nil {
				t.Fatalf("signal bypassed cancellation cleanup: %v", err)
			}
			after, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
			if err != nil || *before != *after {
				t.Fatalf("signal did not restore terminal: %v", err)
			}
			if stdout.Len() != 0 {
				t.Fatal("signal wrote to stdout")
			}
		})
	}
}
