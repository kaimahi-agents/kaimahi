package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kaimahi-agents/kaimahi/plane/internal/config"
	"github.com/kaimahi-agents/kaimahi/plane/internal/store"
)

// Split so this file carries no whole credential shape of its own — the
// tree scan reads test files like any other, and the claim it makes is
// that nothing key-shaped is committed anywhere, with no exceptions to
// remember. The value is unchanged.
const goodToken = "kmh_" + "test_token"

type fakeStore struct {
	allow      []string
	allowErr   error
	auditErr   error
	credErr    error
	audits     []store.ToolAuditEntry
	credential store.Credential
	// Approval grants: tool -> remaining grant uses (0 = exhausted/absent).
	toolGrants map[string]int
	grantErr   error
	filed      []string // "kind/subject" of filed requests
	filings    []store.Filing
	fileErr    error
	// grantDigest, when set, is the call the fake's grants are welded to;
	// empty stands for a legacy verb-level grant.
	grantDigest string
	// Identity on the call: what ActorFor answers, and whether it fails.
	actor    store.Attribution
	actorErr error
}

func (f *fakeStore) ActorFor(context.Context, string) (store.Attribution, error) {
	if f.actorErr != nil {
		return store.Lost, f.actorErr
	}
	if f.actor.ActedFor == "" {
		return store.Nobody, nil
	}
	return f.actor, nil
}

func (f *fakeStore) CredentialByTokenHash(_ context.Context, hash []byte) (store.Credential, error) {
	if f.credErr != nil {
		return store.Credential{}, f.credErr
	}
	want := sha256.Sum256([]byte(goodToken))
	if string(hash) != string(want[:]) {
		return store.Credential{}, store.ErrNotFound
	}
	return f.credential, nil
}

func (f *fakeStore) ToolAllowlist(_ context.Context, _ string) ([]string, error) {
	return f.allow, f.allowErr
}

func (f *fakeStore) RecordToolAudit(_ context.Context, e store.ToolAuditEntry) error {
	if f.auditErr != nil {
		return f.auditErr
	}
	f.audits = append(f.audits, e)
	return nil
}

// ConsumeToolGrant mirrors the store: a tool grant admits one CALL, so
// the digest must match the one the grant was minted for. A grant
// registered with an empty digest is the closed legacy class — a
// verb-level grant that predates argument binding.
func (f *fakeStore) ConsumeToolGrant(_ context.Context, _, tool, argDigest string) (string, bool, error) {
	if f.grantErr != nil {
		return "", false, f.grantErr
	}
	if f.toolGrants[tool] > 0 && (f.grantDigest == "" || f.grantDigest == argDigest) {
		f.toolGrants[tool]--
		return "grant-1", true, nil
	}
	return "", false, nil
}

func (f *fakeStore) LiveToolGrantSubjects(_ context.Context, _ string) ([]string, error) {
	if f.grantErr != nil {
		return nil, f.grantErr
	}
	var out []string
	for tool, uses := range f.toolGrants {
		if uses > 0 {
			out = append(out, tool)
		}
	}
	return out, nil
}

func (f *fakeStore) FileApprovalRequest(_ context.Context, fl store.Filing) (bool, error) {
	if f.fileErr != nil {
		return false, f.fileErr
	}
	f.filed = append(f.filed, fl.Kind+"/"+fl.Subject)
	f.filings = append(f.filings, fl)
	return true, nil
}

func rpc(t *testing.T, method string, params any) []byte {
	t.Helper()
	m := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method}
	if params != nil {
		m["params"] = params
	}
	raw, err := json.Marshal(m)
	require.NoError(t, err)
	return raw
}

func newGateway(t *testing.T, fs *fakeStore, upstream *httptest.Server) http.Handler {
	t.Helper()
	ups := map[string]config.ToolUpstream{}
	if upstream != nil {
		ups["kagent-tools"] = config.ToolUpstream{URL: upstream.URL + "/mcp"}
	}
	return NewMux(Deps{Store: fs, Upstreams: ups})
}

