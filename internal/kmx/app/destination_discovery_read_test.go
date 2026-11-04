package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDiscoveryReadErrorTokens(t *testing.T) {
	for _, tc := range []struct {
		text, want string
		overflow   bool
	}{
		{"Error from server (Forbidden): secret\n", "read_denied", false},
		{"Error from server (NotFound): NotFound\r\n", "resource_missing", false},
		{"error: You must be logged in to the server (Unauthorized)\n", "unauthorized", false},
		{"Unable to connect to the server: EOF\n", "network_unavailable", false},
		{"Error from server (Unauthorized): echo", "read_unknown", false},
		{"Error from server (Forbidden)extra", "read_unknown", false},
		{"quoted Error from server (Forbidden): echo", "read_unknown", false},
		{"\"Error from server (Forbidden): echo\"", "read_unknown", false},
		{"Error from server (Forbidden): x\nUnable to connect: x", "read_unknown", false},
		{"Error from server (Forbidden): x", "read_unknown", true},
		{"The connection to the server localhost was refused", "read_unknown", false},
		{"error from server (Forbidden): x", "read_unknown", false},
	} {
		if got := classifyDiscoveryRead([]byte(tc.text), tc.overflow); string(got) != tc.want {
			t.Errorf("%q: %s != %s", tc.text, got, tc.want)
		}
	}
}

func TestDiscoveryCaptureBoundsAndCancellation(t *testing.T) {
	for _, stream := range []string{"stdout", "stderr"} {
		t.Run(stream, func(t *testing.T) {
			a := captureTestApp()
			a.captureCommand = func(ctx context.Context, c *exec.Cmd) error {
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > 15*time.Second {
					t.Fatal("deadline")
				}
				if c.WaitDelay != time.Second || c.Stdin != nil {
					t.Fatal("capture mechanics")
				}
				var w io.Writer = c.Stdout
				size := 4 << 20
				if stream == "stderr" {
					w = c.Stderr
					size = 4 << 10
				}
				_, _ = w.Write(make([]byte, size+1))
				return nil // overflow must survive a hook ignoring Write's error
			}
			_, err := a.discoveryCapture(t.Context(), "chosen", "get", "--raw", "/api/v1/namespaces")
			if err == nil || err.reason != "read_unknown" {
				t.Fatalf("overflow: %v", err)
			}
		})
	}
	a := captureTestApp()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	a.captureCommand = func(context.Context, *exec.Cmd) error { t.Fatal("cancelled call dispatched"); return nil }
	_, err := a.discoveryCapture(ctx, "chosen", "get", "--raw", "/api/v1/namespaces")
	if !errors.Is(err, context.Canceled) || err.safeCode() != "cancelled" {
		t.Fatalf("cancel: %v", err)
	}
	a.captureCommand = func(_ context.Context, c *exec.Cmd) error {
		fmt.Fprint(c.Stderr, "Error from server (Forbidden): credential-value")
		return errors.New("private endpoint")
	}
	_, err = a.discoveryCapture(t.Context(), "chosen", "get", "--raw", "/api/v1/namespaces")
	if err == nil || err.safeCode() != "permission_denied" || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "credential") {
		t.Fatalf("unsafe failure: %v", err)
	}
}

func TestDiscoveryChildSignalCancellation(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("Unix signal fixture")
	}
	for _, tc := range []struct {
		command, code string
		cancelled     bool
	}{{"kill -INT $$", "cancelled", true}, {"kill -TERM $$", "cancelled", true}, {"kill -KILL $$", "operation_failed", false}, {"exit 130", "operation_failed", false}} {
		a := captureTestApp()
		a.captureCommand = func(context.Context, *exec.Cmd) error { return exec.Command("sh", "-c", tc.command).Run() }
		_, err := a.discoveryCapture(t.Context(), "chosen", "get", "--raw", "/api/v1/namespaces")
		if err == nil || err.safeCode() != tc.code || errors.Is(err, context.Canceled) != tc.cancelled {
			t.Fatalf("%s: %v", tc.command, err)
		}
	}
}

func TestDiscoveryRunnerPreferencesAreIsolated(t *testing.T) {
	a := captureTestApp()
	a.Run.Env = []string{"KUBERC=foreign", "KUBECTL_KUBERC=true", "KEEP=yes"}
	a.Run.Unset = []string{"OTHER"}
	beforeEnv, beforeUnset := append([]string(nil), a.Run.Env...), append([]string(nil), a.Run.Unset...)
	t.Setenv("KUBERC", "ambient")
	a.captureCommand = func(_ context.Context, c *exec.Cmd) error {
		counts := map[string]int{}
		for _, e := range c.Env {
			k, v, _ := strings.Cut(e, "=")
			if k == "KUBERC" || k == "KUBECTL_KUBERC" {
				counts[k]++
				if (k == "KUBERC" && v != "off") || (k == "KUBECTL_KUBERC" && v != "false") {
					t.Fatal(e)
				}
			}
		}
		if counts["KUBERC"] != 1 || counts["KUBECTL_KUBERC"] != 1 {
			t.Fatal(counts)
		}
		if !reflect.DeepEqual(c.Args[1:], []string{"--context", "chosen", "--request-timeout=10s", "get", "--raw", "/api/v1/namespaces"}) {
			t.Fatal(c.Args)
		}
		return nil
	}
	if _, err := a.discoveryCapture(t.Context(), "chosen", "get", "--raw", "/api/v1/namespaces"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a.Run.Env, beforeEnv) || !reflect.DeepEqual(a.Run.Unset, beforeUnset) {
		t.Fatal("caller changed")
	}
}
