package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/kaimahi-agents/kaimahi/plane/internal/gateway"
	"github.com/kaimahi-agents/kaimahi/plane/internal/store"
)

// The notifier's contract: a governed post through the plane's own
// gateway, retried only when the message is KNOWN not to have reached
// Slack, never when the answer was lost after the request went out.

type gw struct {
	*httptest.Server
	mu    sync.Mutex
	calls []call
	// answer decides the tools/call response.
	answer func(w http.ResponseWriter, r *http.Request)
}

type call struct {
	method  string
	auth    string
	session string
	args    map[string]any
}

func newGW(t *testing.T) *gw {
	t.Helper()
	g := &gw{}
	g.answer = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"posted"}]}}`)
	}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var m struct {
			Method string `json:"method"`
			Params struct {
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		_ = json.Unmarshal(raw, &m)
		g.mu.Lock()
		g.calls = append(g.calls, call{method: m.Method, auth: r.Header.Get("Authorization"),
			session: r.Header.Get("Mcp-Session-Id"), args: m.Params.Arguments})
		g.mu.Unlock()
		require.Equal(t, "/upstream/slack/mcp", r.URL.Path)
		switch m.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sess-1")
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		default:
			g.answer(w, r)
		}
	}))
	t.Cleanup(g.Close)
	return g
}

func (g *gw) toolCalls() []call {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []call
	for _, c := range g.calls {
		if c.method == "tools/call" {
			out = append(out, c)
		}
	}
	return out
}

type fixture struct {
	p      *Poster
	sleeps []time.Duration
}

func newFixture(t *testing.T, url string, opts ...func(*Deps)) *fixture {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "token"), []byte("kmh_plane_token\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "channel"), []byte("C0TEST\n"), 0o600))
	f := &fixture{}
	d := Deps{GatewayURL: url, Upstream: "slack", Tool: "conversations_add_message",
		CredentialFile: filepath.Join(dir, "token"), ChannelFile: filepath.Join(dir, "channel"),
		CallTimeout: 2 * time.Second, Backoff: []time.Duration{time.Millisecond, time.Millisecond},
		Sleep: func(_ context.Context, d time.Duration) { f.sleeps = append(f.sleeps, d) }}
	for _, o := range opts {
		o(&d)
	}
	f.p = New(d)
	return f
}

// drain enqueues one post and runs the worker until it has been sent.
func (f *fixture) drain(t *testing.T, post Post) {
	t.Helper()
	require.True(t, f.p.Enqueue(post))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.p.Run(ctx); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(f.p.jobs) > 0 {
		time.Sleep(5 * time.Millisecond)
	}
	// One more beat so the in-flight send finishes before cancel.
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done
}

func TestPostGoesThroughTheGatewayUnderThePlanesCredential(t *testing.T) {
	g := newGW(t)
	f := newFixture(t, g.URL)
	f.drain(t, Post{Kind: "test", Text: "hello", ThreadTS: "1725.0001"})
	calls := g.toolCalls()
	require.Len(t, calls, 1)
	require.Equal(t, "Bearer kmh_plane_token", calls[0].auth)
	require.Equal(t, "sess-1", calls[0].session, "the upstream's session id is relayed after initialize")
	require.Equal(t, map[string]any{"channel_id": "C0TEST", "payload": "hello", "thread_ts": "1725.0001"}, calls[0].args)
	require.Empty(t, f.sleeps)
	// A notification (no thread) carries no thread_ts at all.
	f.drain(t, Post{Kind: "test", Text: "top level"})
	calls = g.toolCalls()
	require.Len(t, calls, 2)
	_, has := calls[1].args["thread_ts"]
	require.False(t, has)
}

func TestRefusalsAreRetriedBounded(t *testing.T) {
	for name, answer := range map[string]func(w http.ResponseWriter, _ *http.Request){
		"redirect refused (502 before any byte went out)": func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, gateway.MsgUpstreamRedirected, http.StatusBadGateway)
		},
		"pre-forward 503": func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "tool audit unavailable", http.StatusServiceUnavailable)
		},
		"not allowlisted (JSON-RPC error)": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":2,"error":{"code":-32001,"message":"tool not permitted"}}`)
		},
		"slack refused (isError)": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":2,"result":{"isError":true,"content":[{"type":"text","text":"not_in_channel"}]}}`)
		},
		"unauthorized (401)": func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "unauthorized", http.StatusUnauthorized) },
	} {
		t.Run(name, func(t *testing.T) {
			g := newGW(t)
			g.answer = answer
			f := newFixture(t, g.URL)
			f.drain(t, Post{Kind: "test", Text: "x"})
			require.Len(t, g.toolCalls(), 3, "three attempts: the message never reached Slack")
			require.Equal(t, []time.Duration{time.Millisecond, time.Millisecond}, f.sleeps)
		})
	}
}

