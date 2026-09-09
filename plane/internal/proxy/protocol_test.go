package proxy_test

// The model seam, on the protocol one current agent framework speaks by
// default. Every test here is keyless: the upstream is an httptest
// server in this process, and the numbers asserted are the numbers it
// reported.

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kaimahi-agents/kaimahi/plane/internal/config"
	"github.com/kaimahi-agents/kaimahi/plane/internal/proxy"
	"github.com/kaimahi-agents/kaimahi/plane/internal/store"
)

const responsesBody = `{"model": "test-model", "input": "hi"}`

// A Responses-API call is metered from ITS OWN token fields. Before the
// protocol existed this was the whole finding: the call was forwarded,
// answered, and ledgered `0 in / 0 out`, so a token budget over the
// upstream could never be exhausted.
func TestAResponsesCallIsMeteredFromInputAndOutputTokens(t *testing.T) {
	f := newFakeStore()
	f.addToken("tok", store.Credential{Name: "hello"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/responses", r.URL.Path)
		body, _ := io.ReadAll(r.Body)
		// The Responses API refuses unknown top-level parameters, so the
		// chat seam's stream_options must not be smuggled in here.
		require.NotContains(t, string(body), "stream_options")
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id": "resp_1", "status": "completed",
		  "usage": {"input_tokens": 13, "output_tokens": 16, "total_tokens": 29}}`)
	}))
	t.Cleanup(srv.Close)
	mux := proxy.NewDataMux(testDeps(f, map[string]config.Upstream{
		"house": {BaseURL: srv.URL, Path: "v1/responses",
			Protocol: config.ProtocolResponses, Classification: config.ClassFree},
	}))
	w := doChat(t, mux, "tok", "/upstream/house/v1/responses", responsesBody)
	require.Equal(t, 200, w.Code)
	require.Len(t, f.ledger, 1)
	require.Equal(t, int64(13), f.ledger[0].InputTokens)
	require.Equal(t, int64(16), f.ledger[0].OutputTokens)
	require.Equal(t, "free", f.ledger[0].CostSource)
}

// The streamed half. The Responses API carries usage on its terminal
// event, inside the `response` object rather than at the top level.
func TestAStreamedResponsesCallIsMeteredFromItsTerminalEvent(t *testing.T) {
	f := newFakeStore()
	f.addToken("tok", store.Credential{Name: "hello"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n")
		_, _ = fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":"+
			"{\"id\":\"resp_1\",\"status\":\"completed\",\"usage\":{\"input_tokens\":21,\"output_tokens\":34}}}\n\n")
	}))
	t.Cleanup(srv.Close)
	mux := proxy.NewDataMux(testDeps(f, map[string]config.Upstream{
		"house": {BaseURL: srv.URL, Path: "v1/responses",
			Protocol: config.ProtocolResponses, Classification: config.ClassFree},
	}))
	w := doChat(t, mux, "tok", "/upstream/house/v1/responses",
		`{"model": "test-model", "input": "hi", "stream": true}`)
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), "response.completed")
	require.Len(t, f.ledger, 1)
	require.Equal(t, int64(21), f.ledger[0].InputTokens)
	require.Equal(t, int64(34), f.ledger[0].OutputTokens)
}

// A turn that ends early still spent tokens, so response.incomplete is
// read too — otherwise the cheapest way to spend unmetered would be to
// hit a token limit.
func TestAnIncompleteResponsesStreamIsStillMetered(t *testing.T) {
	f := newFakeStore()
	f.addToken("tok", store.Credential{Name: "hello"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.incomplete\",\"response\":"+
			"{\"status\":\"incomplete\",\"usage\":{\"input_tokens\":5,\"output_tokens\":2}}}\n\n")
	}))
	t.Cleanup(srv.Close)
	mux := proxy.NewDataMux(testDeps(f, map[string]config.Upstream{
		"house": {BaseURL: srv.URL, Path: "v1/responses",
			Protocol: config.ProtocolResponses, Classification: config.ClassFree},
	}))
	require.Equal(t, 200, doChat(t, mux, "tok", "/upstream/house/v1/responses",
		`{"model": "test-model", "stream": true}`).Code)
	require.Len(t, f.ledger, 1)
	require.Equal(t, int64(5), f.ledger[0].InputTokens)
	require.Equal(t, int64(2), f.ledger[0].OutputTokens)
}

// The decision this lane had to make, asserted: a success the plane
// cannot meter is REFUSED, not relayed. The caller gets nothing, and the
// row says `unmetered` rather than a plausible zero.
func TestASuccessWithNoReadableUsageIsRefusedAndLedgeredUnmetered(t *testing.T) {
	f := newFakeStore()
	f.addToken("tok", store.Credential{Name: "hello"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// An answer with no usage envelope at all — and a body a caller
		// would have been perfectly happy with.
		_, _ = fmt.Fprint(w, `{"id": "resp_1", "output": [{"content": [{"text": "the answer"}]}]}`)
	}))
	t.Cleanup(srv.Close)
	mux := proxy.NewDataMux(testDeps(f, map[string]config.Upstream{
		"house": {BaseURL: srv.URL, Path: "v1/responses",
			Protocol: config.ProtocolResponses, Classification: config.ClassFree},
	}))
	w := doChat(t, mux, "tok", "/upstream/house/v1/responses", responsesBody)
	require.Equal(t, 502, w.Code)
	require.NotContains(t, w.Body.String(), "the answer", "an unmeterable answer must not reach the caller")
	require.Len(t, f.ledger, 1)
	require.Equal(t, "unmetered", f.ledger[0].CostSource)
	require.Equal(t, 502, f.ledger[0].Status)
	require.Zero(t, f.ledger[0].CostCents, "a cost was never read, so none may be charged")
}

// The one case the refusal cannot reach: a stream whose bytes have
// already been flushed. The plane cannot recall them and does not
// pretend the call cost nothing either.
func TestAStreamWithNoReadableUsageIsRelayedButLedgeredUnmetered(t *testing.T) {
	f := newFakeStore()
	f.addToken("tok", store.Credential{Name: "hello"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n")
	}))
	t.Cleanup(srv.Close)
	mux := proxy.NewDataMux(testDeps(f, map[string]config.Upstream{
		"house": {BaseURL: srv.URL, Path: "v1/responses",
			Protocol: config.ProtocolResponses, Classification: config.ClassFree},
	}))
	w := doChat(t, mux, "tok", "/upstream/house/v1/responses",
		`{"model": "test-model", "stream": true}`)
	require.Equal(t, 200, w.Code)
	require.Len(t, f.ledger, 1)
	require.Equal(t, "unmetered", f.ledger[0].CostSource)
	require.Equal(t, 200, f.ledger[0].Status, "the upstream's own status, because that is what the caller got")
}

// A stream nobody asked for is refused before a byte of it is relayed.
// This is the half of the metering rule that would otherwise be a hole
// of the plane's own making: `include_usage` is only ever sent when the
// REQUEST said stream, so an upstream that streams anyway is answering
// in a shape the plane did not prepare to meter — and letting it through
// would land in the one place a refusal is no longer possible.
func TestAStreamTheRequestDidNotAskForIsRefusedBeforeItIsRelayed(t *testing.T) {
	f := newFakeStore()
	f.addToken("tok", store.Credential{Name: "hello"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"the answer\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	mux := proxy.NewDataMux(testDeps(f, map[string]config.Upstream{
		"ollama": {BaseURL: srv.URL, Path: "v1/chat/completions",
			Protocol: config.ProtocolChatCompletions, Classification: config.ClassFree},
	}))
	// chatBody carries no "stream": true.
	w := doChat(t, mux, "tok", "/upstream/ollama/v1/chat/completions", chatBody)
	require.Equal(t, 502, w.Code)
	require.NotContains(t, w.Body.String(), "the answer", "an unmeterable stream must not reach the caller")
	require.Len(t, f.ledger, 1)
	require.Equal(t, "unmetered", f.ledger[0].CostSource)
	require.Equal(t, 502, f.ledger[0].Status)
}

// And the stream the client DID ask for is unaffected — the refusal
// above must not become "the plane refuses streaming".
func TestAStreamTheRequestAskedForIsStillRelayed(t *testing.T) {
	f := newFakeStore()
	f.addToken("tok", store.Credential{Name: "hello"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":5}}\n\n")
	}))
	t.Cleanup(srv.Close)
	mux := proxy.NewDataMux(testDeps(f, map[string]config.Upstream{
		"ollama": {BaseURL: srv.URL, Path: "v1/chat/completions",
			Protocol: config.ProtocolChatCompletions, Classification: config.ClassFree},
	}))
	w := doChat(t, mux, "tok", "/upstream/ollama/v1/chat/completions",
		`{"model": "test-model", "stream": true, "messages": []}`)
	require.Equal(t, 200, w.Code)
	require.Len(t, f.ledger, 1)
	require.Equal(t, int64(3), f.ledger[0].InputTokens)
	require.Equal(t, "free", f.ledger[0].CostSource)
}

// An upstream reporting a genuine zero is NOT the same fact as one the
// plane could not read, and must not be recorded as if it were.
func TestAnUpstreamReportedZeroIsNotUnmetered(t *testing.T) {
	f := newFakeStore()
	f.addToken("tok", store.Credential{Name: "hello"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"usage": {"input_tokens": 0, "output_tokens": 0}}`)
	}))
	t.Cleanup(srv.Close)
	mux := proxy.NewDataMux(testDeps(f, map[string]config.Upstream{
		"house": {BaseURL: srv.URL, Path: "v1/responses",
			Protocol: config.ProtocolResponses, Classification: config.ClassFree},
	}))
	require.Equal(t, 200, doChat(t, mux, "tok", "/upstream/house/v1/responses", responsesBody).Code)
	require.Len(t, f.ledger, 1)
	require.Equal(t, "free", f.ledger[0].CostSource)
	require.Zero(t, f.ledger[0].InputTokens)
}

