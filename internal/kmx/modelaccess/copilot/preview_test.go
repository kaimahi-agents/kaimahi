package copilot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
)

const previewBearer = "preview_EXCHANGED_PRIVATE_1b43"
const previewBody = `{"model":"gpt-test","messages":[{"role":"user","content":"hello"}],"stream":false}`

func previewAccess(t *testing.T, p *Provider, routes ...modelaccess.Route) modelaccess.PreviewAccess {
	t.Helper()
	if len(routes) == 0 {
		routes = []modelaccess.Route{selected()}
	}
	a, err := p.PreviewClient(t.Context(), routes)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func previewSend(t *testing.T, a modelaccess.PreviewAccess, body string) (*http.Response, error) {
	t.Helper()
	rt, err := a.Client(selected())
	if err != nil {
		return nil, err
	}
	return rt.RoundTrip(chatRequest(t, body))
}
func encoded(s string) string {
	var b strings.Builder
	for _, r := range s {
		fmt.Fprintf(&b, `\u%04x`, r)
	}
	return b.String()
}
func requirePrivate(t *testing.T, values ...any) {
	t.Helper()
	safe(t, values...)
	for _, v := range values {
		if strings.Contains(fmt.Sprint(v), previewBearer) {
			t.Fatal("exchanged credential leaked")
		}
	}
}

// Importing a rotated store credential, or refreshing during a send, breaks this.
func TestPreviewInitialExchangeAndPinnedSnapshot(t *testing.T) {
	now := time.Now()
	exchanges, chats, catalogs := 0, 0, 0
	rotated := false
	p, s := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/exchange":
			exchanges++
			fmt.Fprintf(w, `{"token":%q,"expires_at":%d}`, previewBearer, now.Add(10*time.Minute).Unix())
		case "/models":
			catalogs++
			if r.Header.Get("Authorization") == "Bearer "+canary {
				w.WriteHeader(401)
				io.WriteString(w, `{}`)
				return
			}
			if r.Header.Get("Authorization") != "Bearer "+previewBearer && !(rotated && r.Header.Get("Authorization") == "Bearer rotated-source") {
				t.Error("unexpected catalog credential")
			}
			io.WriteString(w, catalog)
		case "/chat/completions":
			chats++
			if r.Header.Get("Authorization") != "Bearer "+previewBearer {
				t.Error("snapshot was changed")
			}
			if r.Header.Get("Proxy-Authorization") != "" || r.Header.Get("Idempotency-Key") != "" || r.Header.Get("X-Idempotency-Key") != "" {
				t.Error("replay/proxy headers retained")
			}
			w.Header().Set("X-Upstream-Secret", previewBearer)
			io.WriteString(w, `{"choices":[]}`)
		}
	})
	s.token = canary
	a := previewAccess(t, p)
	if exchanges != 1 || chats != 0 {
		t.Fatalf("setup exchanges=%d chats=%d", exchanges, chats)
	}
	for _, secret := range []string{canary, previewBearer} {
		if !errors.Is(a.CheckContent(strings.Repeat("x", 5<<20)+secret), ErrCredentialContent) {
			t.Fatal("plain/long content not checked")
		}
	}
	// Use a new capability after deliberate content refusal.
	a = previewAccess(t, p)
	s.token = "rotated-source"
	rotated = true
	if _, err := p.List(t.Context()); err != nil {
		t.Fatal(err)
	}
	reads := s.reads
	for i := 0; i < 2; i++ {
		resp, err := previewSend(t, a, previewBody)
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		requirePrivate(t, resp.Header, resp.Trailer, resp.Status, resp.Request.Header, string(b))
	}
	if s.reads != reads || exchanges != 1 || chats != 2 {
		t.Fatalf("reads=%d want=%d exchanges=%d chats=%d catalogs=%d", s.reads, reads, exchanges, chats, catalogs)
	}
}

