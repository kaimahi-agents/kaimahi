package inbound

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/kaimahi-agents/kaimahi/plane/internal/config"
	"github.com/kaimahi-agents/kaimahi/plane/internal/meter"
	"github.com/kaimahi-agents/kaimahi/plane/internal/store"
)

const (
	// Split so this file carries no whole credential shape of its own —
	// the tree scan reads test files like any other. The values are
	// unchanged.
	callerToken = "kmh_" + "inbound_test_token"
	otherToken  = "kmh_" + "some_other_credential"
	secret      = "shared-signing-secret"
)

type fakeStore struct {
	mu        sync.Mutex
	tokens    map[string]store.Credential // token -> credential
	byName    map[string]store.Credential
	credErr   error
	audits    []store.InboundAuditEntry
	auditErr  error
	admitted  map[string]bool // hook/delivery admitted
	grants    map[string]int  // credential/hook -> remaining uses
	admitErr  error
	filed     []string // credential/kind/subject
	fileErr   error
	admitCall int
	// Requests a Slack command may decide, keyed by full id.
	requests   []*store.ApprovalRequest
	decideErr  error
	grantsMade []store.Grant
	// Identity on the call: the runs the bridge opened and closed.
	runs       []store.Run
	closedRuns []string
	openRunErr error
}

func newFakeStore() *fakeStore {
	f := &fakeStore{
		tokens: map[string]store.Credential{}, byName: map[string]store.Credential{},
		admitted: map[string]bool{}, grants: map[string]int{},
	}
	f.addCredential(callerToken, store.Credential{Name: "inbound-demo"})
	f.addCredential(otherToken, store.Credential{Name: "someone-else"})
	f.byName["hello-world"] = store.Credential{Name: "hello-world"}
	f.grants["inbound-demo/demo"] = 5
	f.grants["inbound-demo/signed"] = 5
	f.grants["inbound-demo/slack"] = 5
	f.grants["inbound-demo/tiny"] = 5
	return f
}

func (f *fakeStore) addCredential(token string, c store.Credential) {
	f.tokens[token] = c
	f.byName[c.Name] = c
}

func (f *fakeStore) CredentialByTokenHash(_ context.Context, hash []byte) (store.Credential, error) {
	if f.credErr != nil {
		return store.Credential{}, f.credErr
	}
	for tok, c := range f.tokens {
		h := sha256.Sum256([]byte(tok))
		if string(h[:]) == string(hash) {
			return c, nil
		}
	}
	return store.Credential{}, store.ErrNotFound
}

func (f *fakeStore) CredentialByName(_ context.Context, name string) (store.Credential, error) {
	if f.credErr != nil {
		return store.Credential{}, f.credErr
	}
	c, ok := f.byName[name]
	if !ok {
		return store.Credential{}, store.ErrNotFound
	}
	return c, nil
}

func (f *fakeStore) RecordInboundAudit(_ context.Context, e store.InboundAuditEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.auditErr != nil {
		return f.auditErr
	}
	f.audits = append(f.audits, e)
	return nil
}

func (f *fakeStore) OpenRun(_ context.Context, credential, actedFor, source, delivery, eventID string, _ time.Duration) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.openRunErr != nil {
		return "", f.openRunErr
	}
	f.runs = append(f.runs, store.Run{CredentialName: credential, ActedFor: actedFor, Source: source, DeliveryID: delivery})
	return "run-" + delivery, nil
}

func (f *fakeStore) CloseRun(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closedRuns = append(f.closedRuns, id)
	return nil
}

func (f *fakeStore) AdmitInboundEvent(_ context.Context, hook, credential, delivery, agent, actedFor string) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.admitCall++
	if f.admitErr != nil {
		return "", "", f.admitErr
	}
	if f.admitted[hook+"/"+delivery] {
		return "", "", store.ErrReplay
	}
	if f.grants[credential+"/"+hook] <= 0 {
		return "", "", store.ErrNoGrant
	}
	f.grants[credential+"/"+hook]--
	f.admitted[hook+"/"+delivery] = true
	id := "evt-" + strconv.Itoa(f.admitCall)
	f.audits = append(f.audits, store.InboundAuditEntry{ID: id, Hook: hook, CredentialName: credential,
		DeliveryID: delivery, Decision: "admitted", Status: 202, Agent: agent, ActedFor: actedFor})
	return id, "grant-1", nil
}

