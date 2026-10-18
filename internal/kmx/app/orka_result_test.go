package app

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
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
	opt.OrkaAPIService = "orka-api" // transport tests select a known service; discovery is tested separately
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
			opt.OrkaAPIService = "orka-api"
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
	a, opt, _, diagnostics, _ := orkaCreateFixture(t, "")
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
	opt.OrkaAPIService = "orka-api"
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	session, err := a.openOrkaResultSession(ctx, opt)
	if err != nil {
		t.Fatal(err)
	}
	defer session.close()
	if notice := diagnostics.String(); notice != "Result access uses ServiceAccount \"reader\"; authority details: docs/orka.md#runtime-and-result-access\n" {
		t.Fatalf("result authority notice is not concise and actionable: %q", notice)
	}
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
