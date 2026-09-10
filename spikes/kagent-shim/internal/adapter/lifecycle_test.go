package adapter

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestFixedTimeoutCancelsUpstream(t *testing.T) {
	cancelled := make(chan struct{})
	u := newUpstream(t, false, false, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		select {
		case <-ctx.Done():
			close(cancelled)
			return nil, ctx.Err()
		case <-time.After(50 * time.Second):
			return nil, errors.New("adapter did not cancel")
		}
	}, nil)
	s, _ := fixtureAdapter(t, u.URL, `{}`)
	start := time.Now()
	status, body := request(t, s, "POST", toolPath, `{}`)
	elapsed := time.Since(start)
	if status != 504 || elapsed < 44*time.Second || elapsed > 49*time.Second {
		t.Fatalf("timeout: status %d, elapsed %s, body %s", status, elapsed, body)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("upstream was not cancelled")
	}
}

func TestSDKReconnectAndMultiRoundTripRetriesAreDisabled(t *testing.T) {
	for _, mode := range []string{"sse-resume", "load-shedding"} {
		t.Run(mode, func(t *testing.T) {
			u := newUpstream(t, false, true, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{InputRequests: mcp.InputRequestMap{}, RequestState: "retry-state"}, nil
			}, func(w http.ResponseWriter, r *http.Request, method string) bool {
				if mode != "sse-resume" || method != "tools/call" {
					return false
				}
				w.Header().Set("Content-Type", "text/event-stream")
				// Fault injection at the HTTP carrier: an interrupted event stream
				// with an event ID would normally cause SDK GET reconnection.
				io.WriteString(w, "id: interrupted\nretry: 1\ndata: \n\n")
				return true
			})
			s, _ := fixtureAdapter(t, u.URL, `{}`)
			start := time.Now()
			status, body := request(t, s, "POST", toolPath, `{}`)
			if status != 502 || time.Since(start) > 2*time.Second {
				t.Fatalf("status %d, body %s", status, body)
			}
			calls := 0
			for _, req := range u.snapshot() {
				if req.Method == "GET" {
					t.Error("unexpected SSE reconnect GET")
				}
				if req.RPC == "tools/call" {
					calls++
				}
			}
			if calls != 1 {
				t.Fatalf("tools/call attempts %d", calls)
			}
		})
	}
}

func TestRedirectsAndInitializationErrorsNeverExposeSecrets(t *testing.T) {
	const secret = "upstream-secret-diagnostic-sentinel"
	// SDK's nil logger must remain a discard logger, not the process default.
	var logs bytes.Buffer
	oldSlog, oldLog := slog.Default(), log.Writer()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	log.SetOutput(&logs)
	defer func() { slog.SetDefault(oldSlog); log.SetOutput(oldLog) }()
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected.Add(1) }))
	defer target.Close()
	for _, redirect := range []bool{false, true} {
		u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if redirect {
				http.Redirect(w, r, target.URL+"/"+secret, http.StatusTemporaryRedirect)
				return
			}
			http.Error(w, secret, http.StatusUnauthorized)
		}))
		s, _ := fixtureAdapter(t, u.URL, `{}`)
		status, body := request(t, s, "POST", toolPath, `{}`)
		if status != 502 || strings.Contains(body, secret) {
			t.Errorf("status %d, body %s", status, body)
		}
		u.Close()
	}
	if redirected.Load() != 0 {
		t.Fatalf("followed redirects %d times", redirected.Load())
	}
	if strings.Contains(logs.String(), secret) {
		t.Fatal("upstream secret escaped into process logs")
	}
}

func TestRejectedProtocolVersionStillClosesSession(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "invalid-version", Version: "1"}, nil)
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			result, err := next(ctx, method, req)
			if init, ok := result.(*mcp.InitializeResult); ok {
				init.ProtocolVersion = "unsupported-version-sentinel"
			}
			return result, err
		}
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{JSONResponse: true})
	var deletes atomic.Int32
	u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" {
			deletes.Add(1)
		}
		handler.ServeHTTP(w, r)
	}))
	defer u.Close()
	s, _ := fixtureAdapter(t, u.URL, `{}`)
	status, body := request(t, s, "POST", toolPath, `{}`)
	if status != 502 || strings.Contains(body, "unsupported-version-sentinel") {
		t.Fatalf("status %d, body %s", status, body)
	}
	if deletes.Load() != 1 {
		t.Fatalf("session deletions = %d, want 1", deletes.Load())
	}
}

func TestSessionCleanupIsBounded(t *testing.T) {
	u := newUpstream(t, true, false, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) { return textResult("ok"), nil }, func(w http.ResponseWriter, r *http.Request, method string) bool {
		if r.Method != "DELETE" {
			return false
		}
		<-r.Context().Done()
		return true
	})
	s, _ := fixtureAdapter(t, u.URL, `{}`)
	start := time.Now()
	status, body := request(t, s, "POST", toolPath, `{}`)
	if status != 200 || time.Since(start) > 2*time.Second {
		t.Fatalf("status %d, elapsed %s, body %s", status, time.Since(start), body)
	}
}
