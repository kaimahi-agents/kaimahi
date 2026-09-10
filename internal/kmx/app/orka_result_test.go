package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func orkaResultServer(t *testing.T, opt *CreateOptions, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	_, opt.ResultPort, _ = net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	opt.Task = "Say hello"
	opt.ResultServiceAccount = "reader"
	return server
}

// Unlike the ordinary HTTP fixtures, this helper really owns the listening
// socket, so killing it reproduces local-port reuse after a proven bind.
func TestOrkaResultRefusesDeadForwardPortReuse(t *testing.T) {
	for _, probe := range []bool{false, true} {
		t.Run(fmt.Sprintf("after-probe=%t", probe), func(t *testing.T) {
			a, opt, _, _, dir := orkaCreateFixture(t, "owned-forward")
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			addr := listener.Addr().String()
			_, opt.ResultPort, _ = net.SplitHostPort(addr)
			listener.Close()
			opt.Task, opt.ResultServiceAccount = "Say hello", "reader"
			if err := validateOrkaResultOptions(&opt); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			session, err := a.openOrkaResultSession(ctx, opt)
			if err != nil {
				t.Fatal(err)
			}
			defer session.close()
			if probe {
				if err := session.probe(ctx, opt.Namespace, "absent-task"); err != nil {
					t.Fatal(err)
				}
			}
			session.forward.Close()
			assertOrkaForwardExited(t, dir)
			replacement, err := net.Listen("tcp", addr)
			if err != nil {
				t.Fatal(err)
			}
			var disclosed atomic.Bool
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				disclosed.Store(r.Header.Get("Authorization") == "Bearer "+orkaTestToken())
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"result":"replacement listener answer"}`)
			})}
			defer server.Close()
			go func() { _ = server.Serve(replacement) }()
			status, _, err := session.get(ctx, opt.Namespace, "absent-task")
			if disclosed.Load() {
				t.Fatalf("bearer disclosed to reused port (HTTP %d, error=%v)", status, err)
			}
			if err == nil {
				t.Fatal("accepted request after owned forward exited")
			}
		})
	}
}

func TestOrkaResultNeverRedialsAfterConnectionClose(t *testing.T) {
	for _, closeBy := range []string{"server", "transport"} {
		t.Run(closeBy, func(t *testing.T) {
			a, opt, _, _, _ := orkaCreateFixture(t, "")
			var requests atomic.Int32
			server := orkaResultServer(t, &opt, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusNotFound)
				fmt.Fprint(w, `{"error":{"code":404,"message":"task not found"}}`)
			})
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			session, err := a.openOrkaResultSession(ctx, opt)
			if err != nil {
				t.Fatal(err)
			}
			defer session.close()
			if err := session.probe(ctx, opt.Namespace, "absent-task"); err != nil {
				t.Fatal(err)
			}
			if closeBy == "server" {
				server.CloseClientConnections()
			} else {
				session.client.CloseIdleConnections()
			}
			for range 2 {
				if _, _, err := session.get(ctx, opt.Namespace, "absent-task"); err == nil {
					t.Error("reconnected after losing the established connection")
				}
			}
			if requests.Load() != 1 {
				t.Fatalf("requests after connection loss: %d, want probe only", requests.Load())
			}
		})
	}
}

func TestOrkaForwardLossCancelsActiveResult(t *testing.T) {
	a, opt, _, _, dir := orkaCreateFixture(t, "")
	started, cancelled := make(chan struct{}), make(chan struct{})
	orkaResultServer(t, &opt, func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(cancelled)
	})
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	session, err := a.openOrkaResultSession(ctx, opt)
	if err != nil {
		t.Fatal(err)
	}
	defer session.close()
	done := make(chan error, 1)
	go func() {
		_, _, err := session.get(ctx, opt.Namespace, "absent-task")
		done <- err
	}()
	finished := false
	defer func() {
		cancel()
		if !finished {
			<-done
		}
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("request did not start")
	}
	// This fake forward's process exits while the HTTP peer stays alive: the
	// lifetime watcher, not a server disconnect, must cancel the request.
	session.forward.Close()
	select {
	case err := <-done:
		finished = true
		if err == nil {
			t.Fatal("active request succeeded after forward loss")
		}
	case <-time.After(time.Second):
		t.Fatal("forward loss did not cancel the active request")
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("active HTTP connection was not closed")
	}
	assertOrkaForwardExited(t, dir)
}

func TestOrkaResultSessionEstablishesAndReusesOneConnection(t *testing.T) {
	a, opt, _, _, _ := orkaCreateFixture(t, "")
	connected := make(chan struct{}, 3)
	var connections, requests atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":{"code":404,"message":"task not found"}}`)
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
			connected <- struct{}{}
		}
	}
	server.Start()
	defer server.Close()
	_, opt.ResultPort, _ = net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	opt.ResultServiceAccount = "reader"
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	session, err := a.openOrkaResultSession(ctx, opt)
	if err != nil {
		t.Fatal(err)
	}
	defer session.close()
	select {
	case <-connected:
	case <-time.After(time.Second):
		t.Fatal("session exposed before establishing its TCP connection")
	}
	if requests.Load() != 0 {
		t.Fatal("opening the TCP connection sent an authenticated request")
	}
	for range 3 {
		if err := session.probe(ctx, opt.Namespace, "absent-task"); err != nil {
			t.Fatal(err)
		}
	}
	if connections.Load() != 1 || requests.Load() != 3 {
		t.Fatalf("connections=%d requests=%d", connections.Load(), requests.Load())
	}
}

