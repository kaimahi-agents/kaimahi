package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
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

func TestStatusCommandReadinessAndReportParity(t *testing.T) {
	for _, tc := range []struct {
		name           string
		governed       bool
		desired, ready int
		secrets        string
		wantReady      bool
	}{
		{"direct", false, -1, 0, "", true},
		{"scaled zero", true, 0, 0, "secret/token", false},
		{"down", true, 1, 0, "secret/token", false},
		{"partial rollout", true, 2, 1, "secret/token", false},
		{"missing credential", true, 1, 1, "", false},
		{"required absent plane", true, -1, 0, "secret/token", false},
		{"healthy", true, 1, 1, "secret/token", true},
	} {
		for _, mode := range []string{"plain", "rich", "no-color"} {
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				t.Setenv("TERM", "xterm-256color")
				t.Setenv("NO_COLOR", "")
				if mode == "no-color" {
					t.Setenv("NO_COLOR", "1")
				}
				out, text := reportOutput(t, mode != "plain", 42)
				base := ""
				if tc.governed {
					base = governedModelURL
				}
				deployment := `{"items":[]}`
				if tc.desired >= 0 {
					deployment = fmt.Sprintf(`{"items":[{"metadata":{"name":"kaimahi-proxy"},"spec":{"replicas":%d},"status":{"readyReplicas":%d}}]}`, tc.desired, tc.ready)
				}
				combined := fmt.Sprintf(`{"items":[{"kind":"Agent","metadata":{"name":"alpha"},"spec":{"declarative":{"modelConfig":"model"}},"status":{"conditions":[{"type":"Ready","status":"True"},{"type":"Accepted","status":"True"}]}},{"kind":"ModelConfig","metadata":{"name":"model"},"spec":{"openAI":{"baseUrl":%q},"apiKeySecret":"token"},"status":{"conditions":[{"type":"Accepted","status":"True"}]}},{"kind":"Pod","metadata":{"name":"runtime"},"status":{"phase":"Running","conditions":[{"type":"Ready","status":"True"}]}}]}`, base)
				script := fmt.Sprintf(`case "$*" in
*"config view"*) printf '%%s' '{"current-context":"kind-test","contexts":[{"name":"kind-test","context":{"cluster":"kind-test"}}],"clusters":[{"name":"kind-test","cluster":{"server":"https://127.0.0.1:6443"}}]}';;
*"get agents.kagent.dev,modelconfigs,pods"*) printf '%%s' '%s';;
*"get deployments"*) printf '%%s' '%s';;
*"get secrets"*) printf '%%s' '%s';;
*) printf '%%s' '{"items":[]}';;
esac
`, combined, deployment, tc.secrets)
				a := reportApp(t, out, script)
				if err := a.Status(); err != nil {
					t.Fatal(err)
				}
				got := ansi.Strip(text())
				if strings.Contains(got, "attention required") == tc.wantReady {
					t.Fatalf("wrong overall readiness: %s", got)
				}
				if mode != "rich" && strings.Contains(text(), "\x1b") {
					t.Fatal("unexpected ANSI")
				}
				if mode != "plain" && (!strings.Contains(got, "Agents (1)") || !strings.Contains(got, "Runtime pods (1)")) {
					t.Fatalf("missing group counts: %s", got)
				}
				data, err := a.collectStatus()
				if err != nil {
					t.Fatal(err)
				}
				g := data.governanceOf()
				if governanceReady(g) != tc.wantReady {
					t.Fatalf("report/state mismatch: %+v", g)
				}
				var structured bytes.Buffer
				a.Out = &structured
				if err := a.StatusWithOptions(StatusOptions{Output: "json"}); err != nil {
					t.Fatal(err)
				}
				var document statusDocument
				if err := json.Unmarshal(structured.Bytes(), &document); err != nil {
					t.Fatal(err)
				}
				if document.Governance.Plane != g.Plane || document.Governance.Credentials.Present != g.Credentials.Present {
					t.Fatalf("structured numeric/state mismatch: %s", structured.String())
				}
			})
		}
	}
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
*"get agents.kagent.dev"*) printf '%s' '{"items":[]}';;
esac
`)
				var err error
				want := ""
				switch command {
				case "agents":
					err, want = a.ListAgents(""), "none"
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
						if text() != "Agents\n  none\n" {
							t.Fatalf("changed redirected agents: %q", text())
						}
					}
				}
			}
		})
	}
}
