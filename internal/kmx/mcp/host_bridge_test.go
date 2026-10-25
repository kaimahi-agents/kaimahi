package mcp

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type bridgePeer struct {
	t       *testing.T
	bridge  *HostBridge
	private ed25519.PrivateKey
	public  ed25519.PublicKey
}

func newBridgePeer(t *testing.T) *bridgePeer {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKIXPublicKey(public)
	bridge, err := OpenHostBridge(t.Context(), base64.StdEncoding.EncodeToString(der))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bridge.Close() })
	serverDER, err := base64.StdEncoding.DecodeString(bridge.Descriptor().PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	key, err := x509.ParsePKIXPublicKey(serverDER)
	if err != nil {
		t.Fatal(err)
	}
	return &bridgePeer{t, bridge, private, key.(ed25519.PublicKey)}
}
func (p *bridgePeer) request(method, path string, body []byte) *http.Request {
	p.t.Helper()
	req, err := http.NewRequestWithContext(p.t.Context(), method, p.bridge.Descriptor().URL+path, bytes.NewReader(body))
	if err != nil {
		p.t.Fatal(err)
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		p.t.Fatal(err)
	}
	req.Header.Set("X-Kmx-Time", strconv.FormatInt(time.Now().Unix(), 10))
	req.Header.Set("X-Kmx-Nonce", hex.EncodeToString(nonce))
	p.sign(req, body)
	return req
}
func (p *bridgePeer) sign(req *http.Request, body []byte) {
	canonical := req.Method + "\n" + req.URL.Path + "\n" + req.Header.Get("X-Kmx-Time") + "\n" + req.Header.Get("X-Kmx-Nonce") + "\n" + string(body)
	req.Header.Set("X-Kmx-Signature", base64.StdEncoding.EncodeToString(ed25519.Sign(p.private, []byte(canonical))))
}
func (p *bridgePeer) send(req *http.Request) (int, []byte) {
	p.t.Helper()
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		p.t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		p.t.Fatal(err)
	}
	signature, _ := base64.StdEncoding.DecodeString(res.Header.Get("X-Kmx-Signature"))
	canonical := fmt.Sprint(res.StatusCode) + "\n" + req.Header.Get("X-Kmx-Nonce") + "\n" + string(body)
	if !ed25519.Verify(p.public, []byte(canonical), signature) {
		p.t.Fatal("response not signed over exact status/nonce/body")
	}
	if bytes.HasSuffix(body, []byte("\n")) {
		p.t.Fatal("implicit JSON newline")
	}
	return res.StatusCode, body
}
func (p *bridgePeer) next() map[string]any {
	p.t.Helper()
	code, body := p.send(p.request("GET", "/next", nil))
	if code != 200 {
		p.t.Fatalf("next=%d", code)
	}
	var job map[string]any
	if json.Unmarshal(body, &job) != nil || job == nil {
		p.t.Fatalf("missing job: %s", body)
	}
	return job
}
func (p *bridgePeer) result(job map[string]any, value any) int {
	p.t.Helper()
	body, _ := json.Marshal(value)
	code, _ := p.send(p.request("POST", "/result/"+job["id"].(string), body))
	return code
}
func (p *bridgePeer) claim(job map[string]any) int {
	p.t.Helper()
	code, body := p.send(p.request("POST", "/claim/"+job["id"].(string), nil))
	if string(body) != "null" {
		p.t.Fatalf("claim response=%s", body)
	}
	return code
}
func bridgeDefinitions() []api.ToolDefinition {
	return []api.ToolDefinition{{Name: "lookup", Description: "Look up a value", InputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}}}
}

type bridgeResolved struct {
	host *localtools.Host
	err  error
}

