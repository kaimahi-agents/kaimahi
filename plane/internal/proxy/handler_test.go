package proxy_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/kaimahi-agents/kaimahi/plane/internal/config"
	"github.com/kaimahi-agents/kaimahi/plane/internal/meter"
	"github.com/kaimahi-agents/kaimahi/plane/internal/pricing"
	"github.com/kaimahi-agents/kaimahi/plane/internal/proxy"
	"github.com/kaimahi-agents/kaimahi/plane/internal/store"
)

// fakeStore implements proxy.Store in memory.
type fakeStore struct {
	creds      map[string]store.Credential // key: hex-free string(token hash)
	actor      store.Attribution
	actorErr   error
	ledger     []store.LedgerEntry
	lookupErr  error
	ledgerErr  error
	monthCents int64
	monthToks  int64
	monthErr   error
	// Reservations: open holds and the ids RecordLedger consumed.
	open     map[string]store.SpendHold
	consumed []string
	admitErr error
	nextRes  int
}

func newFakeStore() *fakeStore {
	return &fakeStore{creds: map[string]store.Credential{},
		open: map[string]store.SpendHold{}}
}

func (f *fakeStore) credByName(name string) (store.Credential, bool) {
	for _, c := range f.creds {
		if c.Name == name {
			return c, true
		}
	}
	return store.Credential{}, false
}

// AdmitSpend mirrors the store's locked admission in memory: committed
// = month sums + open holds; a reached cap denies; an admitted call
// under a cap holds.
func (f *fakeStore) AdmitSpend(_ context.Context, credential string, hold store.SpendHold, _ time.Time, _ time.Duration) (store.Admission, error) {
	if f.admitErr != nil {
		return store.Admission{}, f.admitErr
	}
	if f.monthErr != nil {
		return store.Admission{}, f.monthErr
	}
	c, ok := f.credByName(credential)
	if !ok {
		return store.Admission{}, store.ErrNotFound
	}
	if c.CapCents == nil && c.CapTokens == nil {
		return store.Admission{}, nil
	}
	cents, tokens := f.monthCents, f.monthToks
	for _, h := range f.open {
		cents += h.Cents
		tokens += h.Tokens
	}
	if c.CapCents != nil && cents >= *c.CapCents {
		return store.Admission{Denied: true, Subject: "cents"}, nil
	}
	if c.CapTokens != nil && tokens >= *c.CapTokens {
		return store.Admission{Denied: true, Subject: "tokens"}, nil
	}
	f.nextRes++
	id := fmt.Sprintf("res-%d", f.nextRes)
	f.open[id] = hold
	return store.Admission{ReservationID: id}, nil
}