// An upstream ERROR carries no usage legitimately, and is relayed with
// its own status exactly as before — the refusal above is about
// successes, and widening it would turn every upstream 4xx into a 502.
func TestAnUpstreamErrorIsStillRelayed(t *testing.T) {
	f := newFakeStore()
	f.addToken("tok", store.Credential{Name: "hello"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		_, _ = fmt.Fprint(w, `{"error": {"message": "unknown parameter"}}`)
	}))
	t.Cleanup(srv.Close)
	mux := proxy.NewDataMux(testDeps(f, map[string]config.Upstream{
		"house": {BaseURL: srv.URL, Path: "v1/responses",
			Protocol: config.ProtocolResponses, Classification: config.ClassFree},
	}))
	w := doChat(t, mux, "tok", "/upstream/house/v1/responses", responsesBody)
	require.Equal(t, 400, w.Code)
	require.Contains(t, w.Body.String(), "unknown parameter")
	require.Len(t, f.ledger, 1)
	require.Equal(t, "free", f.ledger[0].CostSource)
	require.Equal(t, 400, f.ledger[0].Status)
}

// The two protocols must not read each other's fields. This is the
// mistake in both directions: the plane reading a Responses answer as
// chat is the finding, and a chat answer read as Responses would be the
// same bug pointed the other way.
//
// It is also the guard against a THIRD protocol arriving with no reader:
// every protocol in the vocabulary needs a sample here, and a sample
// whose reader is missing cannot be found.
func TestEachProtocolReadsOnlyItsOwnUsageShape(t *testing.T) {
	samples := map[string]string{
		config.ProtocolChatCompletions: `{"usage": {"prompt_tokens": 7, "completion_tokens": 11}}`,
		config.ProtocolResponses:       `{"usage": {"input_tokens": 7, "output_tokens": 11}}`,
	}
	require.Len(t, samples, len(config.Protocols),
		"a protocol was added to the vocabulary without a sample of its usage shape here")
	for _, protocol := range config.Protocols {
		body, ok := samples[protocol]
		require.True(t, ok, "no usage sample for protocol %q", protocol)
		for _, reader := range config.Protocols {
			f := newFakeStore()
			f.addToken("tok", store.Credential{Name: "hello"})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, body)
			}))
			path := "v1/chat/completions"
			if reader == config.ProtocolResponses {
				path = "v1/responses"
			}
			mux := proxy.NewDataMux(testDeps(f, map[string]config.Upstream{
				"u": {BaseURL: srv.URL, Path: path, Protocol: reader, Classification: config.ClassFree},
			}))
			w := doChat(t, mux, "tok", "/upstream/u/"+path, chatBody)
			srv.Close()
			require.Len(t, f.ledger, 1)
			if reader == protocol {
				require.Equal(t, 200, w.Code, "%s must read its own shape", reader)
				require.Equal(t, int64(7), f.ledger[0].InputTokens)
				continue
			}
			require.Equal(t, 502, w.Code, "%s must not read %s's usage shape", reader, protocol)
			require.Equal(t, "unmetered", f.ledger[0].CostSource)
		}
	}
}