func post(h http.Handler, token string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost,
		"/upstream/kagent-tools/mcp", strings.NewReader(string(body)))
	if token != "" {
		req.Header.Set("Authorization", token)
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAuthRequired(t *testing.T) {
	upstreamHit := false
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHit = true
	}))
	defer up.Close()
	fs := &fakeStore{credential: store.Credential{Name: "hello-tools"}}
	h := newGateway(t, fs, up)

	assert.Equal(t, http.StatusUnauthorized, post(h, "", rpc(t, "tools/list", nil)).Code)
	assert.Equal(t, http.StatusUnauthorized, post(h, "kmh_wrong", rpc(t, "tools/list", nil)).Code)
	// Bearer prefix optional: headersFrom sends the Secret value verbatim.
	fs.allow = []string{"a"}
	assert.Equal(t, http.StatusOK, post(h, goodToken, rpc(t, "ping", nil)).Code)
	assert.Equal(t, http.StatusOK, post(h, "Bearer "+goodToken, rpc(t, "ping", nil)).Code)
	assert.False(t, upstreamHit, "unauthenticated or local requests must not reach the upstream")
	assert.Empty(t, fs.audits, "auth failures have no credential to audit")
}

func TestCredentialStoreFailureClosed(t *testing.T) {
	fs := &fakeStore{credErr: errors.New("pg down")}
	h := newGateway(t, fs, nil)
	assert.Equal(t, http.StatusServiceUnavailable, post(h, goodToken, rpc(t, "tools/list", nil)).Code)
}

func TestUnknownUpstreamDenied(t *testing.T) {
	fs := &fakeStore{credential: store.Credential{Name: "hello-tools"}}
	h := NewMux(Deps{Store: fs, Upstreams: map[string]config.ToolUpstream{}})
	rec := post(h, goodToken, rpc(t, "tools/call", map[string]any{"name": "x"}))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	require.Len(t, fs.audits, 1)
	assert.Equal(t, "denied", fs.audits[0].Decision)
}

func TestMethodScopeDenied(t *testing.T) {
	upstreamHit := false
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHit = true
	}))
	defer up.Close()
	fs := &fakeStore{credential: store.Credential{Name: "hello-tools"}}
	h := newGateway(t, fs, up)

	for _, method := range []string{"resources/list", "prompts/list", "completion/complete", "logging/setLevel"} {
		rec := post(h, goodToken, rpc(t, method, nil))
		assert.Equal(t, http.StatusOK, rec.Code, method)
		assert.Contains(t, rec.Body.String(), "tools only", method)
		assert.Contains(t, rec.Body.String(), `"error"`, method)
	}
	assert.False(t, upstreamHit, "denied methods must never be relayed")
	require.Len(t, fs.audits, 4)
	for _, e := range fs.audits {
		assert.Equal(t, "denied", e.Decision)
		assert.Equal(t, http.StatusForbidden, e.Status)
	}
}

func TestBatchRejected(t *testing.T) {
	fs := &fakeStore{credential: store.Credential{Name: "hello-tools"}}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer up.Close()
	h := newGateway(t, fs, up)
	rec := post(h, goodToken, []byte(`[{"jsonrpc":"2.0","id":1,"method":"tools/list"}]`))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestLifecycleRelayed(t *testing.T) {
	var sawMethods []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&m)
		sawMethods = append(sawMethods, m.Method)
		assert.Empty(t, r.Header.Get("Authorization"), "the kmh_ token must never reach the tool server")
		w.Header().Set("Mcp-Session-Id", "s-1")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"name":"fake"}}}`))
	}))
	defer up.Close()
	fs := &fakeStore{credential: store.Credential{Name: "hello-tools"}}
	h := newGateway(t, fs, up)

	rec := post(h, goodToken, rpc(t, "initialize", map[string]any{"protocolVersion": "2025-03-26"}))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "s-1", rec.Header().Get("Mcp-Session-Id"), "session header must relay back")

	rec = post(h, goodToken, []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, []string{"initialize", "notifications/initialized"}, sawMethods)
	assert.Empty(t, fs.audits, "successful lifecycle relays are not audited")
}

func TestToolsListProjected(t *testing.T) {
	listing := `{"jsonrpc":"2.0","id":1,"result":{"tools":[` +
		`{"name":"k8s_get_resources","description":"get"},` +
		`{"name":"k8s_describe_resource","description":"describe"},` +
		`{"name":"k8s_get_events","description":"events"}]}}`
	for name, respond := range map[string]http.HandlerFunc{
		"json": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(listing))
		},
		"sse": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: message\ndata: " + listing + "\n\n"))
		},
		// The space after "data:" is optional per the SSE format.
		"sse-no-space": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: message\ndata:" + listing + "\n\n"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			up := httptest.NewServer(respond)
			defer up.Close()
			fs := &fakeStore{credential: store.Credential{Name: "hello-tools"},
				allow: []string{"k8s_get_resources"}}
			h := newGateway(t, fs, up)
			rec := post(h, goodToken, rpc(t, "tools/list", nil))
			require.Equal(t, http.StatusOK, rec.Code)
			var out struct {
				Result struct {
					Tools []struct {
						Name string `json:"name"`
					} `json:"tools"`
				} `json:"result"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
			require.Len(t, out.Result.Tools, 1, "projection must hide unallowed tools")
			assert.Equal(t, "k8s_get_resources", out.Result.Tools[0].Name)
		})
	}
}