func TestAmbiguousFailuresAreNotRetried(t *testing.T) {
	for name, answer := range map[string]func(w http.ResponseWriter, _ *http.Request){
		// The gateway's 502 covers a dial refusal AND a reset after the
		// post was delivered; it cannot say which, so neither is retried.
		"upstream unreachable (502)": func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "tool upstream unreachable", http.StatusBadGateway)
		},
		"upstream 500 relayed": func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "boom", http.StatusInternalServerError) },
		"unparseable 200":      func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "<html>") },
		"timeout": func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-time.After(2 * time.Second):
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			g := newGW(t)
			g.answer = answer
			f := newFixture(t, g.URL, func(d *Deps) { d.CallTimeout = 300 * time.Millisecond })
			f.drain(t, Post{Kind: "test", Text: "x"})
			require.Len(t, g.toolCalls(), 1, "the request went out and the answer was lost: never posted twice")
			require.Empty(t, f.sleeps)
		})
	}
}

func TestDialFailureIsRefusedAndRetried(t *testing.T) {
	g := newGW(t)
	url := g.URL
	g.Close() // nothing listens: a dial error, before anything was sent
	f := newFixture(t, url)
	f.drain(t, Post{Kind: "test", Text: "x"})
	require.Len(t, f.sleeps, 2)
}

func TestSSEFramedAnswerIsRead(t *testing.T) {
	g := newGW(t)
	g.answer = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/message\",\"params\":{}}\n\n"+
			"event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":2,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"ok\"}]}}\n\n")
	}
	f := newFixture(t, g.URL)
	f.drain(t, Post{Kind: "test", Text: "x"})
	require.Len(t, g.toolCalls(), 1)
	require.Empty(t, f.sleeps, "posted: no retry")
}

func TestUnreadableCustodyPostsNothing(t *testing.T) {
	g := newGW(t)
	f := newFixture(t, g.URL, func(d *Deps) { d.CredentialFile = "/nonexistent/token" })
	f.drain(t, Post{Kind: "test", Text: "x"})
	require.Empty(t, g.toolCalls(), "no credential, no call — never an unauthenticated attempt")
	// A channel file that is not exactly one channel (the MCP server's
	// "post anywhere" grammar) is refused the same way.
	f = newFixture(t, g.URL)
	require.NoError(t, os.WriteFile(f.p.d.ChannelFile, []byte("true"), 0o600))
	f.drain(t, Post{Kind: "test", Text: "x"})
	require.Empty(t, g.toolCalls())
}

