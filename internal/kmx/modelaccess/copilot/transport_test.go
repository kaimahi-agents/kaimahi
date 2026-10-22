package copilot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
)

const catalog = `{"data":[{"id":"gpt-test","capabilities":{"type":"chat"},"policy":{"state":"enabled"},"supported_endpoints":["/chat/completions"]},{"id":"disabled","capabilities":{"type":"chat"},"policy":{"state":"disabled"},"supported_endpoints":["/chat/completions"]},{"id":"embedding","capabilities":{"type":"embeddings"},"policy":{"state":"enabled"},"supported_endpoints":["/chat/completions"]},{"id":"responses-only","capabilities":{"type":"chat"},"policy":{"state":"enabled"},"supported_endpoints":["/responses"]},{"id":"unknown-policy","capabilities":{"type":"chat"},"supported_endpoints":["/chat/completions"]}]}`

func selected() modelaccess.Route {
	return modelaccess.Route{Provider: "copilot", Model: "gpt-test", Endpoint: "https://api.githubcopilot.com", Auth: "copilot"}
}
func chatRequest(t *testing.T, body string) *http.Request {
	t.Helper()
	r, err := http.NewRequestWithContext(context.Background(), "POST", "https://api.githubcopilot.com/chat/completions", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func TestDirectCatalogAndJSONSSETransport(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			sends := 0
			p, s := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer "+canary {
					t.Error("not direct bearer")
				}
				for k, v := range map[string]string{"Editor-Version": "vscode/1.95.0", "Editor-Plugin-Version": "copilot-chat/0.26.7", "User-Agent": "GitHubCopilotChat/0.26.7", "Copilot-Integration-Id": "copilot-developer-cli", "X-Github-Api-Version": "2026-08-20"} {
					if r.Header.Get(k) != v {
						t.Errorf("missing header %s", k)
					}
				}
				switch r.URL.Path {
				case "/models":
					if r.Method != "GET" || r.Header.Get("Openai-Intent") != "" || r.Header.Get("Accept") != "application/json" {
						t.Error("catalog request")
					}
					io.WriteString(w, catalog)
				case "/chat/completions":
					sends++
					b, _ := io.ReadAll(r.Body)
					if string(b) != fmt.Sprintf(`{"model":"gpt-test","messages":[{"role":"user","content":"hello"}],"stream":%t}`, stream) {
						t.Error("body changed")
					}
					if r.Header.Get("Openai-Intent") != "conversation-panel" {
						t.Error("intent")
					}
					wantAccept := "application/json"
					if stream {
						wantAccept = "text/event-stream"
					}
					if r.Header.Get("Accept") != wantAccept {
						t.Errorf("caller Accept changed: got %q want %q", r.Header.Get("Accept"), wantAccept)
					}
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						io.WriteString(w, "data: {\"choices\":[]}\n\ndata: [DONE]\n\n")
					} else {
						io.WriteString(w, `{"choices":[]}`)
					}
				default:
					t.Errorf("unexpected endpoint %s", r.URL.Path)
				}
			})
			s.token = canary
			routes, err := p.List(context.Background())
			if err != nil || len(routes) != 1 || routes[0] != selected() {
				t.Fatalf("catalog: %v %v", routes, err)
			}
			safe(t, routes)
			rt, err := p.Client(context.Background(), routes[0])
			if err != nil {
				t.Fatal(err)
			}
			req := chatRequest(t, fmt.Sprintf(`{"model":"gpt-test","messages":[{"role":"user","content":"hello"}],"stream":%t}`, stream))
			req.Header.Set("Accept", "application/json")
			if stream {
				req.Header.Set("Accept", "text/event-stream")
			}
			resp, err := rt.RoundTrip(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			b, err := io.ReadAll(resp.Body)
			if err != nil || len(b) == 0 || sends != 1 {
				t.Fatalf("chat: %v sends=%d", err, sends)
			}
			if stream && !strings.Contains(string(b), "[DONE]") {
				t.Fatal("SSE not preserved")
			}
			if req.Header.Get("Authorization") != "" || resp.Request.Header.Get("Authorization") != "" {
				t.Fatal("mutated caller request")
			}
			if err := p.Logout(); err != nil {
				t.Fatal(err)
			}
			_, err = rt.RoundTrip(chatRequest(t, `{"model":"gpt-test"}`))
			if !errors.Is(err, ErrAuthRequired) || sends != 1 {
				t.Fatalf("logout did not revoke retained transport: %v", err)
			}
		})
	}
}
func TestExchangeOnlyForCredentialRefusalAndRefresh(t *testing.T) {
	now := time.Unix(1900000000, 0)
	exchange := 0
	direct := 0
	chat := 0
	p, s := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/exchange":
			exchange++
			if r.Header.Get("Authorization") != "token "+canary {
				t.Error("exchange source")
			}
			fmt.Fprintf(w, `{"token":"short-%d","expires_at":%d}`, exchange, now.Add(2*time.Minute).Unix())
		case "/models":
			if r.Header.Get("Authorization") == "Bearer "+canary {
				direct++
				w.WriteHeader(401)
				fmt.Fprintf(w, `{"error":{"message":%q}}`, canary)
			} else {
				io.WriteString(w, catalog)
			}
		case "/chat/completions":
			chat++
			if r.Header.Get("Authorization") != fmt.Sprintf("Bearer short-%d", exchange) {
				t.Error("stale bearer")
			}
			io.WriteString(w, `{"choices":[]}`)
		}
	})
	s.token = canary
	p.now = func() time.Time { return now }
	rt, err := p.Client(context.Background(), selected())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		resp, err := rt.RoundTrip(chatRequest(t, `{"model":"gpt-test"}`))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		now = now.Add(100 * time.Second)
	}
	if exchange != 2 || direct != 1 || chat != 2 || s.token != canary {
		t.Fatalf("exchange=%d direct=%d chat=%d", exchange, direct, chat)
	}
}
func TestCatalogRefusalClassificationAndNoInferenceReplay(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		status     int
		want       error
		exchange   bool
	}{
		{"subscription", "no_copilot_subscription", 403, ErrSubscription, false}, {"policy", "policy_denied", 403, ErrPolicy, false}, {"quota", "quota_exceeded", 403, ErrQuota, false}, {"rate", "", 429, ErrQuota, false}, {"unknown403", "", 403, ErrPolicy, false}, {"unavailable", "", 404, ErrModelUnavailable, false}, {"subscription401", "no_copilot_subscription", 401, ErrSubscription, false}, {"quota401", "quota_exceeded", 401, ErrQuota, false}, {"policy401", "policy_denied", 401, ErrPolicy, false}, {"unsupported", "unsupported_token", 403, ErrAuthRevoked, true}, {"revoked", "", 401, ErrAuthRevoked, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exchanges := 0
			p, s := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/exchange" {
					exchanges++
					w.WriteHeader(401)
					fmt.Fprintf(w, `{"error_details":%q}`, canary)
					return
				}
				w.WriteHeader(tc.status)
				fmt.Fprintf(w, `{"error":{"code":%q,"message":%q}}`, tc.code, canary)
			})
			s.token = canary
			_, err := p.List(context.Background())
			if !errors.Is(err, tc.want) || ((exchanges == 1) != tc.exchange) {
				t.Fatalf("err=%v exchanges=%d", err, exchanges)
			}
			safe(t, err)
		})
	}
	for _, status := range []int{401, 403, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			sends := 0
			exchanges := 0
			p, s := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/models":
					io.WriteString(w, catalog)
				case "/exchange":
					exchanges++
				case "/chat/completions":
					sends++
					w.WriteHeader(status)
					fmt.Fprintf(w, `{"error":{"code":"unsupported_token","message":%q}}`, canary)
				}
			})
			s.token = canary
			rt, err := p.Client(context.Background(), selected())
			if err != nil {
				t.Fatal(err)
			}
			resp, err := rt.RoundTrip(chatRequest(t, `{"model":"gpt-test"}`))
			if err == nil || resp != nil || sends != 1 || exchanges != 0 {
				t.Fatalf("err=%v sends=%d exchanges=%d", err, sends, exchanges)
			}
			safe(t, err)
		})
	}
}
func TestMalformedCatalogAndExactSelection(t *testing.T) {
	for _, body := range []string{`{}`, `{"data":null}`, strings.ReplaceAll(catalog, "gpt-test", canary), `{"data":[{"id":"bad\nname","capabilities":{"type":"chat"},"policy":{"state":"enabled"},"supported_endpoints":["/chat/completions"]}]}`, catalog + `{}`, strings.Repeat("x", (1<<20)+1)} {
		p, s := fixture(t, func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, body) })
		s.token = canary
		_, err := p.List(context.Background())
		if !errors.Is(err, ErrMalformed) {
			t.Fatalf("malformed catalog accepted: %v", err)
		}
		safe(t, err)
	}
	p, s := fixture(t, func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, catalog) })
	s.token = canary
	for _, mutate := range []func(*modelaccess.Route){func(r *modelaccess.Route) { r.Model = "disabled" }, func(r *modelaccess.Route) { r.Model = "missing" }, func(r *modelaccess.Route) { r.Provider = "fake" }, func(r *modelaccess.Route) { r.Endpoint = "https://user:" + canary + "@evil.test" }, func(r *modelaccess.Route) { r.Auth = "none" }} {
		r := selected()
		mutate(&r)
		_, err := p.Client(context.Background(), r)
		if !errors.Is(err, ErrModelUnavailable) {
			t.Fatalf("route accepted: %v", err)
		}
		safe(t, err)
	}
}
func TestTransportRejectsChangedDestinationAndModelBeforeAuth(t *testing.T) {
	sends := 0
	p, s := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			sends++
		}
		io.WriteString(w, catalog)
	})
	s.token = canary
	rt, err := p.Client(context.Background(), selected())
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"https://evil.test/chat/completions", "https://user:" + canary + "@api.githubcopilot.com/chat/completions", "https://api.githubcopilot.com/models", "https://api.githubcopilot.com/chat/completions?x=" + canary} {
		req, _ := http.NewRequest("POST", endpoint, strings.NewReader(`{"model":"gpt-test"}`))
		_, err := rt.RoundTrip(req)
		if err == nil {
			t.Fatal("changed destination accepted")
		}
		safe(t, err)
	}
	for _, body := range []string{`{"model":"other"}`, `{"model":"other","MODEL":"gpt-test"}`, `{"model":"other","model":"gpt-test"}`, `{"model":"gpt-test","model":"gpt-test"}`, `{}`, `{`, strings.Repeat("x", (4<<20)+1)} {
		_, err := rt.RoundTrip(chatRequest(t, body))
		if err == nil {
			t.Fatal("changed model/body accepted")
		}
		safe(t, err)
	}
	if sends != 0 {
		t.Fatal("refusal sent inference")
	}
}
func TestRedirectAndNetworkErrorsNeverLeak(t *testing.T) {
	for _, status := range []int{302, 307} {
		p, s := fixture(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", "https://user:"+canary+"@evil.test/")
			w.WriteHeader(status)
		})
		s.token = canary
		_, err := p.List(context.Background())
		if err == nil {
			t.Fatal("redirect accepted")
		}
		safe(t, err)
	}
	p := New(&memoryStore{token: canary})
	p.http = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New(canary) })}
	_, err := p.List(context.Background())
	if err == nil {
		t.Fatal("network error accepted")
	}
	safe(t, err)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestConcurrentCatalogAndLogout(t *testing.T) {
	var sends atomic.Int32
	p, s := fixture(t, func(w http.ResponseWriter, _ *http.Request) { sends.Add(1); io.WriteString(w, catalog) })
	s.token = canary
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			routes, err := p.List(context.Background())
			if err != nil && !errors.Is(err, ErrAuthRequired) {
				t.Error(err)
			}
			b, _ := json.Marshal(routes)
			safe(t, string(b))
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := p.Logout(); err != nil {
			t.Error(err)
		}
	}()
	wg.Wait()
	before := sends.Load()
	if _, err := p.List(context.Background()); !errors.Is(err, ErrAuthRequired) || sends.Load() != before {
		t.Fatalf("logout: %v", err)
	}
}