func TestToolsListEmptyAllowlistProjectsNothing(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"k8s_get_resources"}]}}`))
	}))
	defer up.Close()
	fs := &fakeStore{credential: store.Credential{Name: "hello-tools"}} // nil allowlist
	h := newGateway(t, fs, up)
	rec := post(h, goodToken, rpc(t, "tools/list", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"tools":[]`)
}

func TestToolsListUnparseableFailsClosed(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`not json at all`))
	}))
	defer up.Close()
	fs := &fakeStore{credential: store.Credential{Name: "hello-tools"}, allow: []string{"a"}}
	h := newGateway(t, fs, up)
	assert.Equal(t, http.StatusBadGateway, post(h, goodToken, rpc(t, "tools/list", nil)).Code)
}

func TestToolsCallAllowedRelayedAndAudited(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"three pods\"}]}}\n\n"))
	}))
	defer up.Close()
	fs := &fakeStore{credential: store.Credential{Name: "hello-tools"},
		allow: []string{"k8s_get_resources"}}
	h := newGateway(t, fs, up)
	rec := post(h, goodToken, rpc(t, "tools/call",
		map[string]any{"name": "k8s_get_resources", "arguments": map[string]any{"kind": "pods"}}))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "three pods", "tool responses relay byte-faithfully")
	require.Len(t, fs.audits, 1)
	e := fs.audits[0]
	assert.Equal(t, "allowed", e.Decision)
	assert.Equal(t, "k8s_get_resources", e.Tool)
	assert.Equal(t, "tools/call", e.Method)
	assert.Equal(t, http.StatusOK, e.Status)
	assert.Equal(t, "hello-tools", e.CredentialName)
}

func TestToolsCallDeniedOutsideAllowlist(t *testing.T) {
	upstreamHit := false
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHit = true
	}))
	defer up.Close()
	fs := &fakeStore{credential: store.Credential{Name: "hello-tools"},
		allow: []string{"k8s_get_resources"}}
	h := newGateway(t, fs, up)
	rec := post(h, goodToken, rpc(t, "tools/call", map[string]any{"name": "k8s_describe_resource"}))
	require.Equal(t, http.StatusOK, rec.Code, "denial travels as a JSON-RPC error")
	assert.Contains(t, rec.Body.String(), "not permitted")
	assert.False(t, upstreamHit, "denied calls must never reach the upstream")
	require.Len(t, fs.audits, 1)
	assert.Equal(t, "denied", fs.audits[0].Decision)
	assert.Equal(t, "k8s_describe_resource", fs.audits[0].Tool)
	assert.Equal(t, http.StatusForbidden, fs.audits[0].Status)
}

func TestToolsCallEmptyAllowlistDenied(t *testing.T) {
	fs := &fakeStore{credential: store.Credential{Name: "hello-tools"}} // nil allowlist
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream must not be reached")
	}))
	defer up.Close()
	h := newGateway(t, fs, up)
	rec := post(h, goodToken, rpc(t, "tools/call", map[string]any{"name": "k8s_get_resources"}))
	assert.Contains(t, rec.Body.String(), "not permitted")
}

func TestAllowlistReadFailureClosed(t *testing.T) {
	fs := &fakeStore{credential: store.Credential{Name: "hello-tools"},
		allowErr: errors.New("pg down")}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream must not be reached")
	}))
	defer up.Close()
	h := newGateway(t, fs, up)
	assert.Equal(t, http.StatusServiceUnavailable,
		post(h, goodToken, rpc(t, "tools/call", map[string]any{"name": "x"})).Code)
	assert.Equal(t, http.StatusServiceUnavailable,
		post(h, goodToken, rpc(t, "tools/list", nil)).Code)
}

