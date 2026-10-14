package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	protocol "github.com/kaimahi-agents/kaimahi/internal/kmx/app/kubectlfake"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

func captureTestApp() *App {
	return &App{Cfg: &config.Config{KubeContext: "kind-test"}, Run: &run.Runner{}}
}

func TestCaptureExecutorDefaultsToCommandAndIsInstanceLocal(t *testing.T) {
	dir := t.TempDir()
	fakeTool(t, dir, "capture", "printf 'real command'; exit 7")
	cmd := exec.CommandContext(t.Context(), filepath.Join(dir, "capture"))
	var out bytes.Buffer
	cmd.Stdout = &out
	a := captureTestApp()
	called := false
	hooked := *a
	hooked.captureCommand = func(context.Context, *exec.Cmd) error { called = true; return nil }
	if err := hooked.executeCapture(t.Context(), exec.Command("missing")); err != nil || !called {
		t.Fatalf("local hook: %v", err)
	}
	err := a.executeCapture(t.Context(), cmd)
	var status *exec.ExitError
	if !errors.As(err, &status) || status.ExitCode() != 7 || out.String() != "real command" {
		t.Fatalf("default executor: %v %q", err, out.String())
	}
}

func TestCaptureHookGetsPreparedCommandAndEffectiveEnvironmentPerCall(t *testing.T) {
	for _, kind := range []string{"orka", "kagent"} {
		t.Run(kind, func(t *testing.T) {
			a := captureTestApp()
			t.Setenv("CAPTURE_AMBIENT", "initial")
			t.Setenv("CAPTURE_REMOVED", "ambient")
			a.Run.Env = []string{"CAPTURE_DUP=first", "CAPTURE_DUP=last"}
			a.Run.Unset = []string{"CAPTURE_REMOVED"}
			type contextKey struct{}
			parent := context.WithValue(t.Context(), contextKey{}, "carried")
			expectedAmbient := "initial"
			a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
				if ctx.Value(contextKey{}) != "carried" {
					t.Fatal("parent context lost")
				}
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("capture deadline lost")
				}
				expectedArgs := []string{"--context", "kind-test", "--request-timeout=10s", "get", "agent"}
				if !slices.Equal(cmd.Args[1:], expectedArgs) {
					t.Fatalf("args=%v", cmd.Args)
				}
				r := captureFixtureRequest(ctx, cmd)
				if r.Getenv("CAPTURE_AMBIENT") != expectedAmbient || r.Getenv("CAPTURE_DUP") != "last" || r.Getenv("CAPTURE_REMOVED") != "" {
					t.Fatal("effective env lost")
				}
				{
					raw, err := io.ReadAll(cmd.Stdin)
					if err != nil || string(raw) != "request input" {
						t.Fatalf("stdin=%q %v", raw, err)
					}
				}
				if cmd.Stderr == nil || cmd.WaitDelay != time.Second {
					t.Fatal("prepared I/O or wait bound lost")
				}
				response := "response"
				_, err := fmt.Fprint(cmd.Stdout, response)
				return err
			}
			for _, ambient := range []string{"initial", "changed"} {
				expectedAmbient = ambient
				t.Setenv("CAPTURE_AMBIENT", ambient)
				var raw []byte
				var err error
				switch kind {
				case "orka":
					raw, err = a.orkaCapture(parent, []byte("request input"), "get", "agent")
				case "kagent":
					raw, err = a.kagentCaptureWithTimeout(parent, time.Second, "10s", []byte("request input"), "get", "agent")
				}
				if err != nil || string(raw) != "response" {
					t.Fatalf("capture: %q %v", raw, err)
				}
			}
		})
	}
}

func TestCaptureHookPreservesCancellationAndSafeClassifiers(t *testing.T) {
	for _, kind := range []string{"orka", "kagent"} {
		for _, tc := range []struct{ name, stderr, want string }{
			{"forbidden", "Error from server (Forbidden): agents is forbidden: private-token-must-not-escape", "forbidden"},
			{"transport", "transport private-token-must-not-escape", "failed"},
			{"cancelled", "", "cancelled or timed out"},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				a := captureTestApp()
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				a.captureCommand = func(callCtx context.Context, cmd *exec.Cmd) error {
					if tc.name == "cancelled" {
						cancel()
						<-callCtx.Done()
						return callCtx.Err()
					}
					_, _ = fmt.Fprint(cmd.Stderr, tc.stderr)
					return errors.New("private executor diagnostic")
				}
				var err error
				switch kind {
				case "orka":
					_, err = a.orkaCapture(ctx, nil, "get", "agent")
				case "kagent":
					_, err = a.kagentCaptureWithTimeout(ctx, time.Second, "10s", nil, "get", "agent")
				}
				if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "private") {
					t.Fatalf("classification: %v", err)
				}
			})
		}
	}
	a := captureTestApp()
	called := false
	a.captureCommand = func(context.Context, *exec.Cmd) error { called = true; return nil }
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _ = a.orkaCapture(ctx, nil, "get", "agent")
	_, _ = a.kagentCaptureWithTimeout(ctx, time.Second, "10s", nil, "get", "agent")
	if called {
		t.Fatal("already cancelled request executed")
	}
}

func TestCaptureHookDeadlineIsSynchronous(t *testing.T) {
	a := captureTestApp()
	finished := false
	a.captureCommand = func(ctx context.Context, _ *exec.Cmd) error { <-ctx.Done(); finished = true; return ctx.Err() }
	_, err := a.kagentCaptureWithTimeout(t.Context(), time.Millisecond, "1ms", nil, "get", "agent")
	if !finished || err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("deadline boundary: %v finished=%t", err, finished)
	}
}

// The protocol still reports ignored writer failures through the hook; no
// truncated successful response may escape either capture's output bounds.
func TestCaptureHookRejectsProtocolStdoutAndStderrOverflow(t *testing.T) {
	for _, kind := range []string{"orka", "kagent"} {
		for _, stream := range []string{"stdout", "stderr"} {
			t.Run(kind+"/"+stream, func(t *testing.T) {
				a := captureTestApp()
				dir := t.TempDir()
				t.Setenv("KMX_RECONCILE_DIR", dir)
				if stream == "stdout" {
					t.Setenv("KMX_LIFT_TEST", "1")
					t.Setenv("KMX_LIFT_CLUSTER_UID", strings.Repeat("X", 5<<20))
				}
				a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
					r := captureFixtureRequest(ctx, cmd)
					if stream == "stderr" {
						r.Stderr = expandingWriter{writer: r.Stderr}
					}
					return protocol.Reconcile(r)
				}
				var err error
				switch kind {
				case "orka":
					if stream == "stdout" {
						_, err = a.orkaCapture(t.Context(), nil, "get", "namespace", "kube-system")
					} else {
						a.Cfg.KubeContext = "wrong"
						_, err = a.orkaCapture(t.Context(), nil, "config", "view")
					}
				case "kagent":
					if stream == "stdout" {
						_, err = a.kagentCaptureWithTimeout(t.Context(), time.Second, "10s", nil, "get", "namespace", "kube-system")
					} else {
						a.Cfg.KubeContext = "wrong"
						_, err = a.kagentCaptureWithTimeout(t.Context(), time.Second, "10s", nil, "config", "view")
					}

				}
				if err == nil || strings.Contains(err.Error(), "private") {
					t.Fatalf("overflow concealed: %v", err)
				}
			})
		}
	}
}

type expandingWriter struct{ writer io.Writer }

func (w expandingWriter) Write([]byte) (int, error) {
	return w.writer.Write([]byte(strings.Repeat("X", 5<<10)))
}
