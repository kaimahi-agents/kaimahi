package app

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOrkaForwardClosedOnCancellationAndBindTimeout(t *testing.T) {
	for _, scenario := range []string{"", "forward-hang"} {
		t.Run(scenario, func(t *testing.T) {
			a, opt, _, _, dir := orkaCreateFixture(t, scenario)
			opt.ResultServiceAccount = "reader"
			opt.OrkaAPIService = "orka-api"
			orkaResultServer(t, &opt, func(http.ResponseWriter, *http.Request) {
				t.Error("opening a session sent HTTP before its explicit access probe")
			})
			// Keep the original setup cap; expire the hang at the bind boundary.
			var deadline func() context.Context
			if scenario == "forward-hang" {
				deadline = orkaTestDeadline(t, a, "orka-result-forward")
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			started := time.Now()
			session, err := a.openOrkaResultSession(ctx, opt)
			if scenario == "" {
				if err != nil {
					t.Fatal(err)
				}
				cancel()
				session.close()
				if session.token != "" {
					t.Fatal("closed session retained token")
				}
			} else {
				if err == nil {
					session.close()
					t.Fatal("unproven bind admitted")
				}
				if deadline().Err() != context.DeadlineExceeded {
					t.Fatalf("unproven bind did not reach its phase deadline: %v", err)
				}
			}
			if time.Since(started) > 2*time.Second {
				t.Fatal("cancelled forward waited for full bind timeout")
			}
			assertOrkaForwardExited(t, dir)
		})
	}
}

func TestOrkaResultSessionRefusesTokenOutsideRequestedLifetime(t *testing.T) {
	for _, scenario := range []string{"short-token", "long-token"} {
		t.Run(scenario, func(t *testing.T) {
			a, opt, _, _, dir := orkaCreateFixture(t, scenario)
			opt.ResultServiceAccount = "reader"
			opt.OrkaAPIService = "orka-api"
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
			defer cancel()
			session, err := a.openOrkaResultSession(ctx, opt)
			if session != nil {
				_ = session.close()
				t.Fatal("out-of-bound token lifetime opened a result session")
			}
			if err == nil || !strings.Contains(err.Error(), "token lifetime") {
				t.Fatalf("out-of-bound token lifetime accepted: %v", err)
			}
			if _, statErr := os.Stat(filepath.Join(dir, "forward-pid")); !os.IsNotExist(statErr) {
				t.Fatalf("out-of-bound token reached the forward: %v", statErr)
			}
		})
	}
}

func TestOrkaKubectlCaptureCancellationBoundsHungProcess(t *testing.T) {
	a, _, _, _, _ := orkaCreateFixture(t, "hang-get")
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := a.orkaCapture(ctx, nil, "get", "crd", "agents.core.orka.ai", "-o", "json"); err == nil {
		t.Fatal("hung process succeeded")
	}
	if time.Since(started) > time.Second {
		t.Fatal("capture ignored cancellation")
	}
}

func TestOrkaKubectlCaptureRejectsOversizedStdout(t *testing.T) {
	a, _, _, _, _ := orkaCreateFixture(t, "oversized-get")
	if raw, err := a.orkaCapture(t.Context(), nil, "get", "crd", "agents.core.orka.ai", "-o", "json"); err == nil {
		t.Fatalf("accepted %d bytes from subprocess", len(raw))
	}
}
