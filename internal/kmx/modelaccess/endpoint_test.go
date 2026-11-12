package modelaccess

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestEndpointProtocolsAndCredentialIsolation(t *testing.T) {
	for _, provider := range []string{"openai", "compatible", "ollama", "foundry-local", "anthropic"} {
		t.Run(provider, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var body map[string]any
				if json.NewDecoder(r.Body).Decode(&body) != nil {
					t.Error("request JSON")
				}
				if body["model"] != "unit" || body["stream"] != false {
					t.Errorf("selection %v", body)
				}
				if provider == "anthropic" {
					if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "private-unit-credential" || r.Header.Get("Authorization") != "" || body["system"] != "Be helpful" || body["max_tokens"] != float64(2048) {
						t.Error("Messages request")
					}
					io.WriteString(w, `{"type":"message","role":"assistant","model":"unit","stop_reason":"end_turn","content":[{"type":"thinking","thinking":"private thoughts"},{"type":"text","text":"hello"}]}`)
				} else {
					if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer private-unit-credential" {
						t.Error("chat request")
					}
					io.WriteString(w, `{"model":"unit","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"hello"}}]}`)
				}
			}))
			defer server.Close()
			route := Route{Provider: provider, Model: "unit", Endpoint: server.URL + "/v1", Auth: "env", KeyEnv: "TEST_MODEL_KEY"}
			access, err := NewEndpointAccess(route, "private-unit-credential", 2048)
			if err != nil {
				t.Fatal(err)
			}
			round, err := access.Client(route)
			if err != nil {
				t.Fatal(err)
			}
			req, _ := http.NewRequestWithContext(t.Context(), "POST", route.Endpoint+"/chat/completions", strings.NewReader(`{"model":"unit","stream":false,"messages":[{"role":"system","content":"Be helpful"},{"role":"user","content":"hi"}]}`))
			resp, err := round.RoundTrip(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			raw, _ := io.ReadAll(resp.Body)
			if calls != 1 || !strings.Contains(string(raw), "hello") || strings.Contains(string(raw), "private") {
				t.Fatalf("calls=%d response=%s", calls, raw)
			}
			if access.CheckContent("private-unit-credential") == nil {
				t.Fatal("credential echoed")
			}
		})
	}
}

func TestEndpointAccessReusesConnectionsWithRequestScopedKeys(t *testing.T) {
	var connections atomic.Int32
	var requests atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)
		want := "Bearer first-private-key"
		if n == 2 {
			want = "Bearer second-private-key"
		}
		if r.Header.Get("Authorization") != want {
			t.Error("connection reused another invocation's credential")
		}
		io.WriteString(w, `{"model":"unit","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"hello"}}]}`)
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	server.Start()
	defer server.Close()
	route := Route{Provider: "openai", Model: "unit", Endpoint: server.URL + "/v1", Auth: "env", KeyEnv: "TEST_MODEL_KEY"}
	for _, key := range []string{"first-private-key", "second-private-key"} {
		access, err := NewEndpointAccess(route, key, 1024)
		if err != nil {
			t.Fatal(err)
		}
		req, _ := http.NewRequestWithContext(t.Context(), "POST", route.Endpoint+"/chat/completions", strings.NewReader(`{"model":"unit","stream":false,"messages":[{"role":"user","content":"hello"}]}`))
		resp, err := access.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	if connections.Load() != 1 || requests.Load() != 2 {
		t.Fatalf("connections=%d requests=%d", connections.Load(), requests.Load())
	}
}

func TestEndpointRefusesRedirectMalformedAndIncompleteWithoutRetry(t *testing.T) {
	for _, reply := range []string{
		`{"type":"message","role":"assistant","model":"unit","stop_reason":"max_tokens","content":[{"type":"text","text":"partial"}]}`,
		`{"type":"message","role":"assistant","model":"wrong","stop_reason":"end_turn","content":[{"type":"text","text":"wrong"}]}`,
		`{"type":"message","role":"assistant","model":"unit","stop_reason":"end_turn","content":[{"type":"tool_use"}]}`,
		`{"type":"message","role":"assistant","model":"unit","stop_reason":"end_turn","content":[{"type":"text","text":"private-unit-credential"}]}`,
		`{"model":"unit","model":"unit"}`,
		"redirect",
	} {
		t.Run(reply, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if reply == "redirect" {
					w.Header().Set("Location", "/other")
					w.WriteHeader(302)
					return
				}
				io.WriteString(w, reply)
			}))
			defer server.Close()
			route := Route{Provider: "anthropic", Model: "unit", Endpoint: server.URL + "/v1", Auth: "env", KeyEnv: "TEST_MODEL_KEY"}
			a, err := NewEndpointAccess(route, "private-unit-credential", 1024)
			if err != nil {
				t.Fatal(err)
			}
			req, _ := http.NewRequestWithContext(t.Context(), "POST", route.Endpoint+"/chat/completions", strings.NewReader(`{"model":"unit","stream":false,"messages":[{"role":"system","content":"hi"},{"role":"user","content":"hi"}]}`))
			if _, err = a.RoundTrip(req); err == nil || strings.Contains(err.Error(), "private-unit-credential") || calls != 1 {
				t.Fatalf("calls %d err %v", calls, err)
			}
		})
	}
}

func TestResolveUsesExactLocalDiscoveryAndHostedDefaults(t *testing.T) {
	called := 0
	discover := func(context.Context) []Report {
		called++
		return []Report{{State: Installed, Models: []Model{{Route: Route{Provider: "ollama", Model: "installed:1", Endpoint: "http://127.0.0.1:11434/v1", Auth: "none"}}}}}
	}
	r, err := Resolve(t.Context(), Route{Provider: "ollama", Model: "installed:1", Auth: "none"}, discover)
	if err != nil || called != 1 || r.Endpoint == "" {
		t.Fatalf("route=%+v err=%v", r, err)
	}
	if _, err = Resolve(t.Context(), Route{Provider: "ollama", Model: "missing", Auth: "none"}, discover); err == nil {
		t.Fatal("silently selected another model")
	}
	r, err = Resolve(t.Context(), Route{Provider: "openai", Model: "unit", Auth: "none"}, discover)
	if err != nil || r.KeyEnv != "OPENAI_API_KEY" || r.Auth != "env" || called != 2 {
		t.Fatalf("route=%+v err=%v", r, err)
	}
	if _, err = NewEndpointAccess(Route{Provider: "compatible", Model: "unit", Endpoint: "http://example.com/v1", Auth: "none"}, "", 0); err == nil {
		t.Fatal("remote plaintext allowed")
	}
}
