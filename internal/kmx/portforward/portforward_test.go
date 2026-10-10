package portforward

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// subprocessKube supplies only Command: opening a tunnel must not require
// the model admin client's Secret-reading interface.
type subprocessKube struct {
	ctx      context.Context
	scenario string
	cmd      *exec.Cmd
}

func (k *subprocessKube) Command(args ...string) *exec.Cmd {
	k.cmd = exec.CommandContext(k.ctx, os.Args[0], append([]string{"-test.run=^TestForwardProcess$", "--"}, args...)...)
	k.cmd.Env = append(os.Environ(), "KMX_FORWARD_TEST="+k.scenario)
	return k.cmd
}

// TestForwardProcess is the executable boundary, not a mocked exec.Cmd. Its
// owned mode really binds the requested loopback socket before announcing it.
func TestForwardProcess(t *testing.T) {
	scenario := os.Getenv("KMX_FORWARD_TEST")
	if scenario == "" {
		return
	}
	args := os.Args[3:]
	if len(args) != 7 {
		fmt.Fprintln(os.Stderr, "unexpected kubectl arguments:", args)
		os.Exit(2)
	}
	local, remote, ok := strings.Cut(args[6], ":")
	want := []string{"-n", "agents", "port-forward", "--address", "127.0.0.1", "svc/tool", local + ":8080"}
	if !ok || remote != "8080" || !reflect.DeepEqual(args, want) {
		fmt.Fprintln(os.Stderr, "unexpected kubectl arguments:", args)
		os.Exit(2)
	}
	switch scenario {
	case "owned":
		listener, err := net.Listen("tcp4", "127.0.0.1:"+local)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "diagnostic one\ndiagnostic two")
		fmt.Println("Forwarding from 127.0.0.1:" + local + " -> 8080")
		for {
			conn, err := listener.Accept()
			if err != nil {
				os.Exit(1)
			}
			_, _ = io.WriteString(conn, "owned")
			_ = conn.Close()
		}
	case "ipv6":
		fmt.Println("Forwarding from [::1]:" + local + " -> 8080")
	case "wrong-port":
		fmt.Println("Forwarding from 127.0.0.1:1 -> 8080")
	case "silent":
	case "exit":
		fmt.Fprintln(os.Stderr, "deployment missing\ncheck the target")
		os.Exit(1)
	case "empty-exit":
		os.Exit(1)
	default:
		os.Exit(2)
	}
	time.Sleep(time.Hour)
	os.Exit(0)
}

func freePort(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return port
}

func testKube(t *testing.T, scenario string) *subprocessKube {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	return &subprocessKube{ctx: ctx, scenario: scenario}
}

func impatient(t *testing.T) {
	t.Helper()
	attempts, interval := pollAttempts, pollInterval
	pollAttempts, pollInterval = 100, 10*time.Millisecond
	t.Cleanup(func() { pollAttempts, pollInterval = attempts, interval })
}

func assertReaped(t *testing.T, k *subprocessKube) {
	t.Helper()
	if k.cmd.ProcessState == nil || k.cmd.ProcessState.Success() {
		t.Fatalf("forward subprocess was not reaped after failure/close: %v", k.cmd.ProcessState)
	}
}

