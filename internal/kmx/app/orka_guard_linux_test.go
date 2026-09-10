package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

func TestOrkaGuardPromptHelper(t *testing.T) {
	mode := os.Getenv("KMX_ORKA_GUARD_HELPER")
	if mode == "" {
		return
	}
	a := &App{Cfg: &config.Config{KubeContext: "kind-test", ContextSource: config.SourceFlag}, Run: &run.Runner{}, Out: io.Discard, Err: os.Stdout, Stdin: os.Stdin}
	opt := CreateOptions{Name: "sample", Namespace: "orka-system", ProviderType: "openai", Model: "local", Secret: "model-key", Out: filepath.Join(os.Getenv("KMX_ORKA_TEST_DIR"), "bundle.yaml")}
	// Observe the actual blocked read, not just a printed prompt followed by
	// cancellation before the reader starts. The observer exits before return.
	reading := make(chan struct{})
	go func() {
		defer close(reading)
		for {
			stack := make([]byte, 1<<20)
			stack = stack[:runtime.Stack(stack, true)]
			if bytes.Contains(stack, []byte("/guard.readLine(")) {
				fmt.Println("guard-reading")
				return
			}
			select {
			case <-t.Context().Done():
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}()
	var err error
	switch mode {
	case "deadline":
		bundle, buildErr := createOrkaBundle(opt)
		if buildErr != nil {
			t.Fatal(buildErr)
		}
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		err = a.createOrkaOnline(ctx, opt, bundle)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("deadline refusal = %v", err)
		}
	case "confirm":
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		if err := a.guardOrkaCreate(ctx, opt); err != nil || !a.guarded {
			t.Fatalf("confirmation failed: %v", err)
		}
	default:
		// Exercise the public entry point's actual SIGINT/SIGTERM handling.
		err = a.CreateAgent(opt)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("signal refusal = %v", err)
		}
	}
	<-reading
	if mode != "confirm" && a.guarded {
		t.Fatal("cancelled confirmation marked the guard satisfied")
	}
	stack := make([]byte, 1<<20)
	stack = stack[:runtime.Stack(stack, true)]
	if bytes.Contains(stack, []byte("/guard.readLine")) {
		t.Fatal("guard reader or cancellation watcher still running")
	}
	fmt.Println("guard-returned")
	// A cancelled guard must leave stdin open and must not steal later input.
	const next = "next-reader\n"
	body := make([]byte, len(next))
	if _, err := io.ReadFull(os.Stdin, body); err != nil || string(body) != next {
		t.Fatalf("guard closed stdin or consumed later input: %q, %v", body, err)
	}
}

func TestOrkaGuardPromptCancellationAndInputOwnership(t *testing.T) {
	for _, mode := range []string{"deadline", "SIGINT", "SIGTERM", "confirm"} {
		t.Run(mode, func(t *testing.T) {
			_, opt, _, _, dir := orkaCreateFixture(t, "guard")
			master, slave := chatPTY(t, 100)
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, "-test.run=^TestOrkaGuardPromptHelper$")
			cmd.Env = append(os.Environ(), "KMX_ORKA_GUARD_HELPER="+mode)
			cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			waited := false
			defer func() {
				_ = cmd.Process.Kill()
				if !waited {
					<-done
				}
			}()
			var transcript strings.Builder
			chatPTYReadUntil(t, master, &transcript, func(s string) bool {
				return strings.Contains(s, "Type the context name to continue (anything else aborts): ")
			})
			chatPTYReadUntil(t, master, &transcript, func(s string) bool { return strings.Contains(s, "guard-reading") })
			switch mode {
			case "SIGINT", "SIGTERM":
				signal := syscall.SIGINT
				if mode == "SIGTERM" {
					signal = syscall.SIGTERM
				}
				if err := cmd.Process.Signal(signal); err != nil {
					t.Fatal(err)
				}
			case "confirm":
				if _, err := io.WriteString(master, "kind-test\nnext-reader\n"); err != nil {
					t.Fatal(err)
				}
			}
			chatPTYReadUntil(t, master, &transcript, func(s string) bool { return strings.Contains(s, "guard-returned") })
			if mode != "confirm" {
				if _, err := io.WriteString(master, "next-reader\n"); err != nil {
					t.Fatal(err)
				}
			}
			if err := <-done; err != nil {
				waited = true
				t.Fatalf("guard child failed: %v; %s", err, transcript.String())
			}
			waited = true
			if _, err := os.Stat(opt.Out); !os.IsNotExist(err) {
				t.Fatal("guard-only path emitted an artifact")
			}
			calls := orkaCalls(t, dir)
			if len(calls) != 1 || !strings.Contains(strings.Join(calls[0].Args, " "), "config view") {
				t.Fatalf("guard-only path reached cluster operations: %+v", calls)
			}
		})
	}
}
