package gateway

import (
	"context"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kaimahi-agents/kaimahi/plane/internal/config"
	"github.com/kaimahi-agents/kaimahi/plane/internal/egress"
	"github.com/kaimahi-agents/kaimahi/plane/internal/store"
)

// A hosted (`internet: true`) tool upstream is reached only through
// the injected hardened client, and what the dialer decides lands on the
// audit row of the call it decided.

type scriptedResolver struct{ answers [][]netip.Addr }

func (s *scriptedResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	a := s.answers[0]
	if len(s.answers) > 1 {
		s.answers = s.answers[1:]
	}
	return a, nil
}

// hostedGateway wires a gateway whose "github" upstream is an https test
// server reached through a real egress client: the resolver is scripted,
// the dial is redirected to the test listener AFTER the check, and the
// server's certificate is the upstream's ca_file.
func hostedGateway(t *testing.T, fs *fakeStore, handler http.HandlerFunc, resolver egress.Resolver, policy egress.Policy) http.Handler {
	t.Helper()
	srv := httptest.NewTLSServer(handler)
	t.Cleanup(srv.Close)
	caFile := filepath.Join(t.TempDir(), "ca.crt")
	require.NoError(t, os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600))
	policy.Resolver = resolver
	policy.Dial = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}
	client, err := egress.NewClient(policy, []egress.Host{{Name: "example.com", CAFile: caFile}})
	require.NoError(t, err)
	ups := map[string]config.ToolUpstream{
		"github": {URL: "https://example.com/mcp", Internet: true, CAFile: caFile},
	}
	return NewMux(Deps{Store: fs, Upstreams: ups, InternetClient: client})
}

func postHosted(h http.Handler, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost,
		"/upstream/github/mcp", strings.NewReader(string(body)))
	req.Header.Set("Authorization", goodToken)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func lastAudit(t *testing.T, fs *fakeStore) store.ToolAuditEntry {
	t.Helper()
	require.NotEmpty(t, fs.audits)
	return fs.audits[len(fs.audits)-1]
}

func TestHostedUpstreamWithoutHardenedClientFailsClosed(t *testing.T) {
	hit := false
	up := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit = true }))
	defer up.Close()
	fs := &fakeStore{credential: store.Credential{Name: "hello-github"}, allow: []string{"list_issues"}}
	// Marked internet, but NO InternetClient injected: the plain client
	// must never be used as a fallback.
	h := NewMux(Deps{Store: fs, Upstreams: map[string]config.ToolUpstream{
		"github": {URL: "https://example.com/mcp", Internet: true}}})
	rec := postHosted(h, rpc(t, "tools/call", map[string]any{"name": "list_issues", "arguments": map[string]any{}}))
	assert.Equal(t, http.StatusBadGateway, rec.Code)
	assert.Contains(t, rec.Body.String(), "refused by the egress policy")
	assert.False(t, hit)
	e := lastAudit(t, fs)
	assert.Equal(t, "allowed", e.Decision)
	assert.Equal(t, http.StatusBadGateway, e.Status)
	assert.Contains(t, e.Detail, "egress refused")
}