func TestOrkaResultPreflightOnlyAcceptsExactAbsentTask(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"unauthenticated", 401, `{"error":{"code":401,"message":"denied"}}`},
		{"unauthorized", 403, `{"error":{"code":403,"message":"denied"}}`},
		{"no-result", 404, `{"error":{"code":404,"message":"task has no result"}}`},
		{"unknown", 404, `{"error":{"code":404,"message":"result not found"}}`},
		{"wrong-code", 404, `{"error":{"code":403,"message":"task not found"}}`},
		{"existing-result", 200, `{"result":"answer"}`},
		{"html", 200, `<html>answer</html>`},
		{"malformed", 404, `{bad`},
		{"trailing", 404, `{"error":{"code":404,"message":"task not found"}} {}`},
		{"redirect", 302, `{}`},
		{"oversized", 404, strings.Repeat(" ", (1<<20)+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, opt, out, diagnostics, dir := orkaCreateFixture(t, "")
			var calls atomic.Int32
			orkaResultServer(t, &opt, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Header.Get("Authorization") != "Bearer "+orkaTestToken() {
					t.Error("wrong authorization")
				}
				if r.Method != "GET" || !strings.HasPrefix(r.URL.Path, "/api/v1/tasks/sample-") || !strings.HasSuffix(r.URL.Path, "/result") || r.URL.Query().Get("namespace") != "orka-system" {
					t.Errorf("wrong route: %s", r.URL)
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Location", "http://example.invalid/credential-sink")
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			})
			err := a.CreateAgent(opt)
			if err == nil {
				t.Fatal("accepted invalid probe")
			}
			if calls.Load() != 1 {
				t.Fatalf("probe not performed exactly once: %d", calls.Load())
			}
			if _, e := os.Stat(opt.Out); !os.IsNotExist(e) {
				t.Fatal("preflight emitted artifact")
			}
			for _, call := range orkaCalls(t, dir) {
				if call.Document != nil && !slices.Contains(call.Args, "--dry-run=server") {
					t.Fatalf("write after failed probe: %+v", call)
				}
			}
			if strings.Contains(fmt.Sprint(err)+out.String()+diagnostics.String(), orkaTestToken()) {
				t.Fatal("token leaked")
			}
		})
	}
}