// Resolving routes independently would permit an agent snapshot before judge refusal.
func TestPreviewExactRoutesAndSetupErrorStillChecksContent(t *testing.T) {
	p, s := fixture(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, catalog) })
	s.token = canary
	missing := selected()
	missing.Model = "missing"
	a, err := p.PreviewClient(t.Context(), []modelaccess.Route{selected(), missing})
	if !errors.Is(err, ErrModelUnavailable) || a == nil {
		t.Fatalf("setup result=%v checker=%t", err, a != nil)
	}
	if !errors.Is(a.CheckContent("identity-"+canary), ErrCredentialContent) {
		t.Fatal("setup refusal hid credential checker")
	}
	if a.Validate(t.Context()) == nil {
		t.Fatal("failed setup remained usable")
	}
	p2, s2 := fixture(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, catalog) })
	s2.token = canary
	bad := selected()
	bad.Endpoint = "https://evil.test"
	if _, err := p2.PreviewClient(t.Context(), []modelaccess.Route{bad}); err == nil || s2.reads != 0 {
		t.Fatal("invalid endpoint accessed credentials")
	}
	a = previewAccess(t, p)
	changed := selected()
	changed.Model = "other"
	if _, err := a.Client(changed); !errors.Is(err, ErrModelUnavailable) {
		t.Fatal("unselected route accepted")
	}
}

// Logout/save revokes existing capabilities even if the next token has the same value.
func TestPreviewOwnerInvalidationAndContentAfterInvalidation(t *testing.T) {
	for _, action := range []string{"logout", "save", "failed-logout", "failed-save"} {
		t.Run(action, func(t *testing.T) {
			sends := 0
			p, s := fixture(t, func(w http.ResponseWriter, r *http.Request) { sends++; io.WriteString(w, catalog) })
			s.token = canary
			a := previewAccess(t, p)
			before := sends
			if strings.HasPrefix(action, "failed") {
				s.fail = errors.New(canary)
			}
			if strings.HasSuffix(action, "logout") {
				_ = p.Logout()
			} else {
				p.mu.Lock()
				_ = p.save(canary)
				p.mu.Unlock()
			}
			if !errors.Is(a.Validate(t.Context()), ErrAuthRevoked) {
				t.Fatal("owner change did not invalidate")
			}
			if !errors.Is(a.CheckContent(canary), ErrCredentialContent) {
				t.Fatal("invalid capability stopped content detection")
			}
			if _, err := previewSend(t, a, previewBody); err == nil || sends != before {
				t.Fatal("invalidated capability sent request")
			}
		})
	}
}

// Insufficient known lifetime must exchange at setup once, never in-flight.
func TestPreviewKnownExpirySetupAndInvocationBound(t *testing.T) {
	for _, minutes := range []int{2, 10} {
		t.Run(fmt.Sprint(minutes), func(t *testing.T) {
			now := time.Now()
			exchanges, chats := 0, 0
			p, s := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/exchange":
					exchanges++
					fmt.Fprintf(w, `{"token":%q,"expires_at":%d}`, previewBearer, now.Add(time.Duration(minutes)*time.Minute).Unix())
				case "/models":
					if r.Header.Get("Authorization") == "Bearer "+canary {
						w.WriteHeader(401)
						return
					}
					io.WriteString(w, catalog)
				case "/chat/completions":
					chats++
					io.WriteString(w, `{"choices":[]}`)
				}
			})
			s.token = canary
			p.now = func() time.Time { return now }
			a, err := p.PreviewClient(t.Context(), []modelaccess.Route{selected()})
			if exchanges != 1 {
				t.Fatalf("exchanges=%d", exchanges)
			}
			if minutes == 2 {
				if !errors.Is(err, ErrAuthRevoked) || a == nil {
					t.Fatalf("short setup accepted: %v", err)
				}
				if !errors.Is(a.CheckContent(previewBearer), ErrCredentialContent) {
					t.Fatal("short bearer not protected")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			now = now.Add(5*time.Minute + time.Second)
			if a.Validate(t.Context()) == nil {
				t.Fatal("unbounded invocation")
			}
			if _, err := previewSend(t, a, previewBody); err == nil || chats != 0 || exchanges != 1 {
				t.Fatal("expired invocation sent/refreshed")
			}
		})
	}
	// Reuse an ordinary short-lived bearer only after one setup renewal.
	now := time.Now()
	exchanges := 0
	p, s := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/exchange":
			exchanges++
			fmt.Fprintf(w, `{"token":%q,"expires_at":%d}`, previewBearer, now.Add(10*time.Minute).Unix())
		default:
			io.WriteString(w, catalog)
		}
	})
	s.token = canary
	p.source = canary
	p.bearer = "old-bearer"
	p.legacy = true
	p.expires = now.Add(time.Minute)
	a := previewAccess(t, p)
	if exchanges != 1 || !errors.Is(a.CheckContent(previewBearer), ErrCredentialContent) {
		t.Fatal("short known bearer not renewed at setup")
	}
}

