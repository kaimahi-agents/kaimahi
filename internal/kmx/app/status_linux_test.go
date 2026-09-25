package app

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
	"golang.org/x/sys/unix"
)

// Buffer writes remain inspectable while capability detection uses a real TTY.
type reportTerminal struct {
	bytes.Buffer
	fd int
}

func (w *reportTerminal) Fd() uintptr { return uintptr(w.fd) }

func reportOutput(t *testing.T, rich bool, width int) (io.Writer, func() string) {
	t.Helper()
	if !rich {
		b := &bytes.Buffer{}
		return b, b.String
	}
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unix.Close(fd) })
	if err := unix.IoctlSetWinsize(fd, unix.TIOCSWINSZ, &unix.Winsize{Col: uint16(width), Row: 24}); err != nil {
		t.Fatal(err)
	}
	w := &reportTerminal{fd: fd}
	return w, w.String
}

func reportApp(t *testing.T, out io.Writer, script string) *App {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte("#!/bin/sh\n"+script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return &App{Cfg: &config.Config{KubeContext: "kind-test", ContextSource: config.SourceKubeCtx}, Out: out, Err: io.Discard,
		Run: &run.Runner{Stdout: out, Stderr: io.Discard}}
}

func TestStatusExplainsHowToCompleteMissingDefaultSetup(t *testing.T) {
	a := reportApp(t, io.Discard, `case "$*" in
*"config view"*) printf '%s' '{"current-context":"other","contexts":[{"name":"other","context":{"cluster":"other"}}],"clusters":[{"name":"other","cluster":{"server":"https://example.test"}}]}';;
esac`)
	a.Cfg.ContextSource = config.SourceDefault
	err := a.Status()
	if err == nil || !strings.Contains(err.Error(), "setup is incomplete") || !strings.Contains(err.Error(), "kmx quickstart") {
		t.Fatalf("missing default context did not offer the repair path: %v", err)
	}
}

func TestCtxShowsHowToCompleteMissingDefaultSetup(t *testing.T) {
	var out bytes.Buffer
	a := reportApp(t, &out, `case "$*" in
*"config view"*) printf '%s' '{"current-context":"other","contexts":[{"name":"other","context":{"cluster":"other"}}],"clusters":[{"name":"other","cluster":{"server":"https://example.test"}}]}';;
esac`)
	a.Cfg.ContextSource = config.SourceDefault
	if err := a.Ctx(""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "setup:   incomplete") || !strings.Contains(out.String(), "kmx quickstart") {
		t.Fatalf("context report did not offer the repair path: %s", out.String())
	}
}

func TestReadCommandsRichAndPlain(t *testing.T) {
	for _, rich := range []bool{false, true} {
		t.Run(fmt.Sprint(rich), func(t *testing.T) {
			t.Setenv("TERM", "xterm")
			t.Setenv("NO_COLOR", "1")
			for _, command := range []string{"agents", "context"} {
				out, text := reportOutput(t, rich, 32)
				a := reportApp(t, out, `case "$*" in
*"config view"*) printf '%s' '{"clusters":[{"name":"c","cluster":{"server":"https://127.0.0.1:6443"}}],"contexts":[{"name":"kind-test","context":{"cluster":"c"}}]}';;
*"get agents.core.orka.ai"*) printf '%s' '{"items":[]}';;
esac
`)
				var err error
				want := ""
				switch command {
				case "agents":
					err, want = a.ListAgents("", ""), "none"
				case "context":
					err, want = a.Ctx(""), "kind-test"
				}
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(text(), want) || strings.Contains(text(), "\x1b") {
					t.Fatalf("%s: %q", command, text())
				}
				if !rich {
					switch command {
					case "agents":
						if text() != "Orka Agents in "+OrkaNamespace+"\n  none\n" {
							t.Fatalf("changed redirected agents: %q", text())
						}
					}
				}
			}
		})
	}
}