func TestOrkaTaskAnswerUsesOneFreshNameAndReadChecks(t *testing.T) {
	a, opt, out, diagnostics, dir := orkaCreateFixture(t, "")
	var probes atomic.Int32
	var name string
	orkaResultServer(t, &opt, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer "+orkaTestToken() {
			t.Error("wrong authorization")
		}
		task := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/tasks/"), "/result")
		if probes.Add(1) == 1 {
			name = task
			if _, err := os.Stat(filepath.Join(dir, task+"-tasks.core.orka.ai.json")); !os.IsNotExist(err) {
				t.Error("task created before probe")
			}
			w.WriteHeader(404)
			fmt.Fprint(w, `{"error":{"code":404,"message":"task not found"}}`)
			return
		}
		if task != name {
			t.Error("task name changed")
		}
		if _, err := os.Stat(filepath.Join(dir, task+"-tasks.core.orka.ai.json")); err != nil {
			t.Error("retrieval before execution")
		}
		fmt.Fprint(w, `{"result":"hello\u001b[2J world"}`)
	})
	if err := a.CreateAgent(opt); err != nil {
		t.Fatal(err)
	}
	assertOrkaForwardExited(t, dir)
	if out.String() != "hello world\n" {
		t.Fatalf("answer not inert: %q", out.String())
	}
	var taskWrites, taskReads int
	for _, c := range orkaCalls(t, dir) {
		if c.Document != nil && c.Document["kind"] == "Task" && !slices.Contains(c.Args, "--dry-run=server") {
			taskWrites++
			if c.Document["metadata"].(map[string]any)["name"] != name {
				t.Fatal("created another Task name")
			}
		}
		if slices.Contains(c.Args, "get") && !slices.Contains(c.Args, "crd") && slices.Contains(c.Args, "tasks.core.orka.ai") && slices.Contains(c.Args, "json") {
			taskReads++
		}
		data, _ := json.Marshal(c)
		if strings.Contains(string(data), orkaTestToken()) {
			t.Fatal("token entered argv or document")
		}
	}
	if taskWrites != 1 || taskReads < 3 {
		t.Fatalf("writes=%d reads=%d", taskWrites, taskReads)
	}
	body, err := os.ReadFile(opt.Out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), name) || strings.Contains(string(body)+out.String()+diagnostics.String(), orkaTestToken()) {
		t.Fatal("name drift or credential leak in artifact/output")
	}
}

func TestOrkaResultRefusesSanitizedCredentialsAndBlankAnswers(t *testing.T) {
	token := orkaTestToken()
	for _, tc := range []struct {
		name, answer, want string
	}{
		{"raw-token", token, "credential material"},
		{"raw-token-hidden-by-ANSI", "\x1b]0;" + token + "\ahello", "credential material"},
		{"NUL-interleaving", token[:8] + "\x00" + token[8:], "credential material"},
		{"ANSI-interleaving", token[:8] + "\x1b[31m" + token[8:], "credential material"},
		{"sanitized-blank", "\x00\x1b[2J\t\n", "no printable answer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, opt, out, diagnostics, dir := orkaCreateFixture(t, "")
			var calls atomic.Int32
			orkaResultServer(t, &opt, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if calls.Add(1) == 1 {
					w.WriteHeader(http.StatusNotFound)
					fmt.Fprint(w, `{"error":{"code":404,"message":"task not found"}}`)
					return
				}
				if err := json.NewEncoder(w).Encode(map[string]string{"result": tc.answer}); err != nil {
					t.Error(err)
				}
			})
			err := a.CreateAgent(opt)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("unsafe result was not refused with expected reason")
			}
			if out.Len() != 0 || strings.Contains(fmt.Sprint(err)+diagnostics.String(), token) || strings.Contains(fmt.Sprint(err)+diagnostics.String(), tc.answer) {
				t.Error("unsafe answer or credential material escaped")
			}
			assertOrkaForwardExited(t, dir)
		})
	}
}

