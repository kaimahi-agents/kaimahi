package proxy_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kaimahi-agents/kaimahi/plane/internal/config"
	"github.com/kaimahi-agents/kaimahi/plane/internal/egress"
	"github.com/kaimahi-agents/kaimahi/plane/internal/proxy"
	"github.com/kaimahi-agents/kaimahi/plane/internal/store"
)

// An LLM upstream marked internet (Copilot) is reached ONLY through
// the injected hardened client. With none injected the call fails closed
// — and is still ledgered (spend is recorded before failures are
// honored). The in-cluster upstream on the same proxy is untouched.
func TestInternetUpstreamNeverFallsBackToThePlainClient(t *testing.T) {
	srv, gotReq, _ := newUpstream(t)
	f := newFakeStore()
	f.addToken("kmh_opaque", store.Credential{Name: "hello"})
	deps := testDeps(f, map[string]config.Upstream{
		// The plain-http test server, marked internet: the hardened client
		// (had one been injected) would refuse it; with none injected the
		// call must fail closed, never reach srv.
		"copilot": {Protocol: config.ProtocolChatCompletions, BaseURL: srv.URL, Path: "chat/completions", Classification: config.ClassMetered, Internet: true},
		"ollama":  {Protocol: config.ProtocolChatCompletions, BaseURL: srv.URL, Path: "v1/chat/completions", Classification: config.ClassFree},
	})
	mux := proxy.NewDataMux(deps)
	w := doChat(t, mux, "kmh_opaque", "/upstream/copilot/chat/completions", `{"model": "gpt-5-mini", "messages": []}`)
	require.Equal(t, http.StatusBadGateway, w.Code)
	require.Empty(t, gotReq.Method, "the upstream must not be reached")
	require.Len(t, f.ledger, 1)
	require.Equal(t, http.StatusBadGateway, f.ledger[0].Status)

	// The in-cluster path on the same deps still forwards.
	w = doChat(t, mux, "kmh_opaque", "/upstream/ollama/v1/chat/completions", `{"model": "qwen", "messages": []}`)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "POST", gotReq.Method)

	// With the hardened client injected, the same plain-http upstream is
	// refused by the client itself (https only) — still 502, ledgered.
	hardened, err := egress.NewClient(egress.Policy{}, nil)
	require.NoError(t, err)
	deps.InternetClient = hardened
	mux = proxy.NewDataMux(deps)
	gotReq.Method = ""
	w = doChat(t, mux, "kmh_opaque", "/upstream/copilot/chat/completions", `{"model": "gpt-5-mini", "messages": []}`)
	require.Equal(t, http.StatusBadGateway, w.Code)
	require.Empty(t, gotReq.Method)
	require.Len(t, f.ledger, 3)
}