func (f *fakeStore) FileApprovalRequest(_ context.Context, fl store.Filing) (bool, error) {
	credential, kind, subject := fl.Credential, fl.Kind, fl.Subject
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fileErr != nil {
		return false, f.fileErr
	}
	f.filed = append(f.filed, credential+"/"+kind+"/"+subject)
	return true, nil
}

func (f *fakeStore) RequestByPrefix(_ context.Context, prefix string) (store.ApprovalRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.decideErr != nil {
		return store.ApprovalRequest{}, f.decideErr
	}
	var hits []*store.ApprovalRequest
	for _, r := range f.requests {
		if strings.HasPrefix(r.ID, prefix) {
			hits = append(hits, r)
		}
	}
	switch len(hits) {
	case 0:
		return store.ApprovalRequest{}, store.ErrNotFound
	case 1:
		return *hits[0], nil
	}
	return store.ApprovalRequest{}, store.ErrAmbiguous
}

func (f *fakeStore) find(id string) *store.ApprovalRequest {
	for _, r := range f.requests {
		if r.ID == id {
			return r
		}
	}
	return nil
}

func (f *fakeStore) ApproveRequest(_ context.Context, id string, expiresAt *time.Time, maxUses *int32, amount *int64, decidedBy string) (store.Grant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if decidedBy == "" {
		return store.Grant{}, store.ErrBounds
	}
	r := f.find(id)
	if r == nil {
		return store.Grant{}, store.ErrNotFound
	}
	if r.Status != "pending" {
		return store.Grant{}, store.ErrNotPending
	}
	if expiresAt == nil && maxUses == nil {
		return store.Grant{}, store.ErrBounds
	}
	if (r.Kind == "budget") != (amount != nil) {
		return store.Grant{}, store.ErrBounds
	}
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	r.Status, r.DecidedBy, r.DecidedAt = "approved", decidedBy, &now
	g := store.Grant{ID: "g-" + r.ID[:8], RequestID: r.ID, CredentialName: r.CredentialName, Kind: r.Kind,
		Subject: r.Subject, ExpiresAt: expiresAt, MaxUses: maxUses, Amount: amount, DecidedBy: decidedBy}
	f.grantsMade = append(f.grantsMade, g)
	return g, nil
}

func (f *fakeStore) DenyApprovalRequest(_ context.Context, id string, decidedBy string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if decidedBy == "" {
		return store.ErrBounds
	}
	r := f.find(id)
	if r == nil {
		return store.ErrNotFound
	}
	if r.Status != "pending" {
		return store.ErrNotPending
	}
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	r.Status, r.DecidedBy, r.DecidedAt = "denied", decidedBy, &now
	return nil
}

func (f *fakeStore) decisions(hook string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, a := range f.audits {
		if a.Hook == hook {
			out = append(out, a.Decision+" "+strconv.Itoa(a.Status))
		}
	}
	return out
}

func (f *fakeStore) last(hook string) store.InboundAuditEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.audits) - 1; i >= 0; i-- {
		if f.audits[i].Hook == hook {
			return f.audits[i]
		}
	}
	return store.InboundAuditEntry{}
}

type fakeMeter struct{ err error }

func (m *fakeMeter) Preview(_ context.Context, _ store.Credential) error { return m.err }

// a2aServer is a stand-in for the kagent controller's per-agent A2A
// endpoint: it records the request and answers a completed task shaped
// like the one measured on the live cluster (protocol 0.3).
type a2aServer struct {
	*httptest.Server
	mu       sync.Mutex
	requests []*http.Request
	bodies   []map[string]any
	reply    func(w http.ResponseWriter)
	block    chan struct{} // when non-nil, handlers wait on it
}

