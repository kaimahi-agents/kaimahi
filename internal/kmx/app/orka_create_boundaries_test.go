package app

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/portforward"
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
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
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

func TestOrkaResultStartFailureKeepsSafeCleanupUncertainty(t *testing.T) {
	a, opt, _, _, _ := orkaCreateFixture(t, "forward-fail")
	opt.ResultServiceAccount = "reader"
	opt.OrkaAPIService = "orka-api"
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	session, err := a.openOrkaResultSession(ctx, opt)
	if session != nil || err == nil {
		t.Fatalf("failed forward opened a session: session=%v err=%v", session, err)
	}
	if strings.Contains(err.Error(), orkaTestToken()) || strings.Contains(err.Error(), "external failure") {
		t.Fatalf("forward startup leaked subprocess diagnostics: %v", err)
	}
	// Ordinary startup failure does not invent cleanup uncertainty; the typed
	// sentinel is preserved when portforward.Start supplies it.
	if errors.Is(err, portforward.ErrCleanupUnconfirmed) {
		t.Fatalf("confirmed process exit reported uncertain cleanup: %v", err)
	}
}

func TestOrkaResultStartFailureRedactsCleanupCause(t *testing.T) {
	const private = "private-cleanup-canary"
	failure := withOrkaResultCleanupStatus(&orkaResultPortError{}, errors.Join(errors.New(private), portforward.ErrCleanupUnconfirmed))
	if !errors.Is(failure, portforward.ErrCleanupUnconfirmed) || strings.Contains(failure.Error(), private) {
		t.Fatalf("unsafe cleanup classification: %v", failure)
	}
	plain := withOrkaResultCleanupStatus(&orkaResultPortError{}, errors.New(private))
	if errors.Is(plain, portforward.ErrCleanupUnconfirmed) || strings.Contains(plain.Error(), private) {
		t.Fatalf("ordinary startup failure leaked or invented cleanup status: %v", plain)
	}
}

func TestOrkaCleanupWarningRedactsCause(t *testing.T) {
	const private = "private-cleanup-canary"
	var diagnostics bytes.Buffer
	a := &App{Err: &diagnostics}
	a.reportOrkaForwardCleanup("result session", errors.Join(errors.New(private), portforward.ErrCleanupUnconfirmed))
	if got, want := diagnostics.String(), "warning: Orka result session cleanup unconfirmed; a kubectl port-forward process may still be running\n"; got != want || strings.Contains(got, private) {
		t.Fatalf("unsafe cleanup warning %q", got)
	}
	diagnostics.Reset()
	a.reportOrkaForwardCleanup("tool port-forward", errors.New(private))
	if got, want := diagnostics.String(), "warning: Orka tool port-forward cleanup failed after the forward exited\n"; got != want || strings.Contains(got, private) {
		t.Fatalf("unsafe cleanup warning %q", got)
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