func TestAuditDegradationFailsClosedAndRecovers(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[]}}`))
	}))
	defer up.Close()
	fs := &fakeStore{credential: store.Credential{Name: "hello-tools"},
		allow: []string{"k8s_get_resources"}, auditErr: errors.New("pg down")}
	h := newGateway(t, fs, up)
	call := rpc(t, "tools/call", map[string]any{"name": "k8s_get_resources"})

	// First call relays (the response was already committed when the audit
	// write failed) but trips the breaker...
	assert.Equal(t, http.StatusOK, post(h, goodToken, call).Code)
	// ...so the next request is denied before any upstream contact.
	assert.Equal(t, http.StatusServiceUnavailable, post(h, goodToken, call).Code)

	// The denial's own audit attempt is the recovery probe: once writes
	// succeed again, traffic resumes.
	fs.auditErr = nil
	assert.Equal(t, http.StatusServiceUnavailable, post(h, goodToken, call).Code,
		"the request that heals the breaker is itself still denied")
	assert.Equal(t, http.StatusOK, post(h, goodToken, call).Code)
}

func TestDuplicateKeysAreRefusedNotCollapsed(t *testing.T) {
	forwarded := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer up.Close()
	fs := &fakeStore{credential: store.Credential{Name: "hello-tools"},
		allow: []string{"k8s_get_resources"}}
	h := newGateway(t, fs, up)

	// A duplicated key is a tampering signal, not a typo — Go reads
	// last-wins and an upstream may read first-wins, so the message is
	// refused outright at every depth rather than silently collapsed.
	// Nothing is forwarded, so enforcement and the forwarded bytes cannot
	// disagree about what the call was.
	for _, body := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"k8s_delete_resource"},"method":"initialize"}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"k8s_delete_resource","name":"k8s_get_resources"}}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"k8s_get_resources",` +
			`"arguments":{"namespace":"default","namespace":"kube-system"}}}`,
	} {
		rec := post(h, goodToken, []byte(body))
		require.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, rec.Body.String(), "duplicate JSON key")
	}
	assert.Zero(t, forwarded, "a refused message must never reach the upstream")
	require.Len(t, fs.audits, 3)
	for _, a := range fs.audits {
		assert.Equal(t, "denied", a.Decision)
		assert.Equal(t, http.StatusBadRequest, a.Status)
	}
}

func TestUpstreamRedirectRefused(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://evil.example/mcp", http.StatusTemporaryRedirect)
	}))
	defer up.Close()
	fs := &fakeStore{credential: store.Credential{Name: "hello-tools"},
		allow: []string{"k8s_get_resources"}}
	h := newGateway(t, fs, up)

	for _, body := range [][]byte{
		rpc(t, "tools/call", map[string]any{"name": "k8s_get_resources"}),
		rpc(t, "tools/list", nil),
		rpc(t, "initialize", nil),
	} {
		rec := post(h, goodToken, body)
		assert.Equal(t, http.StatusBadGateway, rec.Code)
		assert.Empty(t, rec.Header().Get("Location"), "Location must not leak through")
	}
}

