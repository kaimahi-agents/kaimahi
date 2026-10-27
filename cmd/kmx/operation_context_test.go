package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"reflect"
	"testing"
	"time"

	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

func TestOperationApplicationContextIsInvocationLocal(t *testing.T) {
	var out, diag bytes.Buffer
	deps, loads := testDependencies(&out, &diag)
	baseline := run.Default()
	baseline.Context = t.Context()
	deps.newApp = func(cfg *config.Config) *app.App {
		configured := app.New(cfg)
		configured.Run = baseline
		return configured
	}
	type contextKey struct{}
	firstCtx, cancelFirst := context.WithCancel(context.WithValue(t.Context(), contextKey{}, "first"))
	defer cancelFirst()
	secondCtx, cancelSecond := context.WithTimeout(context.WithValue(t.Context(), contextKey{}, "second"), time.Minute)
	defer cancelSecond()
	var runners []*run.Runner
	for _, ctx := range []context.Context{firstCtx, secondCtx} {
		state := &commandState{deps: deps, argv: []string{"cluster", "orka", "status"}}
		configured, err := state.application()
		if err != nil {
			t.Fatal(err)
		}
		cmd := &cobra.Command{Use: "status"}
		cmd.SetContext(ctx)
		operation, err := state.operationApplication(cmd)
		if err != nil {
			t.Fatal(err)
		}
		if operation != configured || state.app != configured || operation.Run == baseline || operation.Run.Context != ctx {
			t.Fatal("operation did not preserve App identity with an invocation-local runner context")
		}
		if operation.Cfg != configured.Cfg || operation.Run.Stdout != baseline.Stdout ||
			operation.Run.Stderr != baseline.Stderr || operation.Run.Echo != baseline.Echo ||
			!reflect.DeepEqual(operation.Run.Env, baseline.Env) || !reflect.DeepEqual(operation.Run.Unset, baseline.Unset) {
			t.Fatal("context binding changed configuration or subprocess settings")
		}
		if operation.InvocationCommand == "" {
			t.Fatal("context binding lost invocation identity")
		}
		runners = append(runners, operation.Run)
	}
	cancelFirst()
	if !errors.Is(runners[0].Context.Err(), context.Canceled) || runners[1].Context.Err() != nil {
		t.Fatal("cancellation leaked between operation contexts")
	}
	if baseline.Context != t.Context() || baseline.Context.Err() != nil || *loads != 2 {
		t.Fatal("operation binding mutated the shared runner context")
	}
	deadline, ok := runners[1].Context.Deadline()
	wantDeadline, _ := secondCtx.Deadline()
	if !ok || !deadline.Equal(wantDeadline) || runners[1].Context.Value(contextKey{}) != "second" {
		t.Fatal("operation lost the caller deadline or context values")
	}
}

type operationCancelWriter struct {
	cancel context.CancelFunc
}

func (w operationCancelWriter) Write(p []byte) (int, error) {
	w.cancel()
	return len(p), nil
}

func TestAppRunContextCancelsActiveSubprocess(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	parent, stopParent := context.WithTimeout(t.Context(), 5*time.Second)
	defer stopParent()
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	var out, diag bytes.Buffer
	deps, _ := testDependencies(&out, &diag)
	deps.stdout = operationCancelWriter{cancel: cancel}
	var configured *app.App
	var baseline *run.Runner
	deps.newApp = func(cfg *config.Config) *app.App {
		configured = app.New(cfg)
		configured.Run.Context = parent
		configured.Run.Env = append(configured.Run.Env, "KMX_TEST_OPERATION_CONTEXT_CHILD=1")
		baseline = configured.Run
		return configured
	}
	state := &commandState{deps: deps}
	root := newRootCommand(state)
	root.AddCommand(&cobra.Command{Use: "context-test", RunE: appRun(state, func(a *app.App) error {
		return a.Run.Run(executable, "-test.run=^TestOperationContextChildProcess$")
	})})
	root.SetArgs([]string{"context-test"})
	err = root.ExecuteContext(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("caller cancellation did not stop the active child: %v", err)
	}
	if baseline.Context != parent || configured.Run == baseline || parent.Err() != nil {
		t.Fatal("operation rebound or cancelled the original shared runner context")
	}
}

func TestAppRunCancelledContextPreventsSubprocessStart(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	parent, stopParent := context.WithTimeout(t.Context(), 5*time.Second)
	defer stopParent()
	ctx, cancel := context.WithCancel(parent)
	cancel()
	var out, diag bytes.Buffer
	deps, _ := testDependencies(&out, &diag)
	deps.newApp = func(cfg *config.Config) *app.App {
		configured := app.New(cfg)
		configured.Run.Context = parent
		return configured
	}
	state := &commandState{deps: deps}
	root := newRootCommand(state)
	root.AddCommand(&cobra.Command{Use: "context-test", RunE: appRun(state, func(a *app.App) error {
		a.Run.Env = append(a.Run.Env, "KMX_TEST_OPERATION_CONTEXT_CHILD=1")
		return a.Run.Run(executable, "-test.run=^TestOperationContextChildProcess$")
	})})
	root.SetArgs([]string{"context-test"})
	err = root.ExecuteContext(ctx)
	if !errors.Is(err, context.Canceled) || out.Len() != 0 {
		t.Fatalf("cancelled invocation started a subprocess: %v / %s", err, out.String())
	}
}

func TestOperationContextChildProcess(t *testing.T) {
	if os.Getenv("KMX_TEST_OPERATION_CONTEXT_CHILD") != "1" {
		return
	}
	fmt.Fprintln(os.Stdout, "ready")
	time.Sleep(30 * time.Second)
	os.Exit(0)
}

type operationSessionsServer struct {
	v1.UnimplementedSessionsServer
	cancel   context.CancelFunc
	finished chan error
}

func (s operationSessionsServer) ListSessions(ctx context.Context, _ *v1.ListSessionsRequest) (*v1.ListSessionsResponse, error) {
	s.cancel()
	<-ctx.Done()
	s.finished <- ctx.Err()
	return nil, ctx.Err()
}

func TestAppRunContextCancelsActiveNetworkRequest(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	parent, stopParent := context.WithTimeout(t.Context(), 5*time.Second)
	defer stopParent()
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	finished := make(chan error, 1)
	server := grpc.NewServer()
	v1.RegisterSessionsServer(server, operationSessionsServer{cancel: cancel, finished: finished})
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		if err := <-served; err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			t.Errorf("Sessions fixture failed: %v", err)
		}
	})
	var out, diag bytes.Buffer
	deps, _ := testDependencies(&out, &diag)
	deps.newApp = func(cfg *config.Config) *app.App {
		configured := app.New(cfg)
		configured.Run.Context = parent
		return configured
	}
	state := &commandState{deps: deps}
	root := newRootCommand(state)
	root.AddCommand(&cobra.Command{Use: "context-test", RunE: appRun(state, func(a *app.App) error {
		return a.Targets(app.TargetsOptions{Output: "json", Sessions: listener.Addr().String()})
	})})
	root.SetArgs([]string{"context-test"})
	var unreadable *app.TargetsUnreadableError
	if err := root.ExecuteContext(ctx); !errors.As(err, &unreadable) || parent.Err() != nil {
		t.Fatalf("request did not stop on invocation cancellation: %v", err)
	}
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("server observed expiry instead of cancellation: %v", err)
		}
	case <-parent.Done():
		t.Fatal("network request remained active after invocation cancellation")
	}
}
