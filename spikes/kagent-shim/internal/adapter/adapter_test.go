package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const toolPath = "/tools/shim-012345abcdef"

type observedRequest struct {
	Method, RPC, Session, Version, Authorization string
}

type upstream struct {
	*httptest.Server
	mu       sync.Mutex
	requests []observedRequest
}

func newUpstream(t *testing.T, jsonResponse, stateless bool, tool mcp.ToolHandler, intercept func(http.ResponseWriter, *http.Request, string) bool) *upstream {
	t.Helper()
	s := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	s.AddTool(&mcp.Tool{Name: "k8s_get_resources", InputSchema: map[string]any{"type": "object"}}, tool)
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, &mcp.StreamableHTTPOptions{JSONResponse: jsonResponse, Stateless: stateless})
	u := &upstream{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return
		}
		r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(body))
		var msg struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(body, &msg)
		u.mu.Lock()
		u.requests = append(u.requests, observedRequest{r.Method, msg.Method, r.Header.Get("Mcp-Session-Id"), r.Header.Get("Mcp-Protocol-Version"), r.Header.Get("Authorization")})
		u.mu.Unlock()
		if intercept != nil && intercept(w, r, msg.Method) {
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(u.Close)
	return u
}

func (u *upstream) snapshot() []observedRequest {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]observedRequest(nil), u.requests...)
}

func fixtureAdapter(t *testing.T, url string, headers string) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "routes.json")
	config := fmt.Sprintf(`{"tools":{"shim-012345abcdef":{"url":%q,"name":"k8s_get_resources","headers":%s}}}`, url, headers)
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	secrets := filepath.Join(dir, "secrets")
	if err := os.MkdirAll(secrets, 0700); err != nil {
		t.Fatal(err)
	}
	a, err := New(configPath, secrets)
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewServer(a)
	t.Cleanup(s.Close)
	return s, secrets
}

func request(t *testing.T, s *httptest.Server, method, path, body string) (int, string) {
	t.Helper()
	r, err := http.NewRequest(method, s.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/json")
	resp, err := s.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(data)
}

func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

func TestInvocationNegotiatesMapsArgumentsAndPreservesResult(t *testing.T) {
	for _, stateless := range []bool{false, true} {
		for _, jsonResponse := range []bool{false, true} {
			t.Run(fmt.Sprintf("stateless=%v/json=%v", stateless, jsonResponse), func(t *testing.T) {
				var calls atomic.Int32
				u := newUpstream(t, jsonResponse, stateless, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
					calls.Add(1)
					if r.Params.Name != "k8s_get_resources" {
						t.Errorf("remote name = %q", r.Params.Name)
					}
					if string(r.Params.Arguments) != `{"namespace":"demo","count":9007199254740993,"nested":{"enabled":true}}` {
						t.Errorf("arguments = %s", r.Params.Arguments)
					}
					result := textResult(`{"items":["pod-one"]}`)
					result.StructuredContent = map[string]any{"items": []string{"pod-one"}}
					return result, nil
				}, nil)
				s, _ := fixtureAdapter(t, u.URL, `{}`)
				status, body := request(t, s, "POST", toolPath, `{"namespace":"demo","count":9007199254740993,"nested":{"enabled":true}}`)
				if status != 200 {
					t.Fatalf("status = %d, body = %s", status, body)
				}
				var got struct {
					Content           []struct{ Type, Text string }
					StructuredContent map[string][]string
					Meta              map[string]any `json:"_meta"`
				}
				if err := json.Unmarshal([]byte(body), &got); err != nil {
					t.Fatal(err)
				}
				if len(got.Content) != 1 || got.Content[0].Type != "text" || got.Content[0].Text != `{"items":["pod-one"]}` || len(got.StructuredContent["items"]) != 1 || len(got.Meta) != 0 {
					t.Fatalf("result = %s", body)
				}
				if calls.Load() != 1 {
					t.Fatalf("calls = %d", calls.Load())
				}
				var initialized, closed, negotiated bool
				for _, r := range u.snapshot() {
					initialized = initialized || r.RPC == "notifications/initialized"
					closed = closed || r.Method == "DELETE"
					if r.RPC == "tools/call" {
						negotiated = r.Version != ""
						if !stateless && r.Session == "" {
							t.Error("missing negotiated session")
						}
					}
				}
				if !negotiated || (!stateless && (!initialized || !closed)) {
					t.Fatalf("missing negotiation/lifecycle: %+v", u.snapshot())
				}
			})
		}
	}
}