func TestToolGrantAdmitsConsumesAndExhausts(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[]}}`))
	}))
	defer up.Close()
	fs := &fakeStore{credential: store.Credential{Name: "hello-tools"},
		allow: []string{"k8s_get_resources"}, toolGrants: map[string]int{"k8s_get_events": 1}}
	h := newGateway(t, fs, up)
	call := rpc(t, "tools/call", map[string]any{"name": "k8s_get_events"})

	// USES=1: the first call is admitted via the grant and audited with
	// the grant noted...
	rec := post(h, goodToken, call)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, rec.Body.String(), "not permitted")
	require.Len(t, fs.audits, 1)
	assert.Equal(t, "allowed", fs.audits[0].Decision)
	assert.Contains(t, fs.audits[0].Detail, "granted")

	// ...and the second is denied (exhausted grant is not a grant) and
	// files a fresh approval request.
	rec = post(h, goodToken, call)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "not permitted")
	assert.Contains(t, rec.Body.String(), "approval request filed")
	assert.Equal(t, []string{"tool/k8s_get_events"}, fs.filed)

	// A statically allowlisted tool never consumes a grant.
	fs.toolGrants["k8s_get_resources"] = 5
	rec = post(h, goodToken, rpc(t, "tools/call", map[string]any{"name": "k8s_get_resources"}))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 5, fs.toolGrants["k8s_get_resources"])
}

func TestToolDenialFilesRequestAndFilingFailureStillDenies(t *testing.T) {
	fs := &fakeStore{credential: store.Credential{Name: "hello-tools"}}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("denied calls must not reach the upstream")
	}))
	defer up.Close()
	h := newGateway(t, fs, up)
	call := rpc(t, "tools/call", map[string]any{"name": "k8s_get_events"})

	rec := post(h, goodToken, call)
	assert.Contains(t, rec.Body.String(), "approval request filed")
	assert.Equal(t, []string{"tool/k8s_get_events"}, fs.filed)

	// Filing failure: the denial stands, without claiming a request was
	// filed, and nothing trips.
	fs.fileErr = errors.New("pg down")
	rec = post(h, goodToken, call)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "not permitted")
	assert.NotContains(t, rec.Body.String(), "approval request filed")
}

func TestGrantCheckFailureClosed(t *testing.T) {
	fs := &fakeStore{credential: store.Credential{Name: "hello-tools"},
		grantErr: errors.New("pg down")}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream must not be reached")
	}))
	defer up.Close()
	h := newGateway(t, fs, up)
	rec := post(h, goodToken, rpc(t, "tools/call", map[string]any{"name": "k8s_get_events"}))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	// tools/list projection also fails closed on a grant read failure.
	fs.allow = []string{"a"}
	assert.Equal(t, http.StatusServiceUnavailable, post(h, goodToken, rpc(t, "tools/list", nil)).Code)
}

func TestProjectionIncludesLiveGrants(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[` +
			`{"name":"k8s_get_resources"},{"name":"k8s_get_events"},{"name":"k8s_describe_resource"}]}}`))
	}))
	defer up.Close()
	fs := &fakeStore{credential: store.Credential{Name: "hello-tools"},
		allow: []string{"k8s_get_resources"}, toolGrants: map[string]int{"k8s_get_events": 1}}
	h := newGateway(t, fs, up)
	rec := post(h, goodToken, rpc(t, "tools/list", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "k8s_get_resources")
	assert.Contains(t, rec.Body.String(), "k8s_get_events", "live-granted tools are visible")
	assert.NotContains(t, rec.Body.String(), "k8s_describe_resource")
	assert.Equal(t, 1, fs.toolGrants["k8s_get_events"], "listing burns no uses")
}

func TestGetAnswers405(t *testing.T) {
	fs := &fakeStore{credential: store.Credential{Name: "hello-tools"}}
	h := newGateway(t, fs, nil)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/upstream/kagent-tools/mcp", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

func TestDeleteRelaysSessionTermination(t *testing.T) {
	sawDelete := false
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawDelete = r.Method == http.MethodDelete
		assert.Equal(t, "s-1", r.Header.Get("Mcp-Session-Id"))
		w.WriteHeader(http.StatusOK)
	}))
	defer up.Close()
	fs := &fakeStore{credential: store.Credential{Name: "hello-tools"}}
	h := newGateway(t, fs, up)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodDelete, "/upstream/kagent-tools/mcp", nil)
	req.Header.Set("Authorization", goodToken)
	req.Header.Set("Mcp-Session-Id", "s-1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, sawDelete)
}

// ---- keyed tool upstreams (credential injection from proxy-side custody) ----

// newKeyedGateway wires one upstream that carries its own credential,
// mirroring the Slack MCP server's SLACK_MCP_API_KEY: the gateway holds
// the key, the agent never does.
func newKeyedGateway(t *testing.T, fs *fakeStore, upstream *httptest.Server, secret, header string) (http.Handler, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "api-key")
	// A trailing newline is what `kubectl create secret --from-file` of a
	// pasted value routinely produces; it must not reach the header.
	require.NoError(t, os.WriteFile(path, []byte(secret+"\n"), 0o600))
	ups := map[string]config.ToolUpstream{
		"kagent-tools": {URL: upstream.URL + "/mcp", CredentialFile: path, CredentialHeader: header},
	}
	return NewMux(Deps{Store: fs, Upstreams: ups}), path
}

func TestKeyedUpstreamGetsInjectedCredentialAndNeverTheAgentToken(t *testing.T) {
	var gotAuth, gotXAPIKey string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotXAPIKey = r.Header.Get("X-Api-Key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"posted"}]}}`))
	}))
	defer up.Close()
	fs := &fakeStore{credential: store.Credential{Name: "hello-slack"},
		allow: []string{"conversations_add_message"}}
	h, _ := newKeyedGateway(t, fs, up, "upstream-secret", "")
	rec := post(h, goodToken, rpc(t, "tools/call",
		map[string]any{"name": "conversations_add_message", "arguments": map[string]any{}}))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "Bearer upstream-secret", gotAuth,
		"the upstream sees its OWN credential, trimmed, in the Authorization slot")
	assert.NotContains(t, gotAuth, "kmh_", "the agent's kaimahi token must never reach a tool server")
	assert.Empty(t, gotXAPIKey)
	require.Len(t, fs.audits, 1)
	assert.Equal(t, "allowed", fs.audits[0].Decision)
}