func (f *fakeStore) addToken(token string, c store.Credential) {
	h := sha256.Sum256([]byte(token))
	f.creds[string(h[:])] = c
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

func (f *fakeStore) RenewCredential(_ context.Context, name string, expiresAt time.Time) error {
	for k, c := range f.creds {
		if c.Name == name {
			c.ExpiresAt = &expiresAt
			f.creds[k] = c
			return nil
		}
	}
	return store.ErrNotFound
}

func (f *fakeStore) ListCredentials(context.Context) ([]store.Credential, error) {
	var out []store.Credential
	for _, c := range f.creds {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (f *fakeStore) CredentialByTokenHash(_ context.Context, hash []byte) (store.Credential, error) {
	if f.lookupErr != nil {
		return store.Credential{}, f.lookupErr
	}
	c, ok := f.creds[string(hash)]
	if !ok {
		return store.Credential{}, store.ErrNotFound
	}
	return c, nil
}

func (f *fakeStore) RecordLedger(_ context.Context, e store.LedgerEntry, reservation string) error {
	if f.ledgerErr != nil {
		return f.ledgerErr
	}
	f.ledger = append(f.ledger, e)
	if reservation != "" {
		f.consumed = append(f.consumed, reservation)
		delete(f.open, reservation)
	}
	return nil
}

func (f *fakeStore) CreateCredential(_ context.Context, name string, hash []byte, expiresAt time.Time) error {
	for _, c := range f.creds {
		if c.Name == name {
			return store.ErrExists
		}
	}
	f.creds[string(hash)] = store.Credential{Name: name, ExpiresAt: &expiresAt}
	return nil
}

func (f *fakeStore) SetBudget(_ context.Context, name string, capCents, capTokens *int64) error {
	for k, c := range f.creds {
		if c.Name == name {
			c.CapCents, c.CapTokens = capCents, capTokens
			f.creds[k] = c
			return nil
		}
	}
	return store.ErrNotFound
}

func (f *fakeStore) Ledger(_ context.Context, name string, _ int) ([]store.LedgerEntry, error) {
	var out []store.LedgerEntry
	for _, e := range f.ledger {
		if name == "" || e.CredentialName == name {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *fakeStore) MonthUsage(_ context.Context, _ string, _ time.Time) (int64, int64, error) {
	return f.monthCents, f.monthToks, f.monthErr
}

func i64(v int64) *int64 { return &v }

const chatBody = `{"model": "test-model", "messages": [{"role": "user", "content": "hi"}]}`

// newUpstream returns an httptest upstream that records the last request
// it saw and answers a fixed OpenAI-style completion with usage.
func newUpstream(t *testing.T) (*httptest.Server, *http.Request, *[]byte) {
	t.Helper()
	var gotReq http.Request
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReq = *r.Clone(context.Background())
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"choices": [{"message": {"content": "hello"}}], "usage": {"prompt_tokens": 7, "completion_tokens": 11}}`)
	}))
	t.Cleanup(srv.Close)
	return srv, &gotReq, &gotBody
}

func testDeps(f *fakeStore, upstreams map[string]config.Upstream) proxy.Deps {
	return proxy.Deps{
		Store:  f,
		Meter:  &meter.Meter{Store: f},
		Config: config.Config{Upstreams: upstreams},
	}
}

func doChat(t *testing.T, mux http.Handler, token, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), "POST", path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func TestRejectsMissingAndUnknownToken(t *testing.T) {
	f := newFakeStore()
	mux := proxy.NewDataMux(testDeps(f, map[string]config.Upstream{}))
	require.Equal(t, 401, doChat(t, mux, "", "/upstream/ollama/v1/chat/completions", chatBody).Code)
	require.Equal(t, 401, doChat(t, mux, "wrong", "/upstream/ollama/v1/chat/completions", chatBody).Code)
	require.Empty(t, f.ledger)
}

func TestFailsClosedWhenCredentialStoreDown(t *testing.T) {
	f := newFakeStore()
	f.lookupErr = errors.New("db down")
	mux := proxy.NewDataMux(testDeps(f, map[string]config.Upstream{}))
	require.Equal(t, 503, doChat(t, mux, "tok", "/upstream/ollama/v1/chat/completions", chatBody).Code)
}

func TestDeniesUnknownUpstreamAndWrongPath(t *testing.T) {
	f := newFakeStore()
	f.addToken("tok", store.Credential{Name: "hello"})
	up, _, _ := newUpstream(t)
	mux := proxy.NewDataMux(testDeps(f, map[string]config.Upstream{
		"ollama": {Protocol: config.ProtocolChatCompletions, BaseURL: up.URL, Path: "v1/chat/completions", Classification: config.ClassFree},
	}))
	require.Equal(t, 403, doChat(t, mux, "tok", "/upstream/nope/v1/chat/completions", chatBody).Code)
	require.Equal(t, 403, doChat(t, mux, "tok", "/upstream/ollama/v1/embeddings", chatBody).Code)
	require.Len(t, f.ledger, 2)
	for _, e := range f.ledger {
		require.Equal(t, "denied", e.CostSource)
		require.Equal(t, 403, e.Status)
	}
}

func TestForwardStripsKaimahiTokenAndInjectsRealCredential(t *testing.T) {
	f := newFakeStore()
	f.addToken("kmh_opaque", store.Credential{Name: "hello"})
	up, gotReq, _ := newUpstream(t)
	dir := t.TempDir()
	credFile := filepath.Join(dir, "cred")
	require.NoError(t, os.WriteFile(credFile, []byte("real-upstream-key\n"), 0o600))
	mux := proxy.NewDataMux(testDeps(f, map[string]config.Upstream{
		"copilot": {Protocol: config.ProtocolChatCompletions, BaseURL: up.URL, Path: "chat/completions", Classification: config.ClassMetered,
			CredentialFile: credFile, ExtraHeaders: map[string]string{"Copilot-Integration-Id": "vscode-chat"}},
	}))
	w := doChat(t, mux, "kmh_opaque", "/upstream/copilot/chat/completions", chatBody)
	require.Equal(t, 200, w.Code)
	require.Equal(t, "Bearer real-upstream-key", gotReq.Header.Get("Authorization"))
	require.Equal(t, "vscode-chat", gotReq.Header.Get("Copilot-Integration-Id"))
	require.NotContains(t, gotReq.Header.Get("Authorization"), "kmh_opaque")
	// Usage from the upstream response is ledgered; no price row => unpriced, tokens still counted.
	require.Len(t, f.ledger, 1)
	e := f.ledger[0]
	require.Equal(t, int64(7), e.InputTokens)
	require.Equal(t, int64(11), e.OutputTokens)
	require.Equal(t, "unpriced", e.CostSource)
	require.Equal(t, int64(0), e.CostCents)
	require.Equal(t, 200, e.Status)
}

func TestFreeUpstreamForwardsBareAndLedgersFree(t *testing.T) {
	f := newFakeStore()
	f.addToken("tok", store.Credential{Name: "hello"})
	up, gotReq, _ := newUpstream(t)
	mux := proxy.NewDataMux(testDeps(f, map[string]config.Upstream{
		"ollama": {Protocol: config.ProtocolChatCompletions, BaseURL: up.URL, Path: "v1/chat/completions", Classification: config.ClassFree},
	}))
	w := doChat(t, mux, "tok", "/upstream/ollama/v1/chat/completions", chatBody)
	require.Equal(t, 200, w.Code)
	require.Empty(t, gotReq.Header.Get("Authorization"), "keyless upstream must receive no credential at all")
	require.Len(t, f.ledger, 1)
	require.Equal(t, "free", f.ledger[0].CostSource)
	require.Equal(t, int64(0), f.ledger[0].CostCents)
	require.Equal(t, int64(18), f.ledger[0].InputTokens+f.ledger[0].OutputTokens)
}

func TestPricedModelCostsAreLedgered(t *testing.T) {
	f := newFakeStore()
	f.addToken("tok", store.Credential{Name: "hello"})
	up, _, _ := newUpstream(t)
	mux := proxy.NewDataMux(testDeps(f, map[string]config.Upstream{
		"copilot": {Protocol: config.ProtocolChatCompletions, BaseURL: up.URL, Path: "chat/completions", Classification: config.ClassMetered,
			Prices: map[string]pricing.Price{"test-model": {InCentsPer1M: 1_000_000, OutCentsPer1M: 2_000_000}}},
	}))
	require.Equal(t, 200, doChat(t, mux, "tok", "/upstream/copilot/chat/completions", chatBody).Code)
	require.Len(t, f.ledger, 1)
	require.Equal(t, "priced", f.ledger[0].CostSource)
	require.Equal(t, int64(7*1+11*2), f.ledger[0].CostCents)
}

func TestPricedPairGateDeniesUnpricedModelUnderCentsBudget(t *testing.T) {
	f := newFakeStore()
	f.addToken("tok", store.Credential{Name: "hello", CapCents: i64(100)})
	up, _, _ := newUpstream(t)
	mux := proxy.NewDataMux(testDeps(f, map[string]config.Upstream{
		"copilot": {Protocol: config.ProtocolChatCompletions, BaseURL: up.URL, Path: "chat/completions", Classification: config.ClassMetered},
	}))
	w := doChat(t, mux, "tok", "/upstream/copilot/chat/completions", chatBody)
	require.Equal(t, 403, w.Code)
	require.Contains(t, w.Body.String(), "no configured price")
	require.Equal(t, "denied", f.ledger[0].CostSource)
}

func TestBudgetExhaustionFailsClosedWith429(t *testing.T) {
	f := newFakeStore()
	f.addToken("tok", store.Credential{Name: "hello", CapTokens: i64(10)})
	f.monthToks = 10
	up, _, _ := newUpstream(t)
	mux := proxy.NewDataMux(testDeps(f, map[string]config.Upstream{
		"ollama": {Protocol: config.ProtocolChatCompletions, BaseURL: up.URL, Path: "v1/chat/completions", Classification: config.ClassFree},
	}))
	w := doChat(t, mux, "tok", "/upstream/ollama/v1/chat/completions", chatBody)
	require.Equal(t, 429, w.Code)
	require.Contains(t, w.Body.String(), "token budget reached")
	require.Len(t, f.ledger, 1)
	require.Equal(t, "denied", f.ledger[0].CostSource)
	require.Equal(t, 429, f.ledger[0].Status)
}

func TestMeterStoreErrorFailsClosedWith403(t *testing.T) {
	f := newFakeStore()
	f.addToken("tok", store.Credential{Name: "hello", CapTokens: i64(10)})
	f.monthErr = errors.New("db down")
	up, _, _ := newUpstream(t)
	mux := proxy.NewDataMux(testDeps(f, map[string]config.Upstream{
		"ollama": {Protocol: config.ProtocolChatCompletions, BaseURL: up.URL, Path: "v1/chat/completions", Classification: config.ClassFree},
	}))
	require.Equal(t, 403, doChat(t, mux, "tok", "/upstream/ollama/v1/chat/completions", chatBody).Code)
}

func TestMissingUpstreamCredentialFailsClosed(t *testing.T) {
	f := newFakeStore()
	f.addToken("tok", store.Credential{Name: "hello"})
	up, _, _ := newUpstream(t)
	mux := proxy.NewDataMux(testDeps(f, map[string]config.Upstream{
		"copilot": {Protocol: config.ProtocolChatCompletions, BaseURL: up.URL, Path: "chat/completions", Classification: config.ClassMetered,
			CredentialFile: "/nonexistent/cred"},
	}))
	w := doChat(t, mux, "tok", "/upstream/copilot/chat/completions", chatBody)
	require.Equal(t, 503, w.Code)
	require.Equal(t, "denied", f.ledger[0].CostSource)
}

func TestUpstreamFailureIsStillLedgered(t *testing.T) {
	f := newFakeStore()
	f.addToken("tok", store.Credential{Name: "hello"})
	mux := proxy.NewDataMux(testDeps(f, map[string]config.Upstream{
		"ollama": {Protocol: config.ProtocolChatCompletions, BaseURL: "http://127.0.0.1:1", Path: "v1/chat/completions", Classification: config.ClassFree},
	}))
	w := doChat(t, mux, "tok", "/upstream/ollama/v1/chat/completions", chatBody)
	require.Equal(t, 502, w.Code)
	require.Len(t, f.ledger, 1, "spend recording must precede honoring failures")
	require.Equal(t, 502, f.ledger[0].Status)
}

func TestLedgerWriteFailureTripsFailClosedUntilRecovery(t *testing.T) {
	f := newFakeStore()
	f.addToken("tok", store.Credential{Name: "hello"})
	up, _, _ := newUpstream(t)
	mux := proxy.NewDataMux(testDeps(f, map[string]config.Upstream{
		"ollama": {Protocol: config.ProtocolChatCompletions, BaseURL: up.URL, Path: "v1/chat/completions", Classification: config.ClassFree},
	}))
	// First call forwards, but its ledger write fails -> plane trips.
	f.ledgerErr = errors.New("disk full")
	require.Equal(t, 200, doChat(t, mux, "tok", "/upstream/ollama/v1/chat/completions", chatBody).Code)
	// Tripped: budgets can no longer be enforced, so traffic is denied.
	require.Equal(t, 503, doChat(t, mux, "tok", "/upstream/ollama/v1/chat/completions", chatBody).Code)
	// Ledger recovers; the denied request's own record was the probe (it
	// failed above), so the next request's denial record succeeds and
	// clears the trip, and the one after forwards again.
	f.ledgerErr = nil
	require.Equal(t, 503, doChat(t, mux, "tok", "/upstream/ollama/v1/chat/completions", chatBody).Code)
	require.Equal(t, 200, doChat(t, mux, "tok", "/upstream/ollama/v1/chat/completions", chatBody).Code)
}

func TestStreamingInjectsIncludeUsageAndCapturesUsage(t *testing.T) {
	f := newFakeStore()
	f.addToken("tok", store.Credential{Name: "hello"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var m map[string]any
		require.NoError(t, json.Unmarshal(body, &m))
		opts, _ := m["stream_options"].(map[string]any)
		require.Equal(t, true, opts["include_usage"], "proxy must request the usage chunk")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":5}}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	mux := proxy.NewDataMux(testDeps(f, map[string]config.Upstream{
		"ollama": {Protocol: config.ProtocolChatCompletions, BaseURL: srv.URL, Path: "v1/chat/completions", Classification: config.ClassFree},
	}))
	body := `{"model": "test-model", "stream": true, "messages": []}`
	w := doChat(t, mux, "tok", "/upstream/ollama/v1/chat/completions", body)
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), "data: [DONE]")
	require.Len(t, f.ledger, 1)
	require.Equal(t, int64(3), f.ledger[0].InputTokens)
	require.Equal(t, int64(5), f.ledger[0].OutputTokens)
}

func TestRedirectIsNotFollowed(t *testing.T) {
	f := newFakeStore()
	f.addToken("tok", store.Credential{Name: "hello"})
	var followed bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		followed = true
	}))
	t.Cleanup(target.Close)
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirector.Close)
	mux := proxy.NewDataMux(testDeps(f, map[string]config.Upstream{
		"ollama": {Protocol: config.ProtocolChatCompletions, BaseURL: redirector.URL, Path: "v1/chat/completions", Classification: config.ClassFree},
	}))
	w := doChat(t, mux, "tok", "/upstream/ollama/v1/chat/completions", chatBody)
	require.Equal(t, http.StatusTemporaryRedirect, w.Code, "3xx must surface, not be followed")
	require.False(t, followed, "a keyed call must never follow a redirect")
}

func TestBudgetDenialHasNoApprovalAdvice(t *testing.T) {
	f := newFakeStore()
	f.addToken("tok", store.Credential{Name: "hello", CapTokens: i64(0)})
	up, got, _ := newUpstream(t)
	mux := proxy.NewDataMux(testDeps(f, map[string]config.Upstream{
		"ollama": {Protocol: config.ProtocolChatCompletions, BaseURL: up.URL, Path: "v1/chat/completions", Classification: config.ClassFree},
	}))
	for range 3 {
		w := doChat(t, mux, "tok", "/upstream/ollama/v1/chat/completions", chatBody)
		require.Equal(t, http.StatusTooManyRequests, w.Code)
		require.Equal(t, "monthly token budget reached\n", w.Body.String())
	}
	require.Empty(t, got.Method, "denials never reach the upstream")
	require.Len(t, f.ledger, 3, "denials still belong in the spend ledger")
	require.Empty(t, f.open)
}

func TestOnlyPostChatRouteExists(t *testing.T) {
	f := newFakeStore()
	f.addToken("tok", store.Credential{Name: "hello"})
	mux := proxy.NewDataMux(testDeps(f, map[string]config.Upstream{}))
	req := httptest.NewRequestWithContext(context.Background(), "GET", "/upstream/ollama/v1/models", nil)
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	require.Equal(t, 405, w.Code)
}

func TestAdmittedCallHoldsUntilItsLedgerWrite(t *testing.T) {
	// Under a cap the admission leaves a reservation; the ledger
	// write of the SAME call consumes it — on success and on a refusal
	// taken after admission alike — so nothing stays held once the call
	// is recorded, and a credential with no caps never holds.
	f := newFakeStore()
	f.addToken("tok", store.Credential{Name: "hello", CapTokens: i64(10)})
	f.addToken("free", store.Credential{Name: "uncapped"})
	up, _, _ := newUpstream(t)
	mux := proxy.NewDataMux(testDeps(f, map[string]config.Upstream{
		"ollama": {Protocol: config.ProtocolChatCompletions, BaseURL: up.URL, Path: "v1/chat/completions", Classification: config.ClassFree},
		"copilot": {Protocol: config.ProtocolChatCompletions, BaseURL: up.URL, Path: "v1/chat/completions", Classification: config.ClassMetered,
			CredentialFile: "/nonexistent/cred"},
	}))
	require.Equal(t, 200, doChat(t, mux, "tok", "/upstream/ollama/v1/chat/completions", chatBody).Code)
	require.Equal(t, []string{"res-1"}, f.consumed, "the allowed call's row consumed its hold")
	require.Empty(t, f.open)

	// Admitted, then refused because the upstream credential is missing:
	// the denied row releases the hold.
	require.Equal(t, 503, doChat(t, mux, "tok", "/upstream/copilot/v1/chat/completions", chatBody).Code)
	require.Equal(t, []string{"res-1", "res-2"}, f.consumed)
	require.Empty(t, f.open)

	// No caps: nothing reserved, nothing consumed.
	require.Equal(t, 200, doChat(t, mux, "free", "/upstream/ollama/v1/chat/completions", chatBody).Code)
	require.Len(t, f.consumed, 2)
}