func (p *bridgePeer) resolve(ctx context.Context) <-chan bridgeResolved {
	done := make(chan bridgeResolved, 1)
	go func() { host, err := p.bridge.ResolveTools(ctx, []string{"lookup"}); done <- bridgeResolved{host, err} }()
	return done
}
func awaitResolved(t *testing.T, done <-chan bridgeResolved) bridgeResolved {
	t.Helper()
	select {
	case got := <-done:
		return got
	case <-time.After(3 * time.Second):
		t.Fatal("resolve did not complete")
		return bridgeResolved{}
	}
}
func TestHostBridgeSharedSigningVector(t *testing.T) {
	raw, err := os.ReadFile("../../../testdata/host-bridge-signing.json")
	if err != nil {
		t.Fatal(err)
	}
	var vector struct {
		SeedByte                                                                        byte
		Method, Path, Time, Nonce, Body, PublicKey, RequestSignature, ResponseSignature string
		Status                                                                          int
	}
	if json.Unmarshal(raw, &vector) != nil {
		t.Fatal("invalid shared vector")
	}
	private := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{vector.SeedByte}, 32))
	public := private.Public().(ed25519.PublicKey)
	der, _ := x509.MarshalPKIXPublicKey(public)
	request := vector.Method + "\n" + vector.Path + "\n" + vector.Time + "\n" + vector.Nonce + "\n" + vector.Body
	if base64.StdEncoding.EncodeToString(der) != vector.PublicKey || base64.StdEncoding.EncodeToString(ed25519.Sign(private, []byte(request))) != vector.RequestSignature {
		t.Fatal("request signing differs from Node vector")
	}
	req := httptest.NewRequest(vector.Method, vector.Path, strings.NewReader(vector.Body))
	req.Header.Set("X-Kmx-Nonce", vector.Nonce)
	recorder := httptest.NewRecorder()
	(&HostBridge{private: private}).respond(recorder, req, vector.Status, json.RawMessage(vector.Body))
	if recorder.Body.String() != vector.Body || recorder.Header().Get("X-Kmx-Signature") != vector.ResponseSignature {
		t.Fatal("response canonical bytes differ from Node vector")
	}
}