func TestKeyedUpstreamCustomHeaderSlot(t *testing.T) {
	var gotAuth, gotCustom string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotCustom = r.Header.Get("X-Api-Key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer up.Close()
	fs := &fakeStore{credential: store.Credential{Name: "hello-slack"},
		allow: []string{"channels_list"}}
	h, _ := newKeyedGateway(t, fs, up, "upstream-secret", "X-Api-Key")
	rec := post(h, goodToken, rpc(t, "tools/call",
		map[string]any{"name": "channels_list", "arguments": map[string]any{}}))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "upstream-secret", gotCustom, "a non-Authorization slot carries the raw value")
	assert.Empty(t, gotAuth, "the stripped agent token leaves the Authorization slot untouched")
}

func TestKeyedUpstreamMissingCredentialFailsClosed(t *testing.T) {
	upstreamHit := false
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHit = true
	}))
	defer up.Close()
	fs := &fakeStore{credential: store.Credential{Name: "hello-slack"},
		allow: []string{"conversations_add_message"}}
	h, path := newKeyedGateway(t, fs, up, "upstream-secret", "")
	require.NoError(t, os.Remove(path))
	rec := post(h, goodToken, rpc(t, "tools/call",
		map[string]any{"name": "conversations_add_message", "arguments": map[string]any{}}))
	// 503, not 502 and never a bare forward: an unreadable credential
	// must not downgrade to an unauthenticated call.
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.False(t, upstreamHit, "no request reaches a keyed upstream without its credential")
	require.Len(t, fs.audits, 1)
	assert.Equal(t, "allowed", fs.audits[0].Decision,
		"the allowlist decision stands; the audit row carries the 503 outcome")
	assert.Equal(t, http.StatusServiceUnavailable, fs.audits[0].Status)
}

func TestKeyedUpstreamEmptyCredentialFailsClosed(t *testing.T) {
	upstreamHit := false
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHit = true
	}))
	defer up.Close()
	fs := &fakeStore{credential: store.Credential{Name: "hello-slack"},
		allow: []string{"conversations_add_message"}}
	h, path := newKeyedGateway(t, fs, up, "upstream-secret", "")
	require.NoError(t, os.WriteFile(path, []byte("   \n"), 0o600))
	rec := post(h, goodToken, rpc(t, "tools/call",
		map[string]any{"name": "conversations_add_message", "arguments": map[string]any{}}))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.False(t, upstreamHit, "a whitespace-only Secret is not a credential")
}

func TestKeyedUpstreamProjectionAlsoInjects(t *testing.T) {
	var gotAuth string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"channels_list"},{"name":"conversations_add_message"}]}}`))
	}))
	defer up.Close()
	fs := &fakeStore{credential: store.Credential{Name: "hello-slack"},
		allow: []string{"channels_list"}}
	h, _ := newKeyedGateway(t, fs, up, "upstream-secret", "")
	rec := post(h, goodToken, rpc(t, "tools/list", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "Bearer upstream-secret", gotAuth,
		"discovery rides the same custody as tool calls")
	assert.Contains(t, rec.Body.String(), "channels_list")
	assert.NotContains(t, rec.Body.String(), "conversations_add_message",
		"posting is not allowlisted: an agent never even sees it until it is approved")
}

func TestKeyedUpstreamCredentialRotatesWithoutRestart(t *testing.T) {
	var seen []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer up.Close()
	fs := &fakeStore{credential: store.Credential{Name: "hello-slack"},
		allow: []string{"channels_list"}}
	h, path := newKeyedGateway(t, fs, up, "first", "")
	call := func() { post(h, goodToken, rpc(t, "tools/call", map[string]any{"name": "channels_list"})) }
	call()
	require.NoError(t, os.WriteFile(path, []byte("second\n"), 0o600))
	call()
	assert.Equal(t, []string{"Bearer first", "Bearer second"}, seen,
		"the credential is read per request, so a Secret update needs no restart")
}