func TestOrkaTaskControllerRoundTripKeepsCreatedGeneration(t *testing.T) {
	a, opt, out, _, dir := orkaCreateFixture(t, "task-resources-roundtrip")
	var calls atomic.Int32
	orkaResultServer(t, &opt, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"error":{"code":404,"message":"task not found"}}`)
			return
		}
		fmt.Fprint(w, `{"result":"hello from Orka"}`)
	})
	if err := a.CreateAgent(opt); err != nil {
		t.Fatal(err)
	}
	if out.String() != "hello from Orka\n" {
		t.Fatalf("answer not retrieved: %q", out.String())
	}
	writes := 0
	for _, call := range orkaCalls(t, dir) {
		if call.Document != nil && call.Document["kind"] == "Task" && !slices.Contains(call.Args, "--dry-run=server") {
			writes++
		}
	}
	if writes != 1 {
		t.Fatalf("Task executions = %d, want exactly one", writes)
	}
	assertOrkaForwardExited(t, dir)
}

func TestOrkaTokenAndForwardFailuresStopBeforeArtifact(t *testing.T) {
	for _, scenario := range []string{"token-fail", "short-token", "forward-fail"} {
		t.Run(scenario, func(t *testing.T) {
			a, opt, out, diagnostics, dir := orkaCreateFixture(t, scenario)
			var calls atomic.Int32
			orkaResultServer(t, &opt, func(http.ResponseWriter, *http.Request) { calls.Add(1) })
			err := a.CreateAgent(opt)
			if err == nil {
				t.Fatal("expected refusal")
			}
			if calls.Load() != 0 {
				t.Fatal("sent bearer before proven bind or valid expiry")
			}
			if _, e := os.Stat(opt.Out); !os.IsNotExist(e) {
				t.Fatal("emitted artifact")
			}
			for _, c := range orkaCalls(t, dir) {
				if c.Document != nil && !slices.Contains(c.Args, "--dry-run=server") {
					t.Fatal("mutated resources")
				}
			}
			if strings.Contains(fmt.Sprint(err)+out.String()+diagnostics.String(), orkaTestToken()) {
				t.Fatal("token leaked in failure")
			}
		})
	}
}

func TestOrkaTaskFailuresNeverRetryExecution(t *testing.T) {
	for _, scenario := range []string{"failed-task", "ambiguous-task"} {
		t.Run(scenario, func(t *testing.T) {
			a, opt, _, _, dir := orkaCreateFixture(t, scenario)
			orkaResultServer(t, &opt, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(404)
				fmt.Fprint(w, `{"error":{"code":404,"message":"task not found"}}`)
			})
			err := a.CreateAgent(opt)
			if err == nil {
				t.Fatal("accepted Task failure")
			}
			writes := 0
			for _, c := range orkaCalls(t, dir) {
				if c.Document != nil && c.Document["kind"] == "Task" && !slices.Contains(c.Args, "--dry-run=server") {
					writes++
				}
			}
			if writes != 1 {
				t.Fatalf("Task writes=%d", writes)
			}
			if !strings.Contains(err.Error(), "sample-") {
				t.Fatal("failure omitted known Task name")
			}
		})
	}
}

func TestOrkaResultReadFailuresAreBoundedAndDoNotResubmit(t *testing.T) {
	for _, body := range []string{`{"result":42}`, `{"result":""}`, `{"result":"   "}`, `{"error":{"code":404,"message":"result not found"}}`} {
		t.Run(body, func(t *testing.T) {
			a, opt, _, _, dir := orkaCreateFixture(t, "")
			var calls atomic.Int32
			orkaResultServer(t, &opt, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if calls.Add(1) == 1 {
					w.WriteHeader(404)
					fmt.Fprint(w, `{"error":{"code":404,"message":"task not found"}}`)
					return
				}
				if strings.Contains(body, "error") {
					w.WriteHeader(404)
				}
				fmt.Fprint(w, body)
			})
			if err := validateOrkaResultOptions(&opt); err != nil {
				t.Fatal(err)
			}
			bundle, err := createOrkaBundle(opt)
			if err != nil {
				t.Fatal(err)
			}
			// Reach the actual result boundary even when the real preflight
			// subprocesses are race-instrumented. The Task-write assertion below
			// still refuses a timeout that happened before execution.
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			err = a.createOrkaOnline(ctx, opt, bundle)
			if err == nil {
				t.Fatal("invalid result accepted")
			}
			writes := 0
			for _, c := range orkaCalls(t, dir) {
				if c.Document != nil && c.Document["kind"] == "Task" && !slices.Contains(c.Args, "--dry-run=server") {
					writes++
				}
			}
			if writes != 1 {
				t.Fatalf("execution retried: %d", writes)
			}
		})
	}
}