// Every refusal latches only this invocation: no retry, exchange, or later sample.
func TestPreviewAuthAndCatalogRefusalStopOnlyInvocation(t *testing.T) {
	for _, at := range []string{"chat", "catalog"} {
		t.Run(at, func(t *testing.T) {
			catalogs, chats, exchanges := 0, 0, 0
			refuse := false
			p, s := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/models":
					catalogs++
					if at == "catalog" && refuse {
						w.WriteHeader(401)
						io.WriteString(w, `{"error":{"message":"unsafe"}}`)
						return
					}
					io.WriteString(w, catalog)
				case "/exchange":
					exchanges++
				case "/chat/completions":
					chats++
					if refuse {
						w.WriteHeader(401)
						fmt.Fprintf(w, `{"error":{"message":%q}}`, canary)
						return
					}
					io.WriteString(w, `{"choices":[]}`)
				}
			})
			s.token = canary
			a := previewAccess(t, p)
			refuse = true
			_, err := previewSend(t, a, previewBody)
			if !errors.Is(err, ErrAuthRevoked) {
				t.Fatalf("refusal=%v", err)
			}
			requirePrivate(t, err)
			before := catalogs + chats
			if _, err := previewSend(t, a, previewBody); err == nil || catalogs+chats != before || exchanges != 0 {
				t.Fatal("refused preview retried/refreshed")
			}
			refuse = false
			a = previewAccess(t, p)
			resp, err := previewSend(t, a, previewBody)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
		})
	}
}

// Decoding to a map drops duplicate values; scanning raw bytes misses escapes.
func TestPreviewDecodedCredentialContentEveryJSONKeyAndString(t *testing.T) {
	for _, secret := range []string{canary, previewBearer} {
		for _, direction := range []string{"request", "response"} {
			for _, shape := range []string{"text", "key", "metadata", "duplicate"} {
				t.Run(direction+"/"+shape+"/"+secret[:3], func(t *testing.T) {
					echo := `"` + encoded(secret) + `"`
					bad := ""
					switch shape {
					case "text":
						bad = `{"model":"gpt-test","stream":false,"messages":[{"content":` + echo + `}]}`
					case "key":
						bad = `{"model":"gpt-test","stream":false,` + echo + `:0}`
					case "metadata":
						bad = `{"model":"gpt-test","stream":false,"metadata":{"nested":[{"hidden":` + echo + `}]}}`
					case "duplicate":
						bad = `{"model":"gpt-test","stream":false,"metadata":` + echo + `,"metadata":"clean"}`
					}
					now := time.Now()
					chats := 0
					p, s := fixture(t, func(w http.ResponseWriter, r *http.Request) {
						switch r.URL.Path {
						case "/models":
							io.WriteString(w, catalog)
						case "/chat/completions":
							chats++
							if direction == "response" {
								io.WriteString(w, bad)
							} else {
								io.WriteString(w, `{"choices":[]}`)
							}
						}
					})
					s.token = canary
					p.source = canary
					p.bearer = previewBearer
					p.legacy = true
					p.expires = now.Add(10 * time.Minute)
					a := previewAccess(t, p)
					body := previewBody
					if direction == "request" {
						body = bad
					}
					resp, err := previewSend(t, a, body)
					if resp != nil || !errors.Is(err, ErrCredentialContent) {
						t.Fatalf("echo accepted: %v", err)
					}
					if direction == "request" && chats != 0 {
						t.Fatal("unsafe request was sent")
					}
					requirePrivate(t, err)
					if a.Validate(t.Context()) == nil {
						t.Fatal("content refusal did not latch")
					}
				})
			}
		}
	}
}