func TestRejectsRequestsBeforeAnyUpstreamNetwork(t *testing.T) {
	var calls atomic.Int32
	u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); http.Error(w, "unexpected", 500) }))
	defer u.Close()
	s, _ := fixtureAdapter(t, u.URL, `{}`)
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{
		{"GET", "/healthz", "", 200}, {"HEAD", toolPath, "", 200},
		{"GET", toolPath, "", 405}, {"PUT", toolPath, `{}`, 405},
		{"POST", "/tools/shim-deadbeef", `{}`, 404}, {"HEAD", "/tools/shim-deadbeef", "", 404},
		{"POST", toolPath + "/", `{}`, 404}, {"POST", "/other", `{}`, 404},
		{"POST", "/tools/%73him-012345abcdef", `{}`, 404},
		{"POST", toolPath + "?url=http://elsewhere", `{}`, 404},
		{"POST", toolPath, `[]`, 400}, {"POST", toolPath, `null`, 400},
		{"POST", toolPath, `"text"`, 400}, {"POST", toolPath, `true`, 400},
		{"POST", toolPath, ``, 400}, {"POST", toolPath, `{"broken":`, 400},
		{"POST", toolPath, `{"a":1,"a":2}`, 400},
		{"POST", toolPath, `{"nested":[{"a":1,"\u0061":2}]}`, 400},
		{"POST", toolPath, `{} {}`, 400}, {"POST", toolPath, `{} garbage`, 400},
		{"POST", toolPath, `{"big":"` + strings.Repeat("a", 1<<20) + `"}`, 413},
	} {
		status, body := request(t, s, tc.method, tc.path, tc.body)
		if status != tc.status {
			t.Errorf("%s %s body %.60s: status %d, want %d: %s", tc.method, tc.path, tc.body, status, tc.status, body)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid/health requests made %d network calls", calls.Load())
	}
}

func TestSecretsRotateWithoutBearerRewriting(t *testing.T) {
	u := newUpstream(t, true, false, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) { return textResult("ok"), nil }, nil)
	s, dir := fixtureAdapter(t, u.URL, `{"Authorization":{"name":"upstream-auth","key":"token"}}`)
	if err := os.Mkdir(filepath.Join(dir, "upstream-auth"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"opaque-secret", "Bearer rotated-secret"} {
		if err := os.WriteFile(filepath.Join(dir, "upstream-auth", "token"), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		before := len(u.snapshot())
		status, body := request(t, s, "POST", toolPath, `{}`)
		if status != 200 {
			t.Fatalf("status %d: %s", status, body)
		}
		for _, r := range u.snapshot()[before:] {
			if r.Authorization != value {
				t.Errorf("%s %s authorization = %q, want exact file value", r.Method, r.RPC, r.Authorization)
			}
		}
	}
}

func TestErrorsAndUnsupportedContentAreGenericAndNeverRetried(t *testing.T) {
	const secret = "sensitive-upstream-error-sentinel"
	for _, mode := range []string{"protocol", "isError", "image", "meta", "text-meta", "audience", "large-integer", "http", "malformed", "disconnect", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			u := newUpstream(t, true, false, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				switch mode {
				case "protocol":
					return nil, errors.New(secret)
				case "isError":
					result := textResult(secret)
					result.IsError = true
					return result, nil
				case "image":
					return &mcp.CallToolResult{Content: []mcp.Content{&mcp.ImageContent{Data: []byte(secret), MIMEType: "image/png"}}}, nil
				case "meta":
					result := textResult("ok")
					result.Meta = mcp.Meta{"private": secret}
					return result, nil
				case "text-meta":
					return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok", Meta: mcp.Meta{"private": secret}}}}, nil
				case "audience":
					return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: secret, Annotations: &mcp.Annotations{Audience: []mcp.Role{"user"}}}}}, nil
				case "large-integer":
					result := textResult("ok")
					result.StructuredContent = map[string]any{"nested": []any{json.Number("9007199254740993")}}
					return result, nil
				case "oversized":
					return textResult(strings.Repeat("a", 5<<20)), nil
				default:
					return textResult("not reached"), nil
				}
			}, func(w http.ResponseWriter, r *http.Request, method string) bool {
				if method != "tools/call" {
					return false
				}
				switch mode {
				case "http":
					http.Error(w, secret, 503)
				case "malformed":
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, secret)
				case "disconnect":
					conn, _, err := w.(http.Hijacker).Hijack()
					if err == nil {
						conn.Close()
					}
				default:
					return false
				}
				return true
			})
			s, _ := fixtureAdapter(t, u.URL, `{}`)
			status, body := request(t, s, "POST", toolPath, `{}`)
			if status < 400 {
				t.Fatalf("status = %d, body %.100s", status, body)
			}
			if strings.Contains(body, secret) || len(body) > 256 {
				t.Fatalf("leaky/unbounded error: %.300s", body)
			}
			calls := 0
			for _, r := range u.snapshot() {
				if r.RPC == "tools/call" {
					calls++
				}
			}
			if calls != 1 {
				t.Fatalf("tools/call attempts = %d", calls)
			}
		})
	}
}

func TestCallerCancellationReachesMCPTool(t *testing.T) {
	started, cancelled := make(chan struct{}), make(chan struct{})
	u := newUpstream(t, false, false, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		close(started)
		select {
		case <-ctx.Done():
			close(cancelled)
			return nil, ctx.Err()
		case <-time.After(5 * time.Second):
			return nil, errors.New("test timed out")
		}
	}, nil)
	s, _ := fixtureAdapter(t, u.URL, `{}`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, _ := http.NewRequestWithContext(ctx, "POST", s.URL+toolPath, strings.NewReader(`{}`))
	done := make(chan struct{})
	go func() {
		defer close(done)
		resp, _ := s.Client().Do(r)
		if resp != nil {
			resp.Body.Close()
		}
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("tool did not start")
	}
	cancel()
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("MCP tool did not observe cancellation")
	}
	<-done
}