func TestInitializeFailureStillSendsTheCall(t *testing.T) {
	// The tools/call is what the gateway audits; it is sent whatever the
	// handshake answered, so every attempt is on record.
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var m struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(raw, &m)
		calls = append(calls, m.Method)
		http.Error(w, "tool upstream unreachable", http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	f := newFixture(t, srv.URL)
	f.drain(t, Post{Kind: "test", Text: "x"})
	require.Equal(t, []string{"initialize", "tools/call"}, calls)
}

func TestQueueFullDropsRatherThanBlocks(t *testing.T) {
	f := newFixture(t, "http://127.0.0.1:1", func(d *Deps) { d.QueueSize = 1 })
	require.True(t, f.p.Enqueue(Post{Kind: "a"}))
	require.False(t, f.p.Enqueue(Post{Kind: "b"}))
}

// --- the store wrapper and the message ----------------------------------

type fakeFiler struct {
	id    string
	filed bool
	err   error
}

func (f fakeFiler) FileRequest(context.Context, store.Filing) (string, bool, error) {
	return f.id, f.filed, f.err
}

type fakeNotifier struct{ got []Filing }

func (n *fakeNotifier) Notify(f Filing) { n.got = append(n.got, f) }

func TestStoreNotifiesOncePerFreshFiling(t *testing.T) {
	n := &fakeNotifier{}
	s := Store{Filer: fakeFiler{id: "00000000-0000-0000-0000-000000000001", filed: true}, N: n}
	filed, err := s.FileApprovalRequest(context.Background(), store.Filing{Credential: "hello-tools", Kind: "tool",
		Subject: "k8s_get_events", Detail: "denied tools/call", ArgSummary: "k8s_get_events: namespace default"})
	require.NoError(t, err)
	require.True(t, filed)
	require.Equal(t, []Filing{{ID: "00000000-0000-0000-0000-000000000001", Credential: "hello-tools",
		Kind: "tool", Subject: "k8s_get_events", Detail: "denied tools/call",
		Summary: "k8s_get_events: namespace default"}}, n.got)

	// Deduped: the request was already pending — nobody is told twice.
	s.Filer = fakeFiler{filed: false}
	filed, err = s.FileApprovalRequest(context.Background(), store.Filing{Credential: "hello-tools", Kind: "tool", Subject: "k8s_get_events", Detail: "again"})
	require.NoError(t, err)
	require.False(t, filed)
	require.Len(t, n.got, 1)

	// A failed filing notifies nothing and surfaces the error.
	s.Filer = fakeFiler{err: errors.New("pg down")}
	_, err = s.FileApprovalRequest(context.Background(), store.Filing{Credential: "hello-tools", Kind: "tool", Subject: "k8s_get_events", Detail: "again"})
	require.Error(t, err)
	require.Len(t, n.got, 1)

	// No notifier configured: the store's answer, unchanged.
	s = Store{Filer: fakeFiler{id: "x", filed: true}}
	filed, err = s.FileApprovalRequest(context.Background(), store.Filing{Credential: "c", Kind: "tool", Subject: "t", Detail: "d"})
	require.NoError(t, err)
	require.True(t, filed)
}

func TestMessageNamesTheRequestAndTheCommand(t *testing.T) {
	m := Message(Filing{ID: "00000000-0000-0000-0000-000000000007", Credential: "hello-slack",
		Kind: "tool", Subject: "conversations_add_message", Detail: "denied tools/call via upstream slack"})
	for _, want := range []string{"00000000-0000-0000-0000-000000000007", "hello-slack", "tool", "conversations_add_message",
		"@kaimahi approve 00000000-0000-0000-0000-000000000007", "@kaimahi deny 00000000-0000-0000-0000-000000000007", "make approvals"} {
		require.Contains(t, m, want)
	}
	require.NotContains(t, m, "amount=")
	m = Message(Filing{ID: "00000000-0000-0000-0000-000000000008", Credential: "hello-world", Kind: "budget", Subject: "tokens"})
	require.Contains(t, m, "amount=<tokens>")
	require.False(t, strings.Contains(m, "<@"), "the bot is named in plain text, never as a mention token")

	// An approver who cannot see the transaction is the whole
	// problem restated — where a filing carries a call summary, that is
	// what the notification names.
	m = Message(Filing{ID: "00000000-0000-0000-0000-000000000009", Credential: "ap-agent",
		Kind: "tool", Subject: "payment_schedule", Detail: "denied tools/call via upstream erp",
		Summary: "payment_schedule: amount_cents 4800000, payee_id MER-4471"})
	require.Contains(t, m, "payment_schedule: amount_cents 4800000, payee_id MER-4471")
}
