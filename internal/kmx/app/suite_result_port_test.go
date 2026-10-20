package app

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// These exercise the real result session through both suite error boundaries:
// a regression to requested-port dialing/defaulting cannot reach the service.
func TestSuiteEvaluationResultPort(t *testing.T) {
	for _, mode := range []string{"automatic", "explicit", "occupied explicit", "canonical occupied explicit"} {
		t.Run(mode, func(t *testing.T) {
			canonical := strings.HasPrefix(mode, "canonical")
			var a *App
			var f *evalFixture
			var state *publicCandidateFixture
			if canonical {
				a, _, _, f = nativeEvaluationFixture(t)
			} else {
				a, _, f, state = publicNativeEvaluationFixture(t)
			}
			t.Setenv("KMX_EVAL_FORWARD_TARGET", f.resultAddress)
			opt := nativeEvaluationOptions(t)
			opt.ResultPort = ""
			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = listener.Close() })
			_, heldPort, err := net.SplitHostPort(listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			if mode != "automatic" {
				opt.ResultPort = heldPort
			}
			if mode == "explicit" {
				_ = listener.Close()
			}
			// Exercise repeat stdout as well as the returned error.
			casePath := filepath.Join(opt.Workspace, "eval/example.yaml")
			raw, _ := os.ReadFile(casePath)
			writeFile(t, casePath, string(raw)+"\nruns: 2\n")
			p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			for _, frame := range p.inventory {
				f.answers[frame.Case.Input] = nativeCanary + " answer"
			}
			ports := make(chan string, len(p.inventory))
			f.resultRequest = func(r *http.Request) { ports <- r.Host }
			if canonical {
				err = a.evaluateSuiteWorkspace(t.Context(), p, false)
			} else {
				err = a.EvaluateSuiteWorkspace(t.Context(), p)
			}
			if state != nil && len(state.objects) != 0 {
				t.Fatal("result-port failure bypassed candidate cleanup")
			}
			if strings.HasSuffix(mode, "occupied explicit") {
				if err == nil || !strings.Contains(err.Error(), "result port") || strings.Contains(err.Error(), "outcome unknown") || strings.Contains(err.Error(), "before native changes") {
					t.Fatalf("port startup failure misclassified: %v", err)
				}
				if !strings.Contains(f.out.String(), "first run 1/2: result port") || strings.Contains(f.out.String(), "first run 2/2") || f.taskCreates(t) != 0 {
					t.Fatalf("port failure not reported/stopped: %s", f.out)
				}
				if strings.Contains(err.Error()+f.out.String()+a.Err.(*bytes.Buffer).String(), orkaTestToken()) {
					t.Fatal("subprocess payload leaked")
				}
				if _, e := os.Stat(opt.Out); !os.IsNotExist(e) {
					t.Fatal("port failure exported evidence")
				}
				return
			}
			if err != nil || len(ports) != len(p.inventory) {
				t.Fatalf("result service not reached: requests=%d err=%v", len(ports), err)
			}
			for range len(ports) {
				_, port, _ := net.SplitHostPort(<-ports)
				if port == "" || mode == "automatic" && (port == "19180" || port == heldPort) || mode == "explicit" && port != opt.ResultPort {
					t.Fatalf("wrong live result port %q (requested %q)", port, opt.ResultPort)
				}
			}
			if _, err := readSuitePassingReceipt(t.Context(), opt.Out); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestConcurrentSuiteEvaluationsUseDistinctResultPorts(t *testing.T) {
	a, _, f, state := publicNativeEvaluationFixture(t)
	t.Setenv("KMX_EVAL_FORWARD_TARGET", f.resultAddress)
	// Serialize the external cluster double, not evaluation or live forwarding.
	var cluster sync.Mutex
	capture := a.captureCommand
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		cluster.Lock()
		defer cluster.Unlock()
		return capture(ctx, cmd)
	}
	prepared := make([]*PreparedSuiteWorkspaceEvaluation, 2)
	for i := range prepared {
		opt := nativeEvaluationOptions(t) // distinct workspaces; same-workspace eval is locked
		opt.ResultPort = ""
		p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
		if err != nil {
			t.Fatal(err)
		}
		defer p.Close()
		prepared[i] = p
		for _, frame := range p.inventory {
			f.answers[frame.Case.Input] = nativeCanary + " answer"
		}
	}
	ports, release := make(chan string, 4), make(chan struct{})
	var held sync.Map
	f.resultRequest = func(r *http.Request) {
		if _, seen := held.LoadOrStore(r.Host, true); !seen {
			ports <- r.Host
			select {
			case <-release:
			case <-r.Context().Done():
			}
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	errs := make(chan error, 2)
	for _, p := range prepared {
		worker := *a
		worker.Out, worker.Err = &bytes.Buffer{}, &bytes.Buffer{}
		go func() { errs <- worker.EvaluateSuiteWorkspace(ctx, p) }()
	}
	var live []string
	for len(live) < 2 {
		select {
		case port := <-ports:
			live = append(live, port)
		case <-ctx.Done():
			close(release)
			for range 2 {
				<-errs
			}
			t.Fatal("evaluations did not open simultaneous result connections", live)
		}
	}
	close(release)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if live[0] == live[1] || len(state.objects) != 0 {
		t.Fatalf("evaluations shared a port or left candidates: %v", live)
	}
}