// The overlay's whole purpose, end to end through the parser the plane
// actually boots with: a fragment adds a model upstream, and a call to
// it is metered.
func TestAModelUpstreamAddedByOverlayIsGovernedLikeACommittedOne(t *testing.T) {
	f := newFakeStore()
	f.addToken("tok", store.Credential{Name: "hello", CapTokens: i64(20)})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"usage": {"input_tokens": 13, "output_tokens": 16}}`)
	}))
	t.Cleanup(srv.Close)
	base := []byte(`{"upstreams": {"ollama": {"base_url": "http://ollama.ollama.svc.cluster.local:11434",
	  "path": "v1/chat/completions", "classification": "free"}}}`)
	fragment := fmt.Sprintf(`{"upstreams": {"house": {"base_url": %q, "path": "v1/responses",
	  "classification": "free"}}}`, srv.URL)
	merged, err := config.Merge(base, []config.Fragment{{Name: "house.json", Raw: []byte(fragment)}})
	require.NoError(t, err)
	cfg, err := config.Parse(merged)
	require.NoError(t, err)
	require.Equal(t, config.ProtocolResponses, cfg.Upstreams["house"].Protocol)

	mux := proxy.NewDataMux(testDeps(f, cfg.Upstreams))
	require.Equal(t, 200, doChat(t, mux, "tok", "/upstream/house/v1/responses", responsesBody).Code)
	require.Len(t, f.ledger, 1)
	require.Equal(t, int64(13), f.ledger[0].InputTokens)
	// And the budget it lands in is the same one every other seam uses:
	// 29 tokens against a cap of 20 exhausts it on the next call.
	f.monthToks = 29
	require.Equal(t, 429, doChat(t, mux, "tok", "/upstream/house/v1/responses", responsesBody).Code)
}

// The path gate is unchanged and is the request-side answer to "an
// unrecognised shape": one declared path per upstream, and anything else
// refused before any upstream contact.
func TestAPathTheUpstreamDoesNotDeclareIsStillRefused(t *testing.T) {
	f := newFakeStore()
	f.addToken("tok", store.Credential{Name: "hello"})
	var reached bool
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
	t.Cleanup(srv.Close)
	mux := proxy.NewDataMux(testDeps(f, map[string]config.Upstream{
		"house": {BaseURL: srv.URL, Path: "v1/responses",
			Protocol: config.ProtocolResponses, Classification: config.ClassFree},
	}))
	w := doChat(t, mux, "tok", "/upstream/house/v1/chat/completions", chatBody)
	require.Equal(t, 403, w.Code)
	require.Contains(t, w.Body.String(), "path not allowed")
	require.False(t, reached, "no upstream contact before the route is authorised")
}

// The price gate is unchanged on the new protocol: a metered upstream
// under a cents budget refuses a model it has no price for, and the new
// path is not a way around it.
func TestThePriceGateStillAppliesOnTheResponsesPath(t *testing.T) {
	f := newFakeStore()
	f.addToken("tok", store.Credential{Name: "hello", CapCents: i64(500)})
	var reached bool
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
	t.Cleanup(srv.Close)
	mux := proxy.NewDataMux(testDeps(f, map[string]config.Upstream{
		"paid": {BaseURL: srv.URL, Path: "v1/responses",
			Protocol: config.ProtocolResponses, Classification: config.ClassMetered},
	}))
	w := doChat(t, mux, "tok", "/upstream/paid/v1/responses", responsesBody)
	require.Equal(t, 403, w.Code)
	require.Contains(t, w.Body.String(), "no configured price")
	require.False(t, reached)
}

// /admin/config/validate answers with the protocol it resolved, which is
// the field an operator cannot check by reading their own fragment back.
func TestValidateEchoesEveryModelUpstreamWithItsProtocol(t *testing.T) {
	code, out := validate(t, `{"fragments": {"house.json": {
	  "upstreams": {"house": {"base_url": "http://vllm.demo:8000",
	    "path": "v1/responses", "classification": "free"}}
	}}}`)
	require.Equal(t, 200, code)
	require.Equal(t, true, out["ok"])
	require.Equal(t, []any{"house (responses)", "ollama (chat_completions)"}, out["upstreams"])
}
