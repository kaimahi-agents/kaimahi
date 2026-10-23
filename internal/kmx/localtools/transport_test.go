package localtools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const testMCPCredential = "mcp-credential-canary"
const searchSchema = `{"type":"object","properties":{"query":{"type":"string"},"perPage":{"type":"number","minimum":1,"maximum":100},"page":{"type":"number","minimum":1},"fields":{"type":"array","items":{"type":"string","enum":["number","title","pull_request","repository_url"]}},"sort":{"type":"string","enum":["updated"]},"order":{"type":"string","enum":["desc"]}},"required":["query"]}`

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type mcpFixture struct {
	Transport http.RoundTripper
	Calls     atomic.Int64
	mu        sync.Mutex
	methods   []string
}

func (f *mcpFixture) Methods() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.methods...)
}

func newMCPFixture(t *testing.T, text string, sse ...bool) *mcpFixture {
	t.Helper()
	f := &mcpFixture{}
	server := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	var schema any
	if json.Unmarshal([]byte(searchSchema), &schema) != nil {
		t.Fatal("schema fixture")
	}
	server.AddTool(&mcp.Tool{Name: "search_pull_requests", InputSchema: schema}, func(_ context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		f.Calls.Add(1)
		var args map[string]any
		if json.Unmarshal(r.Params.Arguments, &args) != nil {
			t.Error("arguments missing")
		}
		want := map[string]any{"query": "repo:owner/repo is:pr is:merged", "perPage": float64(20), "page": float64(1), "fields": []any{"number", "title", "pull_request", "repository_url"}, "sort": "updated", "order": "desc"}
		if !reflect.DeepEqual(args, want) {
			t.Error("wrong fixed read arguments")
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{JSONResponse: len(sse) == 0 || !sse[0]})
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testMCPCredential {
			t.Error("credential missing or changed")
			w.WriteHeader(401)
			return
		}
		if r.Method == http.MethodPost {
			raw, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(strings.NewReader(string(raw)))
			var req struct {
				Method string `json:"method"`
				Params struct {
					Protocol     string         `json:"protocolVersion"`
					Capabilities map[string]any `json:"capabilities"`
				} `json:"params"`
			}
			if json.Unmarshal(raw, &req) != nil {
				t.Error("invalid request")
			}
			f.mu.Lock()
			f.methods = append(f.methods, req.Method)
			f.mu.Unlock()
			if req.Method == "initialize" && (req.Params.Protocol != "2025-11-25" || len(req.Params.Capabilities) != 0) {
				t.Error("unqualified handshake/capabilities")
			}
		} else {
			f.mu.Lock()
			f.methods = append(f.methods, r.Method)
			f.mu.Unlock()
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	target, _ := url.Parse(ts.URL)
	base := ts.Client().Transport
	f.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != endpointURL {
			t.Error("origin gate bypassed")
		}
		copy := r.Clone(r.Context())
		copy.URL = target
		copy.Host = target.Host
		return base.RoundTrip(copy)
	})
	return f
}
func executeMCP(ctx context.Context, h *Host) (api.ToolResult, error) {
	return h.Execute(ctx, controller.ToolCallContext{SessionUID: "session"}, api.ToolCall{ID: "call", IdempotencyKey: "execution:call", Tool: Operation, Mediation: api.MediationControllerMediated})
}

func TestMCPHostReadLifecycle(t *testing.T) {
	f := newMCPFixture(t, mergedSearch)
	b := Binding{"owner/repo", endpointURL, "TEST_MCP_TOKEN"}
	host, err := newMCPHost(b, testMCPCredential, f.Transport)
	if err != nil || len(f.Methods()) != 0 {
		t.Fatal("factory performed network IO", err)
	}
	result, err := executeMCP(t.Context(), host)
	if err != nil || result.ID != "call" || f.Calls.Load() != 1 {
		t.Fatal("read did not complete", err)
	}
	if got := f.Methods(); !reflect.DeepEqual(got, []string{"initialize", "notifications/initialized", "tools/list", "tools/call", "DELETE"}) {
		t.Fatal("unexpected protocol sends", got)
	}
	if host.Check(testMCPCredential) == nil {
		t.Fatal("host checker lost credential")
	}
}

func TestMCPHostSDKServerSSE(t *testing.T) {
	f := newMCPFixture(t, mergedSearch, true)
	h, err := newMCPHost(Binding{"owner/repo", endpointURL, "TEST_MCP_TOKEN"}, testMCPCredential, f.Transport)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = executeMCP(t.Context(), h); err != nil || f.Calls.Load() != 1 {
		t.Fatal("SDK SSE response refused", err)
	}
}

func TestMCPHostExplicitEnvAndCancellation(t *testing.T) {
	b := Binding{"owner/repo", endpointURL, "TEST_MCP_TOKEN"}
	t.Setenv("TEST_MCP_TOKEN", "")
	if _, err := NewMCPHost(t.Context(), b); err == nil {
		t.Fatal("empty explicit reference accepted")
	}
	for _, key := range []string{"bad\nheader", "bad header", " bad"} {
		t.Setenv("TEST_MCP_TOKEN", key)
		if _, err := NewMCPHost(t.Context(), b); err == nil {
			t.Fatal("unsafe header accepted")
		}
	}
	t.Setenv("TEST_MCP_TOKEN", testMCPCredential)
	if _, err := NewMCPHost(t.Context(), b); err != nil {
		t.Fatal("valid explicit credential refused", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := NewMCPHost(ctx, b); err == nil {
		t.Fatal("canceled factory accepted")
	}
	f := newMCPFixture(t, mergedSearch)
	h, err := newMCPHost(b, testMCPCredential, f.Transport)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = executeMCP(ctx, h); err == nil || len(f.Methods()) != 0 {
		t.Fatal("canceled read sent")
	}
}

func TestMCPResponseRefusalsNeverRetry(t *testing.T) {
	for name, change := range map[string]func(*http.Response){
		"redirect":  func(r *http.Response) { r.StatusCode = 307; r.Header.Set("Location", "https://elsewhere.invalid/") },
		"auth":      func(r *http.Response) { r.StatusCode = 401 },
		"quota":     func(r *http.Response) { r.StatusCode = 429 },
		"transient": func(r *http.Response) { r.StatusCode = 503 },
		"oversize":  func(r *http.Response) { r.Body = io.NopCloser(strings.NewReader(strings.Repeat("x", 1024*1024+1))) },
		"encoded credential": func(r *http.Response) {
			r.Body = io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":1,"result":{"value":"\u006dcp-credential-canary"}}`))
		},
		"callback": func(r *http.Response) {
			r.Body = io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","method":"sampling/createMessage","id":1,"params":{}}`))
		},
		"duplicate envelope": func(r *http.Response) {
			r.Body = io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":1,"result":{},"RESULT":{}}`))
		},
		"version": func(r *http.Response) {
			raw, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(strings.NewReader(strings.ReplaceAll(string(raw), "2025-11-25", "2025-06-18")))
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newMCPFixture(t, mergedSearch)
			var sends atomic.Int64
			bad := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				sends.Add(1)
				res, err := f.Transport.RoundTrip(r)
				if err == nil && r.Method == http.MethodPost {
					defer res.Body.Close()
					change(res)
				}
				return res, err
			})
			h, err := newMCPHost(Binding{"owner/repo", endpointURL, "TEST_MCP_TOKEN"}, testMCPCredential, bad)
			if err != nil {
				t.Fatal(err)
			}
			_, err = executeMCP(t.Context(), h)
			if err == nil || strings.Contains(err.Error(), testMCPCredential) || f.Calls.Load() != 0 || sends.Load() > 2 {
				t.Fatal("unsafe response accepted or retried")
			}
		})
	}
}

func TestMCPReadDeadlineClosesDetachedSession(t *testing.T) {
	f := newMCPFixture(t, mergedSearch)
	slow := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		raw := []byte{}
		if r.Body != nil {
			raw, _ = io.ReadAll(r.Body)
			r.Body = io.NopCloser(strings.NewReader(string(raw)))
		}
		if strings.Contains(string(raw), `"tools/call"`) {
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
		return f.Transport.RoundTrip(r)
	})
	h, err := newMCPHost(Binding{"owner/repo", endpointURL, "TEST_MCP_TOKEN"}, testMCPCredential, slow)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = executeMCP(ctx, h)
	if err == nil || time.Since(start) > 2*time.Second {
		t.Fatal("SDK detached cleanup escaped caller deadline")
	}
}