func TestStartProvesLoopbackBindAndCloseReapsProcess(t *testing.T) {
	k := testKube(t, "owned")
	port := freePort(t)
	f, err := Start(k, "agents", "svc/tool", port, "8080")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if f.Port != port {
		t.Fatalf("local port = %q, want %q", f.Port, port)
	}
	select {
	case <-f.Done():
		t.Fatal("live forward reported exited")
	default:
	}
	conn, err := net.DialTimeout("tcp4", "127.0.0.1:"+port, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	body, err := io.ReadAll(conn)
	if err != nil || string(body) != "owned" {
		t.Fatalf("owned socket reply = %q, %v", body, err)
	}
	if !strings.Contains(f.Detail(), "diagnostic one\n  diagnostic two") {
		t.Fatalf("stderr diagnostics were dropped or not indented: %s", f.Detail())
	}
	// Multiple callers can tear down a session while its lifetime watcher runs.
	var closers sync.WaitGroup
	for range 4 {
		closers.Go(f.Close)
	}
	closers.Wait()
	select {
	case <-f.Done():
	default:
		t.Fatal("Close returned before process exit")
	}
	assertReaped(t, k)
	f.Close()
}

func TestStartRefusesPortOwnedByAnotherProcess(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	k := testKube(t, "owned")
	f, err := Start(k, "agents", "svc/tool", port, "8080")
	if f != nil {
		f.Close()
		t.Fatal("accepted somebody else's listening socket")
	}
	if err == nil || !strings.Contains(err.Error(), "never came up on 127.0.0.1:"+port) || !strings.Contains(err.Error(), "address already in use") {
		t.Fatalf("bind conflict lost refusal or subprocess diagnostic: %v", err)
	}
	assertReaped(t, k)
}

func TestStartRequiresOwnIPv4PortAnnouncement(t *testing.T) {
	impatient(t)
	for _, scenario := range []string{"ipv6", "wrong-port", "silent"} {
		t.Run(scenario, func(t *testing.T) {
			k := testKube(t, scenario)
			f, err := Start(k, "agents", "svc/tool", freePort(t), "8080")
			if f != nil {
				f.Close()
				t.Fatal("accepted an unproven bind")
			}
			if err == nil || !strings.Contains(err.Error(), "never came up") {
				t.Fatalf("unproven bind = %v", err)
			}
			assertReaped(t, k)
		})
	}
}

func TestStartReportsEarlyExitWithoutWaitingForBindTimeout(t *testing.T) {
	for _, scenario := range []string{"exit", "empty-exit"} {
		t.Run(scenario, func(t *testing.T) {
			k := testKube(t, scenario)
			started := time.Now()
			f, err := Start(k, "agents", "svc/tool", freePort(t), "8080")
			if f != nil {
				f.Close()
				t.Fatal("accepted early subprocess exit")
			}
			if err == nil || !strings.Contains(err.Error(), "never came up") {
				t.Fatalf("early exit = %v", err)
			}
			want := "deployment missing\n  check the target"
			if scenario == "empty-exit" {
				want = "no output from kubectl"
			}
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("exit lost subprocess diagnostic %q: %v", want, err)
			}
			if time.Since(started) > 3*time.Second {
				t.Fatal("early exit waited for bind timeout")
			}
			assertReaped(t, k)
		})
	}
}

func TestContextCancellationStopsWaitingForUnprovenBind(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	k := &subprocessKube{ctx: ctx, scenario: "silent"}
	started := time.Now()
	f, err := Start(k, "agents", "svc/tool", freePort(t), "8080")
	if f != nil {
		f.Close()
		t.Fatal("accepted cancelled forward")
	}
	if err == nil || !strings.Contains(err.Error(), "never came up") || ctx.Err() != context.DeadlineExceeded {
		t.Fatalf("cancelled bind = %v, context = %v", err, ctx.Err())
	}
	if time.Since(started) > 3*time.Second {
		t.Fatal("cancelled bind waited for full bind timeout")
	}
	assertReaped(t, k)
}

func TestContextCancellationClosesDoneAfterProvenBind(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	k := &subprocessKube{ctx: ctx, scenario: "owned"}
	f, err := Start(k, "agents", "svc/tool", freePort(t), "8080")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cancel()
	select {
	case <-f.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled owned process did not close Done")
	}
	assertReaped(t, k)
}

type missingCommand struct{}

func (missingCommand) Command(...string) *exec.Cmd {
	return exec.Command("/nonexistent-kmx-portforward-test/kubectl")
}

func TestStartFailurePreservesCauseAndTarget(t *testing.T) {
	f, err := Start(missingCommand{}, "agents", "svc/tool", "18080", "8080")
	if f != nil || !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), "cannot port-forward to svc/tool in agents") {
		t.Fatalf("command start failure lost cause/target: forward=%v error=%v", f, err)
	}
}

func TestNilForwardCloseIsSafe(t *testing.T) {
	var f *Forward
	f.Close()
}