func TestHostedUpstreamRoundTripsAndAuditsAllowed200(t *testing.T) {
	fs := &fakeStore{credential: store.Credential{Name: "hello-github"}, allow: []string{"list_issues"}}
	h := hostedGateway(t, fs, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "example.com", r.Host)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"probe-1"}]}}`)
	}, &scriptedResolver{answers: [][]netip.Addr{{netip.MustParseAddr("203.0.113.10")}}}, egress.Policy{})
	rec := postHosted(h, rpc(t, "tools/call", map[string]any{"name": "list_issues", "arguments": map[string]any{}}))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "probe-1")
	e := lastAudit(t, fs)
	assert.Equal(t, "allowed", e.Decision)
	assert.Equal(t, http.StatusOK, e.Status)
	assert.Empty(t, e.Detail)
}

func TestHostedUpstreamPrivateAnswerRefusedAndAudited(t *testing.T) {
	hit := false
	fs := &fakeStore{credential: store.Credential{Name: "hello-github"}, allow: []string{"list_issues"}}
	// Rebinding: public on the first call (answered), private on the
	// second (refused before any byte leaves).
	h := hostedGateway(t, fs, func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
	}, &scriptedResolver{answers: [][]netip.Addr{{netip.MustParseAddr("203.0.113.10")}, {netip.MustParseAddr("169.254.169.254")}}}, egress.Policy{})
	call := rpc(t, "tools/call", map[string]any{"name": "list_issues", "arguments": map[string]any{}})
	assert.Equal(t, http.StatusOK, postHosted(h, call).Code)
	hit = false
	rec := postHosted(h, call)
	assert.Equal(t, http.StatusBadGateway, rec.Code)
	assert.Contains(t, rec.Body.String(), "refused by the egress policy")
	assert.False(t, hit, "the refused call must not reach the upstream")
	e := lastAudit(t, fs)
	assert.Equal(t, "allowed", e.Decision)
	assert.Equal(t, http.StatusBadGateway, e.Status)
	assert.Contains(t, e.Detail, "egress refused")
	assert.Contains(t, e.Detail, "cloud metadata endpoint")
	assert.Len(t, fs.audits, 2)
}

func TestHostedUpstreamCutBodyFailsClosedAsA502(t *testing.T) {
	fs := &fakeStore{credential: store.Credential{Name: "hello-github"}, allow: []string{"list_issues"}}
	release := make(chan struct{})
	defer close(release)
	h := hostedGateway(t, fs, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"content":[`)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		select { // stall
		case <-release:
		case <-r.Context().Done():
		}
	}, &scriptedResolver{answers: [][]netip.Addr{{netip.MustParseAddr("203.0.113.10")}}},
		egress.Policy{BodyLifetime: 300 * time.Millisecond})
	started := time.Now()
	rec := postHosted(h, rpc(t, "tools/call", map[string]any{"name": "list_issues", "arguments": map[string]any{}}))
	assert.Less(t, time.Since(started), 5*time.Second)
	// The client never saw a 200 with half a payload: the status is 502.
	assert.Equal(t, http.StatusBadGateway, rec.Code)
	assert.Contains(t, rec.Body.String(), "cut by the egress policy")
	e := lastAudit(t, fs)
	assert.Equal(t, http.StatusBadGateway, e.Status)
	assert.Contains(t, e.Detail, "upstream body cut")
	assert.Contains(t, e.Detail, "lifetime")

	// The size cap, the same way.
	fs2 := &fakeStore{credential: store.Credential{Name: "hello-github"}, allow: []string{"list_issues"}}
	h2 := hostedGateway(t, fs2, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(make([]byte, 5000))
	}, &scriptedResolver{answers: [][]netip.Addr{{netip.MustParseAddr("203.0.113.10")}}},
		egress.Policy{MaxResponseBytes: 2048})
	rec = postHosted(h2, rpc(t, "tools/call", map[string]any{"name": "list_issues", "arguments": map[string]any{}}))
	assert.Equal(t, http.StatusBadGateway, rec.Code)
	assert.Contains(t, lastAudit(t, fs2).Detail, "size cap")
}

func TestHostedUpstreamRedirectRefusedAndNoted(t *testing.T) {
	fs := &fakeStore{credential: store.Credential{Name: "hello-github"}, allow: []string{"list_issues"}}
	h := hostedGateway(t, fs, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://example.com/elsewhere", http.StatusFound)
	}, &scriptedResolver{answers: [][]netip.Addr{{netip.MustParseAddr("203.0.113.10")}}}, egress.Policy{})
	rec := postHosted(h, rpc(t, "tools/call", map[string]any{"name": "list_issues", "arguments": map[string]any{}}))
	assert.Equal(t, http.StatusBadGateway, rec.Code)
	assert.Contains(t, rec.Body.String(), MsgUpstreamRedirected)
	assert.Equal(t, MsgUpstreamRedirected, lastAudit(t, fs).Detail)
}

// In-cluster upstreams are untouched by any of this: no marker, plain
// client, no InternetClient needed.
func TestInClusterUpstreamNeverUsesTheHardenedClient(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
	}))
	defer up.Close()
	fs := &fakeStore{credential: store.Credential{Name: "hello-tools"}, allow: []string{"k8s_get_resources"}}
	hardened, err := egress.NewClient(egress.Policy{}, nil)
	require.NoError(t, err)
	h := NewMux(Deps{Store: fs, Upstreams: map[string]config.ToolUpstream{"kagent-tools": {URL: up.URL + "/mcp"}},
		InternetClient: hardened})
	rec := post(h, goodToken, rpc(t, "tools/call", map[string]any{"name": "k8s_get_resources", "arguments": map[string]any{}}))
	assert.Equal(t, http.StatusOK, rec.Code, "a plain-http in-cluster upstream is reachable although the hardened client would refuse it")
}