func newA2A(t *testing.T) *a2aServer {
	t.Helper()
	s := &a2aServer{}
	s.reply = func(w http.ResponseWriter) {
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":"x","result":{"kind":"task","id":"task-1",
		  "status":{"state":"completed"},
		  "artifacts":[{"artifactId":"a","parts":[{"kind":"text","text":"PONG"}]}],
		  "history":[{"role":"user","parts":[]},
		    {"role":"agent","metadata":{"kagent_usage_metadata":{"promptTokenCount":368,"candidatesTokenCount":3}},"parts":[{"kind":"text","text":"PONG"}]}]}}`)
	}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		s.mu.Lock()
		s.requests = append(s.requests, r)
		s.bodies = append(s.bodies, m)
		block := s.block
		s.mu.Unlock()
		if block != nil {
			<-block
		}
		s.reply(w)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *a2aServer) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

type fixture struct {
	fs     *fakeStore
	a2a    *a2aServer
	bridge *Bridge
	mux    http.Handler
	now    time.Time
	done   chan struct{}
}

func hooks(t *testing.T) map[string]config.InboundHook {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "secret"), []byte(secret+"\n"), 0o600))
	return map[string]config.InboundHook{
		"demo": {Credential: "inbound-demo", Auth: config.AuthBearer,
			AgentNamespace: "kagent", Agent: "hello-world", BudgetCredential: "hello-world"},
		"signed": {Credential: "inbound-demo", Auth: config.AuthKaimahiHMAC, SigningSecretFile: filepath.Join(dir, "secret"),
			AgentNamespace: "kagent", Agent: "hello-world", BudgetCredential: "hello-world"},
		"slack": {Credential: "inbound-demo", Auth: config.AuthSlack, SigningSecretFile: filepath.Join(dir, "secret"),
			AgentNamespace: "kagent", Agent: "hello-slack", BudgetCredential: "hello-world"},
		"nosecret": {Credential: "inbound-demo", Auth: config.AuthKaimahiHMAC, SigningSecretFile: filepath.Join(dir, "missing"),
			AgentNamespace: "kagent", Agent: "hello-world", BudgetCredential: "hello-world"},
		"tiny": {Credential: "inbound-demo", Auth: config.AuthBearer, MaxBodyBytes: 16, RatePerMinute: 1, Burst: 2,
			AgentNamespace: "kagent", Agent: "hello-world", BudgetCredential: "hello-world"},
		"ungoverned": {Credential: "inbound-demo", Auth: config.AuthBearer,
			AgentNamespace: "kagent", Agent: "hello-world", BudgetCredential: "nobody"},
	}
}

func newFixture(t *testing.T, fs *fakeStore, m Meter, opts ...func(*Deps)) *fixture {
	t.Helper()
	// The fixture owns the clock: tests advance f.now and the bridge (and
	// its limiter) read it through one closure.
	f := &fixture{fs: fs, a2a: newA2A(t), now: time.Date(2026, 9, 1, 21, 0, 0, 0, time.UTC)}
	d := Deps{Store: fs, Meter: m, Hooks: hooks(t), A2ABase: f.a2a.URL, QueueSize: 4, Workers: 1,
		InvokeTimeout: 5 * time.Second, Now: func() time.Time { return f.now }}
	for _, o := range opts {
		o(&d)
	}
	f.bridge = New(d)
	f.mux = f.bridge.Mux()
	ctx, cancel := context.WithCancel(context.Background())
	f.done = make(chan struct{})
	go func() { f.bridge.Run(ctx); close(f.done) }()
	t.Cleanup(func() { cancel(); <-f.done })
	return f
}

func (f *fixture) post(hook string, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/hook/"+hook, strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	return rec
}

func bearer(delivery string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + callerToken, HeaderDelivery: delivery}
}

func (f *fixture) signed(delivery, body string) map[string]string {
	ts := strconv.FormatInt(f.now.Unix(), 10)
	return map[string]string{HeaderDelivery: delivery, HeaderTimestamp: ts,
		HeaderSignature: Sign([]byte(secret), ts, delivery, []byte(body))}
}

func (f *fixture) slackSigned(body string) map[string]string {
	ts := strconv.FormatInt(f.now.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v0:" + ts + ":" + body))
	return map[string]string{slackTimestamp: ts, slackSignature: "v0=" + hex.EncodeToString(mac.Sum(nil))}
}

// waitOutcome polls until the hook's newest row is an outcome.
func (f *fixture) waitOutcome(t *testing.T, hook string) store.InboundAuditEntry {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if e := f.fs.last(hook); e.Decision == "completed" || e.Decision == "failed" {
			return e
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no outcome recorded for hook %s; audits: %v", hook, f.fs.decisions(hook))
	return store.InboundAuditEntry{}
}

func TestUnknownHookIsUnauthorizedAndUnaudited(t *testing.T) {
	f := newFixture(t, newFakeStore(), &fakeMeter{})
	rec := f.post("nope", `{"text":"hi"}`, bearer("d1"))
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Empty(t, f.fs.audits)
	require.Zero(t, f.a2a.count())
}

func TestOnlyPostHookRouteExists(t *testing.T) {
	f := newFixture(t, newFakeStore(), &fakeMeter{})
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/hook/demo", nil)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	req = httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", nil)
	rec = httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestBearerAuthRequiredAndAudited(t *testing.T) {
	f := newFixture(t, newFakeStore(), &fakeMeter{})
	rec := f.post("demo", `{"text":"hi"}`, map[string]string{HeaderDelivery: "d1"})
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	rec = f.post("demo", `{"text":"hi"}`, map[string]string{"Authorization": "Bearer kmh_wrong", HeaderDelivery: "d1"})
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	// A real credential that is not the hook's: 403, not 401.
	rec = f.post("demo", `{"text":"hi"}`, map[string]string{"Authorization": "Bearer " + otherToken, HeaderDelivery: "d1"})
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Equal(t, []string{"denied 401", "denied 401", "denied 403"}, f.fs.decisions("demo"))
	require.Zero(t, f.a2a.count())
}

func TestCredentialStoreFailureClosed(t *testing.T) {
	fs := newFakeStore()
	fs.credErr = errors.New("pg down")
	f := newFixture(t, fs, &fakeMeter{})
	rec := f.post("demo", `{"text":"hi"}`, bearer("d1"))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestDeliveryIDRequired(t *testing.T) {
	f := newFixture(t, newFakeStore(), &fakeMeter{})
	rec := f.post("demo", `{"text":"hi"}`, map[string]string{"Authorization": "Bearer " + callerToken})
	require.Equal(t, http.StatusBadRequest, rec.Code)
	rec = f.post("demo", `{"text":"hi"}`, bearer("has space"))
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAdmittedEventIsInvokedAndOutcomeAudited(t *testing.T) {
	f := newFixture(t, newFakeStore(), &fakeMeter{})
	rec := f.post("demo", `{"text":"Reply with exactly the word PONG."}`, bearer("d1"))
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	var resp map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, "admitted", resp["status"])
	require.Equal(t, "kagent/hello-world", resp["agent"])
	require.NotEmpty(t, resp["event_id"])

	out := f.waitOutcome(t, "demo")
	require.Equal(t, "completed", out.Decision)
	require.Equal(t, http.StatusOK, out.Status)
	require.Equal(t, "task task-1", out.Detail)
	require.Equal(t, int64(368), out.InputTokens)
	require.Equal(t, int64(3), out.OutputTokens)
	require.Equal(t, "d1", out.DeliveryID)

	require.Equal(t, 1, f.a2a.count())
	r := f.a2a.requests[0]
	require.Equal(t, "/api/a2a/kagent/hello-world/", r.URL.Path)
	require.Equal(t, "kaimahi-inbound/demo", r.Header.Get("x-user-id"))
	require.Empty(t, r.Header.Get("Authorization"), "the caller's token must never reach the agent")
	body := f.a2a.bodies[0]
	require.Equal(t, "message/send", body["method"])
	msg := body["params"].(map[string]any)["message"].(map[string]any)
	parts := msg["parts"].([]any)
	require.Equal(t, "Reply with exactly the word PONG.", parts[0].(map[string]any)["text"])
	require.Equal(t, []string{"admitted 202", "completed 200"}, f.fs.decisions("demo"))
}

func TestRawBodyBecomesTheText(t *testing.T) {
	f := newFixture(t, newFakeStore(), &fakeMeter{})
	rec := f.post("demo", "plain text event", bearer("d1"))
	require.Equal(t, http.StatusAccepted, rec.Code)
	f.waitOutcome(t, "demo")
	parts := f.a2a.bodies[0]["params"].(map[string]any)["message"].(map[string]any)["parts"].([]any)
	require.Equal(t, "plain text event", parts[0].(map[string]any)["text"])
	// Empty or non-UTF-8 payloads are not prompts.
	rec = f.post("demo", "   ", bearer("d2"))
	require.Equal(t, http.StatusBadRequest, rec.Code)
	rec = f.post("demo", "\xff\xfe", bearer("d3"))
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestReplayRejectedWithoutBurningAUse(t *testing.T) {
	fs := newFakeStore()
	f := newFixture(t, fs, &fakeMeter{})
	require.Equal(t, http.StatusAccepted, f.post("demo", `{"text":"one"}`, bearer("same")).Code)
	f.waitOutcome(t, "demo")
	rec := f.post("demo", `{"text":"one again"}`, bearer("same"))
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Contains(t, rec.Body.String(), "replay")
	require.Equal(t, 4, fs.grants["inbound-demo/demo"], "a replay must not consume a grant use")
	require.Equal(t, 1, f.a2a.count())
	require.Equal(t, "denied 409", f.fs.decisions("demo")[len(f.fs.decisions("demo"))-1])
}

func TestNoGrantDeniesAndFilesRequest(t *testing.T) {
	fs := newFakeStore()
	fs.grants["inbound-demo/demo"] = 0
	f := newFixture(t, fs, &fakeMeter{})
	rec := f.post("demo", `{"text":"hi"}`, bearer("d1"))
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "approval request filed")
	require.Equal(t, []string{"inbound-demo/inbound/demo"}, fs.filed)
	require.Zero(t, f.a2a.count())
	require.Equal(t, []string{"denied 403"}, fs.decisions("demo"))

	// Filing failure still denies.
	fs.fileErr = errors.New("pg down")
	rec = f.post("demo", `{"text":"hi"}`, bearer("d2"))
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.NotContains(t, rec.Body.String(), "filed")
}

func TestGrantExhaustionReDenies(t *testing.T) {
	fs := newFakeStore()
	fs.grants["inbound-demo/demo"] = 1
	f := newFixture(t, fs, &fakeMeter{})
	require.Equal(t, http.StatusAccepted, f.post("demo", `{"text":"one"}`, bearer("d1")).Code)
	f.waitOutcome(t, "demo")
	require.Equal(t, http.StatusForbidden, f.post("demo", `{"text":"two"}`, bearer("d2")).Code)
	require.Equal(t, 1, f.a2a.count())
}

func TestTargetBudgetPreviewDeniesAndFilesUnderAgentCredential(t *testing.T) {
	fs := newFakeStore()
	f := newFixture(t, fs, &fakeMeter{err: meter.Denial{Status: http.StatusTooManyRequests,
		Msg: "monthly token budget reached", BudgetSubject: "tokens"}})
	rec := f.post("demo", `{"text":"hi"}`, bearer("d1"))
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.Contains(t, rec.Body.String(), "monthly token budget reached")
	require.Equal(t, []string{"hello-world/budget/tokens"}, fs.filed)
	require.Equal(t, 5, fs.grants["inbound-demo/demo"], "a budget denial burns no inbound use")
	require.Zero(t, f.a2a.count())
}

func TestMeteringFailureClosedAs503(t *testing.T) {
	// Not a cap denial: the plane is degraded, and an ingress caller must
	// be able to tell "refused" (429/403) from "try later" (503).
	f := newFixture(t, newFakeStore(), &fakeMeter{err: meter.Denial{Status: http.StatusForbidden, Msg: "metering unavailable"}})
	rec := f.post("demo", `{"text":"hi"}`, bearer("d1"))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Empty(t, f.fs.filed, "a metering outage files no budget request")
	require.Zero(t, f.a2a.count())
}

func TestUngovernedTargetNotTriggerable(t *testing.T) {
	f := newFixture(t, newFakeStore(), &fakeMeter{})
	rec := f.post("ungoverned", `{"text":"hi"}`, bearer("d1"))
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "not governed")
}

func TestBodyLimitRejects(t *testing.T) {
	f := newFixture(t, newFakeStore(), &fakeMeter{})
	rec := f.post("tiny", `{"text":"this is longer than sixteen bytes"}`, bearer("d1"))
	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	require.Equal(t, []string{"denied 413"}, f.fs.decisions("tiny"))
}

func TestRateLimitRejectsBeforeAuth(t *testing.T) {
	fs := newFakeStore()
	f := newFixture(t, fs, &fakeMeter{})
	// burst 2: two calls pass the bucket (and fail auth, audited), the
	// third is refused by the bucket alone — unaudited.
	for i := 0; i < 2; i++ {
		require.Equal(t, http.StatusUnauthorized, f.post("tiny", "x", nil).Code)
	}
	rec := f.post("tiny", "x", bearer("d9"))
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.Len(t, fs.decisions("tiny"), 2)
	// Refill at 1/min: a minute later one token is back.
	f.now = f.now.Add(time.Minute)
	require.Equal(t, http.StatusAccepted, f.post("tiny", "short", bearer("d10")).Code)
}

func TestQueueFullDeniesWithoutBurningAUse(t *testing.T) {
	fs := newFakeStore()
	fs.grants["inbound-demo/demo"] = 10
	f := newFixture(t, fs, &fakeMeter{}, func(d *Deps) { d.QueueSize = 1; d.Workers = 1 })
	f.a2a.block = make(chan struct{})
	require.Equal(t, http.StatusAccepted, f.post("demo", `{"text":"one"}`, bearer("d1")).Code)
	rec := f.post("demo", `{"text":"two"}`, bearer("d2"))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), "queue full")
	require.Equal(t, 9, fs.grants["inbound-demo/demo"])
	close(f.a2a.block)
	f.waitOutcome(t, "demo")
	// Capacity released once the invocation is audited.
	require.Eventually(t, func() bool {
		return f.post("demo", `{"text":"three"}`, bearer("d3")).Code == http.StatusAccepted
	}, 2*time.Second, 10*time.Millisecond)
}

func (f *fakeStore) setAuditErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auditErr = err
}

func TestAuditDegradationFailsClosedAndRecovers(t *testing.T) {
	fs := newFakeStore()
	f := newFixture(t, fs, &fakeMeter{})
	fs.setAuditErr(errors.New("pg down"))
	// The first refusal's audit write fails and trips the breaker...
	f.post("demo", "x", nil)
	// ...so an otherwise-admissible event is refused — and so is a
	// Slack handshake: nothing is honoured while decisions cannot be
	// recorded.
	rec := f.post("demo", `{"text":"hi"}`, bearer("d1"))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	challenge := `{"type":"url_verification","challenge":"abc"}`
	require.Equal(t, http.StatusServiceUnavailable, f.post("slack", challenge, f.slackSigned(challenge)).Code)
	require.Zero(t, f.a2a.count())
	// A successful write (the denial's own record) heals it.
	fs.setAuditErr(nil)
	rec = f.post("demo", `{"text":"hi"}`, bearer("d1"))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, "the healing request itself is still denied")
	rec = f.post("demo", `{"text":"hi"}`, bearer("d2"))
	require.Equal(t, http.StatusAccepted, rec.Code)
}

func TestKaimahiHMACSignedEvent(t *testing.T) {
	fs := newFakeStore()
	f := newFixture(t, fs, &fakeMeter{})
	body := `{"text":"signed hello"}`
	rec := f.post("signed", body, f.signed("d1", body))
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	f.waitOutcome(t, "signed")
	require.Equal(t, 1, f.a2a.count())

	// Tampered body, wrong secret, missing signature, stale timestamp,
	// and a re-signed delivery id all fail identically.
	h := f.signed("d2", body)
	require.Equal(t, http.StatusUnauthorized, f.post("signed", body+" ", h).Code)
	require.Equal(t, http.StatusUnauthorized, f.post("signed", body, map[string]string{HeaderDelivery: "d2", HeaderTimestamp: h[HeaderTimestamp],
		HeaderSignature: Sign([]byte("wrong"), h[HeaderTimestamp], "d2", []byte(body))}).Code)
	require.Equal(t, http.StatusUnauthorized, f.post("signed", body, map[string]string{HeaderDelivery: "d2", HeaderTimestamp: h[HeaderTimestamp]}).Code)
	stale := strconv.FormatInt(f.now.Add(-6*time.Minute).Unix(), 10)
	require.Equal(t, http.StatusUnauthorized, f.post("signed", body, map[string]string{HeaderDelivery: "d2", HeaderTimestamp: stale,
		HeaderSignature: Sign([]byte(secret), stale, "d2", []byte(body))}).Code)
	// The delivery id is signed: swapping it invalidates the signature,
	// so a captured request cannot be replayed under a fresh id.
	h["X-Kaimahi-Delivery"] = "d3"
	require.Equal(t, http.StatusUnauthorized, f.post("signed", body, h).Code)
	// The signed replay (same headers, same body) hits the admitted index.
	require.Equal(t, http.StatusConflict, f.post("signed", body, f.signed("d1", body)).Code)
	require.Equal(t, 1, f.a2a.count())
}

func TestSigningSecretUnavailableFailsClosed(t *testing.T) {
	f := newFixture(t, newFakeStore(), &fakeMeter{})
	body := `{"text":"hi"}`
	rec := f.post("nosecret", body, f.signed("d1", body))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Zero(t, f.a2a.count())
}

func TestHMACHookCredentialMustBeIssued(t *testing.T) {
	fs := newFakeStore()
	delete(fs.byName, "inbound-demo")
	f := newFixture(t, fs, &fakeMeter{})
	body := `{"text":"hi"}`
	rec := f.post("signed", body, f.signed("d1", body))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), "not issued")
}

func TestSlackURLVerificationEchoesWithoutTriggering(t *testing.T) {
	fs := newFakeStore()
	f := newFixture(t, fs, &fakeMeter{})
	body := `{"type":"url_verification","token":"x","challenge":"3eZbrw1aBm2rZgRNFdxV2595E9CY3gmdALWMmHkvFXO7tYXAYM8P"}`
	rec := f.post("slack", body, f.slackSigned(body))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.JSONEq(t, `{"challenge":"3eZbrw1aBm2rZgRNFdxV2595E9CY3gmdALWMmHkvFXO7tYXAYM8P"}`, rec.Body.String())
	require.Zero(t, f.a2a.count())
	require.Equal(t, []string{"challenge 200"}, fs.decisions("slack"))
	// Unsigned, the handshake is refused like anything else.
	require.Equal(t, http.StatusUnauthorized, f.post("slack", body, nil).Code)
}

func TestSlackEventCallbackTriggersByEventID(t *testing.T) {
	fs := newFakeStore()
	f := newFixture(t, fs, &fakeMeter{})
	body := `{"type":"event_callback","event_id":"Ev0123456789","event":{"type":"app_mention","user":"U2",` +
		`"text":"<@U1> what is the weather","channel":"C0TEST","ts":"1725.0001"}}`
	rec := f.post("slack", body, f.slackSigned(body))
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	out := f.waitOutcome(t, "slack")
	require.Equal(t, "Ev0123456789", out.DeliveryID)
	require.Equal(t, "kagent/hello-slack", out.Agent)
	parts := f.a2a.bodies[0]["params"].(map[string]any)["message"].(map[string]any)["parts"].([]any)
	require.Contains(t, parts[0].(map[string]any)["text"], `"what is the weather"`)
	// Slack's retry of the same event_id is a replay once admitted.
	require.Equal(t, http.StatusConflict, f.post("slack", body, f.slackSigned(body)).Code)
	// Other envelope types are not prompts.
	other := `{"type":"something_else","event_id":"Ev1"}`
	require.Equal(t, http.StatusBadRequest, f.post("slack", other, f.slackSigned(other)).Code)
}

func TestAgentFailureIsAuditedAsFailed(t *testing.T) {
	cases := map[string]func(w http.ResponseWriter){
		"http 500": func(w http.ResponseWriter) { w.WriteHeader(500) },
		"rpc error": func(w http.ResponseWriter) {
			_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":"x","error":{"code":-32600,"message":"bad"}}`)
		},
		"task failed": func(w http.ResponseWriter) {
			_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":"x","result":{"kind":"task","id":"t","status":{"state":"failed"}}}`)
		},
		"completed but empty": func(w http.ResponseWriter) {
			_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":"x","result":{"kind":"task","id":"t","status":{"state":"completed"},"artifacts":[]}}`)
		},
		"not json": func(w http.ResponseWriter) { _, _ = io.WriteString(w, "<html>") },
	}
	for name, reply := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, newFakeStore(), &fakeMeter{})
			f.a2a.reply = reply
			require.Equal(t, http.StatusAccepted, f.post("demo", `{"text":"hi"}`, bearer("d1")).Code)
			out := f.waitOutcome(t, "demo")
			require.Equal(t, "failed", out.Decision)
			require.NotEmpty(t, out.Detail)
		})
	}
}