func TestPreviewRejectsStreamingAmbiguityAndExactRequestModel(t *testing.T) {
	for _, body := range []string{
		`{"model":"other","stream":false}`, `{"model":"gpt-test"}`, `{"model":"gpt-test","stream":true}`, `{"model":"gpt-test","stream":null}`, `{"model":"gpt-test","STREAM":true}`, `{"model":"gpt-test","stream":false,"stream":true}`, `{"model":"gpt-test","MODEL":"other","stream":false}`, `{"model":"gpt-test","stream":false} {}`, `[]`,
	} {
		t.Run(body, func(t *testing.T) {
			chats := 0
			p, s := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/models" {
					chats++
				}
				io.WriteString(w, catalog)
			})
			s.token = canary
			a := previewAccess(t, p)
			resp, err := previewSend(t, a, body)
			if err == nil || resp != nil || chats != 0 {
				t.Fatal("invalid JSON Chat accepted")
			}
			requirePrivate(t, err)
		})
	}
}

func TestPreviewCancellationNoSendAndStaticNetworkErrors(t *testing.T) {
	p, s := fixture(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, catalog) })
	s.token = canary
	ctx, cancel := context.WithCancel(t.Context())
	a, err := p.PreviewClient(ctx, []modelaccess.Route{selected()})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if !errors.Is(a.Validate(t.Context()), context.Canceled) {
		t.Fatal("invocation cancellation lost")
	}
	if _, err := previewSend(t, a, previewBody); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled preview sent")
	}
	a = previewAccess(t, p)
	rt, err := a.Client(selected())
	if err != nil {
		t.Fatal(err)
	}
	req := chatRequest(t, previewBody)
	ctx, cancel = context.WithCancel(req.Context())
	cancel()
	req = req.WithContext(ctx)
	if _, err := rt.RoundTrip(req); !errors.Is(err, context.Canceled) {
		t.Fatal("request cancellation lost")
	}
	a = previewAccess(t, p)
	rt, err = a.Client(selected())
	if err != nil {
		t.Fatal(err)
	}
	p.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New(canary + previewBearer) })
	if resp, err := rt.RoundTrip(chatRequest(t, previewBody)); resp != nil || err == nil {
		t.Fatal("network failure accepted")
	} else {
		requirePrivate(t, err)
	}
}

// The wrapper must not give net/http an implicit inference replay mechanism.
func TestPreviewTransportHasNoReplayAndNoCredentialInCallerRequest(t *testing.T) {
	p, s := fixture(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, catalog) })
	s.token = canary
	a := previewAccess(t, p)
	original := p.http.Transport
	p.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/chat/completions" {
			return original.RoundTrip(r)
		}
		if r.GetBody != nil || r.Header.Get("Idempotency-Key") != "" || r.Header.Get("X-Idempotency-Key") != "" || r.Header.Get("Proxy-Authorization") != "" {
			t.Error("replay/proxy authority retained")
		}
		return &http.Response{StatusCode: 200, Status: "200 " + canary, Header: http.Header{"X-Echo": []string{canary}}, Trailer: http.Header{"Echo": []string{canary}}, Body: io.NopCloser(strings.NewReader(`{"choices":[]}`)), Request: r}, nil
	})
	req := chatRequest(t, previewBody)
	req.Header.Set("Idempotency-Key", "replay")
	req.Header.Set("X-Idempotency-Key", "replay")
	req.Header.Set("Proxy-Authorization", "private")
	rt, err := a.Client(selected())
	if err != nil {
		t.Fatal(err)
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if req.Header.Get("Authorization") != "" || resp.Request.Header.Get("Authorization") != "" {
		t.Fatal("caller request contains credential")
	}
	requirePrivate(t, resp.Header, resp.Trailer, resp.Status, resp.Request.Header)
	b, _ := json.Marshal(resp.Header)
	requirePrivate(t, string(b))
}