func TestHostBridgeRequiresPinnedPublicKey(t *testing.T) {
	for _, key := range []string{"", "not a key", base64.StdEncoding.EncodeToString(make([]byte, 32))} {
		bridge, err := OpenHostBridge(t.Context(), key)
		if bridge != nil || err == nil || err.Error() != "host tools unavailable" {
			t.Fatalf("invalid public key created authority: %v %v", bridge, err)
		}
	}
}
func TestHostBridgeAuthenticationAndCorrelation(t *testing.T) {
	p := newBridgePeer(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := p.resolve(ctx)
	job := p.next()
	if job["kind"] != "resolve" || job["runId"] == "" || !reflect.DeepEqual(job["names"], []any{"lookup"}) {
		t.Fatalf("resolve identity: %v", job)
	}
	body, _ := json.Marshal(map[string]any{"definitions": bridgeDefinitions()})
	path := "/result/" + job["id"].(string)
	for _, mutation := range []string{"unsigned", "modified", "origin", "empty-origin", "host", "stale", "future", "nonce", "query", "oversize"} {
		t.Run(mutation, func(t *testing.T) {
			req := p.request("POST", path, body)
			switch mutation {
			case "unsigned":
				req.Header.Del("X-Kmx-Signature")
			case "modified":
				req.Body = io.NopCloser(bytes.NewReader([]byte(`{"error":true}`)))
				req.ContentLength = int64(len(`{"error":true}`))
			case "origin":
				req.Header.Set("Origin", "https://example.invalid")
			case "empty-origin":
				req.Header["Origin"] = []string{""}
			case "host":
				req.Host = "localhost"
			case "stale", "future":
				delta := -31
				if mutation == "future" {
					delta = 31
				}
				req.Header.Set("X-Kmx-Time", strconv.FormatInt(time.Now().Unix()+int64(delta), 10))
				p.sign(req, body)
			case "nonce":
				req.Header.Set("X-Kmx-Nonce", "bad")
				p.sign(req, body)
			case "query":
				req.URL.RawQuery = "x=1"
			case "oversize":
				huge := bytes.Repeat([]byte("x"), 65537)
				req = p.request("POST", path, huge)
			}
			if code, _ := p.send(req); code < 400 {
				t.Fatalf("accepted %s", mutation)
			}
			select {
			case <-done:
				t.Fatal("unauthorized body touched waiter")
			default:
			}
		})
	}
	req := p.request("POST", path, body)
	replay := p.request("POST", path, body)
	replay.Header = req.Header.Clone()
	if code, _ := p.send(req); code != 200 {
		t.Fatalf("valid result=%d", code)
	}
	got := awaitResolved(t, done)
	if got.err != nil || !got.host.Matches([]string{"lookup"}) {
		t.Fatalf("resolved=%v", got.err)
	}
	if code, _ := p.send(replay); code != 401 {
		t.Fatalf("replay=%d", code)
	}
	if code := p.result(job, map[string]any{"definitions": bridgeDefinitions()}); code != 410 {
		t.Fatalf("duplicate=%d", code)
	}
	if code, _ := p.send(p.request("POST", "/result/unknown", []byte(`{}`))); code != 410 {
		t.Fatalf("unknown=%d", code)
	}
}
func TestHostBridgeCallRequiresOneShotClaim(t *testing.T) {
	p := newBridgePeer(t)
	done := make(chan error, 1)
	go func() { done <- p.bridge.rpc(t.Context(), hostJob{Kind: "call"}, new(any)) }()
	job := p.next()
	path := "/claim/" + job["id"].(string)
	for _, mutation := range []string{"unsigned", "modified"} {
		req := p.request("POST", path, nil)
		if mutation == "unsigned" {
			req.Header.Del("X-Kmx-Signature")
		} else {
			req.Body = io.NopCloser(strings.NewReader("x"))
			req.ContentLength = 1
		}
		if code, _ := p.send(req); code != 401 {
			t.Fatalf("%s claim=%d", mutation, code)
		}
	}
	if code, _ := p.send(p.request("POST", path, []byte("x"))); code != 400 {
		t.Fatalf("nonempty claim=%d", code)
	}
	if code := p.result(job, map[string]any{"output": map[string]any{"ok": true}}); code != 410 {
		t.Fatalf("premature result=%d", code)
	}
	select {
	case err := <-done:
		t.Fatalf("premature result completed call: %v", err)
	default:
	}
	if code := p.claim(job); code != 200 {
		t.Fatalf("live claim=%d", code)
	}
	if code := p.claim(job); code != 410 {
		t.Fatalf("duplicate claim=%d", code)
	}
	if code := p.result(job, map[string]any{"output": map[string]any{"ok": true}}); code != 200 {
		t.Fatalf("claimed result=%d", code)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("claimed result did not complete call")
	}
	if code := p.claim(job); code != 410 {
		t.Fatalf("completed claim=%d", code)
	}
}

func TestHostBridgeClaimRequiresLivePendingContexts(t *testing.T) {
	for _, mode := range []string{"cancelled", "expired", "broker-cancelled"} {
		t.Run(mode, func(t *testing.T) {
			p := newBridgePeer(t)
			dead, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
			defer cancel()
			pending := &hostPending{ctx: dead, kind: "call", result: make(chan []byte, 1)}
			brokerCtx := t.Context()
			if mode != "expired" {
				dead, stop := context.WithCancel(t.Context())
				stop()
				pending.ctx = dead
			}
			if mode == "broker-cancelled" {
				brokerCtx, pending.ctx = pending.ctx, t.Context()
			}
			id := hostID()
			bridge := &HostBridge{descriptor: p.bridge.descriptor, public: p.bridge.public, private: p.bridge.private, ctx: brokerCtx, nonces: map[string]int64{}, pending: map[string]*hostPending{id: pending}}
			req := p.request("POST", "/claim/"+id, nil)
			req.RequestURI = req.URL.Path
			recorder := httptest.NewRecorder()
			bridge.serve(recorder, req)
			if recorder.Code != 410 || pending.claimed {
				t.Fatalf("nonlive claim=%d committed=%v", recorder.Code, pending.claimed)
			}
		})
	}
}

func TestHostBridgeClaimRejectsResolve(t *testing.T) {
	p := newBridgePeer(t)
	done := p.resolve(t.Context())
	job := p.next()
	if code := p.claim(job); code != 410 {
		t.Fatalf("resolve claim=%d", code)
	}
	if code := p.result(job, map[string]any{"definitions": bridgeDefinitions()}); code != 200 {
		t.Fatalf("resolve result=%d", code)
	}
	if got := awaitResolved(t, done); got.err != nil {
		t.Fatal(got.err)
	}
}

func TestHostBridgeClaimRejectsCancelledAndExpiredCall(t *testing.T) {
	for _, mode := range []string{"cancelled", "expired"} {
		t.Run(mode, func(t *testing.T) {
			p := newBridgePeer(t)
			ctx, cancel := context.WithCancel(t.Context())
			if mode == "expired" {
				cancel()
				ctx, cancel = context.WithTimeout(t.Context(), time.Second)
			}
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- p.bridge.rpc(ctx, hostJob{Kind: "call"}, new(any)) }()
			job := p.next()
			if mode == "cancelled" {
				cancel()
			}
			<-ctx.Done()
			select {
			case err := <-done:
				if err != errHostBridge {
					t.Fatalf("cancelled call=%v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("call did not finish cancellation")
			}
			p.bridge.mu.Lock()
			pending := p.bridge.pending[job["id"].(string)]
			p.bridge.mu.Unlock()
			if pending != nil {
				t.Fatal("cancelled response preceded pending removal")
			}
			if code := p.claim(job); code != 410 {
				t.Fatalf("%s claim=%d", mode, code)
			}
		})
	}
}

func TestHostBridgeFrozenCallAndCancellationIsolation(t *testing.T) {
	p := newBridgePeer(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := p.resolve(ctx)
	job := p.next()
	if p.result(job, map[string]any{"definitions": bridgeDefinitions()}) != 200 {
		t.Fatal("resolve post")
	}
	got := awaitResolved(t, done)
	if got.err != nil {
		t.Fatal(got.err)
	}
	callDone := make(chan error, 1)
	call := api.ToolCall{ID: "call_1", Tool: "lookup", Args: map[string]any{}, IdempotencyKey: "once", Mediation: api.MediationControllerMediated}
	go func() {
		result, err := got.host.Execute(ctx, controller.ToolCallContext{SessionUID: "session"}, call)
		if err == nil && (result.ID != "call_1" || result.Output["value"] != "found") {
			err = fmt.Errorf("incorrect result: %v", result)
		}
		callDone <- err
	}()
	callJob := p.next()
	if callJob["kind"] != "call" || callJob["runId"] != job["runId"] || callJob["name"] != "lookup" || callJob["callId"] != "call_1" || !reflect.DeepEqual(callJob["arguments"], map[string]any{}) {
		t.Fatalf("call projection: %v", callJob)
	}
	if code := p.claim(callJob); code != 200 {
		t.Fatalf("call claim=%d", code)
	}
	if p.result(callJob, map[string]any{"output": map[string]any{"value": "found"}}) != 200 {
		t.Fatal("call result")
	}
	if err := <-callDone; err != nil {
		t.Fatal(err)
	}
	cancel()
	closeJob := p.next()
	if closeJob["kind"] != "close" || closeJob["runId"] != job["runId"] {
		t.Fatalf("close=%v", closeJob)
	}
	abandonedCtx, abandon := context.WithCancel(t.Context())
	old := p.resolve(abandonedCtx)
	oldJob := p.next()
	abandon()
	if awaitResolved(t, old).err == nil {
		t.Fatal("cancelled resolve succeeded")
	}
	ctx2, cancel2 := context.WithCancel(t.Context())
	defer cancel2()
	fresh := p.resolve(ctx2)
	next := p.next()
	if next["kind"] == "close" {
		next = p.next()
	}
	if next["runId"] == oldJob["runId"] {
		t.Fatal("run IDs reused")
	}
	if code := p.result(oldJob, map[string]any{"definitions": bridgeDefinitions()}); code != 410 {
		t.Fatalf("late result=%d", code)
	}
	select {
	case <-fresh:
		t.Fatal("late result crossed run")
	default:
	}
	p.result(next, map[string]any{"definitions": bridgeDefinitions()})
	if awaitResolved(t, fresh).err != nil {
		t.Fatal("fresh result lost")
	}
}
func TestHostBridgeCancelledInvocationRejectsInFlightCall(t *testing.T) {
	p := newBridgePeer(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := p.resolve(ctx)
	job := p.next()
	p.result(job, map[string]any{"definitions": bridgeDefinitions()})
	got := awaitResolved(t, done)
	if got.err != nil {
		t.Fatal(got.err)
	}
	callDone := make(chan error, 1)
	go func() {
		_, err := got.host.Execute(t.Context(), controller.ToolCallContext{SessionUID: "session"}, api.ToolCall{ID: "call_1", Tool: "lookup", Args: map[string]any{}, IdempotencyKey: "once", Mediation: api.MediationControllerMediated})
		callDone <- err
	}()
	callJob := p.next()
	if code := p.claim(callJob); code != 200 {
		t.Fatalf("call claim=%d", code)
	}
	cancel()
	select {
	case err := <-callDone:
		if err == nil {
			t.Fatal("cancelled invocation accepted result")
		}
	case <-time.After(time.Second):
		t.Fatal("invocation cancellation did not stop independent call context")
	}
	if p.result(callJob, map[string]any{"output": map[string]any{"ok": true}}) != 410 {
		t.Fatal("late call result accepted")
	}
}

func TestHostBridgeNonceCacheIsBoundedAndExpires(t *testing.T) {
	p := newBridgePeer(t)
	p.bridge.mu.Lock()
	for i := 0; i < 4096; i++ {
		p.bridge.nonces[fmt.Sprint(i)] = time.Now().Unix() + 60
	}
	p.bridge.mu.Unlock()
	if code, _ := p.send(p.request("POST", "/result/unknown", []byte(`{}`))); code != 401 {
		t.Fatal("full replay cache did not fail closed")
	}
	p.bridge.mu.Lock()
	for nonce := range p.bridge.nonces {
		p.bridge.nonces[nonce] = time.Now().Unix() - 1
	}
	p.bridge.mu.Unlock()
	if code, _ := p.send(p.request("POST", "/result/unknown", []byte(`{}`))); code != 410 {
		t.Fatal("expired cache prevented fresh signed request")
	}
	p.bridge.mu.Lock()
	size := len(p.bridge.nonces)
	p.bridge.mu.Unlock()
	if size != 1 {
		t.Fatalf("expired nonces retained: %d", size)
	}
}

func TestHostBridgeBoundsActiveRunsAndReleasesCapacity(t *testing.T) {
	p := newBridgePeer(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	for i := 0; i < 64; i++ {
		done := p.resolve(ctx)
		job := p.next()
		p.result(job, map[string]any{"definitions": bridgeDefinitions()})
		if got := awaitResolved(t, done); got.err != nil {
			t.Fatal(got.err)
		}
	}
	if got := awaitResolved(t, p.resolve(ctx)); got.err == nil {
		t.Fatal("unbounded frozen runs")
	}
	cancel()
	// Cleanup is asynchronous and nonblocking; consume its bounded close jobs.
	for i := 0; i < 64; i++ {
		if job := p.next(); job["kind"] != "close" {
			t.Fatalf("release=%v", job)
		}
	}
	fresh := p.resolve(t.Context())
	job := p.next()
	p.result(job, map[string]any{"definitions": bridgeDefinitions()})
	if got := awaitResolved(t, fresh); got.err != nil {
		t.Fatal("cancelled runs kept capacity")
	}
}

func TestHostBridgeParallelResultsRemainCorrelated(t *testing.T) {
	p := newBridgePeer(t)
	first := p.resolve(t.Context())
	firstJob := p.next()
	second := p.resolve(t.Context())
	secondJob := p.next()
	if firstJob["id"] == secondJob["id"] || firstJob["runId"] == secondJob["runId"] {
		t.Fatal("correlation identity reused")
	}
	p.result(secondJob, map[string]any{"definitions": bridgeDefinitions()})
	if got := awaitResolved(t, second); got.err != nil {
		t.Fatal(got.err)
	}
	select {
	case <-first:
		t.Fatal("second result reached first waiter")
	default:
	}
	p.result(firstJob, map[string]any{"error": true})
	if got := awaitResolved(t, first); got.err == nil {
		t.Fatal("failed first run consumed another result")
	}
}

func TestHostBridgeLongPollEmptyIsSignedNull(t *testing.T) {
	p := newBridgePeer(t)
	started := time.Now()
	code, body := p.send(p.request("GET", "/next", nil))
	if code != 200 || string(body) != "null" || time.Since(started) < 9*time.Second {
		t.Fatalf("empty long poll: %d %s", code, body)
	}
}

func TestHostBridgeRejectsUnsafeDefinitionsAndResults(t *testing.T) {
	for _, mode := range []string{"too-many", "schema", "credential", "error", "mismatch", "duplicate-json"} {
		t.Run(mode, func(t *testing.T) {
			p := newBridgePeer(t)
			done := p.resolve(t.Context())
			job := p.next()
			defs := bridgeDefinitions()
			switch mode {
			case "too-many":
				defs = make([]api.ToolDefinition, 33)
			case "schema":
				defs[0].InputSchema = nil
			case "credential":
				defs[0].Description = "Bearer ghp_" + strings.Repeat("a", 36)
			case "mismatch":
				defs[0].Name = "other"
			}
			value := map[string]any{"definitions": defs}
			if mode == "error" {
				value = map[string]any{"error": true}
			}
			if mode == "duplicate-json" {
				raw := []byte(`{"definitions":[],"definitions":[]}`)
				code, _ := p.send(p.request("POST", "/result/"+job["id"].(string), raw))
				if code < 400 {
					t.Fatal("duplicate JSON accepted")
				}
				_ = p.bridge.Close()
			} else {
				p.result(job, value)
			}
			if awaitResolved(t, done).err == nil {
				t.Fatal("unsafe definitions accepted")
			}
		})
	}
	for _, value := range []any{map[string]any{"output": map[string]any{"value": strings.Repeat("x", 32769)}}, map[string]any{"error": true}, map[string]any{"output": map[string]any{"value": "ghp_" + strings.Repeat("a", 36)}}} {
		t.Run("result", func(t *testing.T) {
			p := newBridgePeer(t)
			done := p.resolve(t.Context())
			job := p.next()
			p.result(job, map[string]any{"definitions": bridgeDefinitions()})
			got := awaitResolved(t, done)
			if got.err != nil {
				t.Fatal(got.err)
			}
			result := make(chan error, 1)
			go func() {
				_, err := got.host.Execute(t.Context(), controller.ToolCallContext{SessionUID: "session"}, api.ToolCall{ID: "call_1", Tool: "lookup", Args: map[string]any{}, IdempotencyKey: "once", Mediation: api.MediationControllerMediated})
				result <- err
			}()
			callJob := p.next()
			if code := p.claim(callJob); code != 200 {
				t.Fatalf("call claim=%d", code)
			}
			p.result(callJob, value)
			if err := <-result; err == nil {
				t.Fatal("unsafe result accepted")
			}
		})
	}
}
func TestHostRuntimeResourceUsesOnlyTypedStartup(t *testing.T) {
	t.Setenv("WSL_DISTRO_NAME", "Ubuntu-test")
	t.Setenv("PRIVATE_KEY", "private sentinel")
	descriptor, err := NewHostRuntime("/trusted root", "compatible", "http://localhost:1234", nil)
	if err != nil {
		t.Fatal(err)
	}
	executable, _ := os.Executable()
	cwd, _ := os.Getwd()
	if descriptor.Executable != executable || descriptor.CWD != cwd || descriptor.Distro != "Ubuntu-test" || descriptor.Broker != nil || !reflect.DeepEqual(descriptor.Args, []string{"mcp", "--root", "/trusted root", "--provider", "compatible", "--endpoint", "http://localhost:1234"}) {
		t.Fatalf("runtime descriptor: %+v", descriptor)
	}
	cs := connectFixture(t, protocolFixture(), func(s *sdk.Server) { AddHostRuntimeResource(s, descriptor) })
	resource, err := cs.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: "kmx://host-runtime"})
	if err != nil {
		t.Fatal(err)
	}
	raw := resource.Contents[0].Text
	if strings.Contains(raw, "private sentinel") || strings.Contains(raw, "public-key") || !strings.Contains(raw, `"kind":"kmx.host-runtime"`) {
		t.Fatalf("unsafe runtime: %s", raw)
	}
	p := newBridgePeer(t)
	bound, err := NewHostRuntime("", "", "", p.bridge)
	if err != nil || bound.Broker == nil || !reflect.DeepEqual(bound.Args, []string{"mcp"}) {
		t.Fatalf("bound runtime: %v %v", bound, err)
	}
}
func TestAgentResourceProjectsDeclaredToolNames(t *testing.T) {
	f, _, source := canonicalFiles(t)
	source = strings.Replace(source, "## Examples", "## Tools\n- lookup\n\n## Examples", 1)
	if _, err := f.Create(t.Context(), "tools.agent.md", []byte(source)); err != nil {
		t.Fatal(err)
	}
	view := readView(t, resourceClient(t, f, nil), "tools.agent.md")
	if !reflect.DeepEqual(view.Tools, []string{"lookup"}) {
		t.Fatalf("tools=%v", view.Tools)
	}
}