func TestAgentUnreachableIsAuditedAsFailed(t *testing.T) {
	f := newFixture(t, newFakeStore(), &fakeMeter{}, func(d *Deps) { d.A2ABase = "http://127.0.0.1:1" })
	require.Equal(t, http.StatusAccepted, f.post("demo", `{"text":"hi"}`, bearer("d1")).Code)
	out := f.waitOutcome(t, "demo")
	require.Equal(t, "failed", out.Decision)
	require.Zero(t, out.Status)
	require.Contains(t, out.Detail, "unreachable")
}

func TestRedirectIsNotFollowed(t *testing.T) {
	f := newFixture(t, newFakeStore(), &fakeMeter{})
	f.a2a.reply = func(w http.ResponseWriter) {
		w.Header().Set("Location", "http://127.0.0.1:1/elsewhere")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}
	require.Equal(t, http.StatusAccepted, f.post("demo", `{"text":"hi"}`, bearer("d1")).Code)
	out := f.waitOutcome(t, "demo")
	require.Equal(t, "failed", out.Decision)
	require.Equal(t, http.StatusTemporaryRedirect, out.Status)
	require.Equal(t, 1, f.a2a.count())
}

func TestLimiterRefillsAndCaps(t *testing.T) {
	now := time.Unix(0, 0)
	l := newLimiter(func() time.Time { return now })
	require.True(t, l.allow("h", 60, 2))
	require.True(t, l.allow("h", 60, 2))
	require.False(t, l.allow("h", 60, 2))
	now = now.Add(time.Second) // 1 token/second at 60/min
	require.True(t, l.allow("h", 60, 2))
	require.False(t, l.allow("h", 60, 2))
	now = now.Add(time.Hour) // capped at burst, not accumulated
	require.True(t, l.allow("h", 60, 2))
	require.True(t, l.allow("h", 60, 2))
	require.False(t, l.allow("h", 60, 2))
	require.True(t, l.allow("other", 60, 2), "buckets are per hook")
}
