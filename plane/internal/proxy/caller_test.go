package proxy_test

// The spend trail has the same blind spot the tool trail had, so it gets
// the same two columns: nothing in a ledger row told an agent apart from
// a script holding its token.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kaimahi-agents/kaimahi/plane/internal/config"
	"github.com/kaimahi-agents/kaimahi/plane/internal/proxy"
	"github.com/kaimahi-agents/kaimahi/plane/internal/store"
)

func chatAs(t *testing.T, mux http.Handler, token, userAgent, remote, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), "POST", path, strings.NewReader(chatBody))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Del("User-Agent")
	if userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}
	req.RemoteAddr = remote
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func TestEveryLedgerRowSaysWhoCalled(t *testing.T) {
	f := newFakeStore()
	f.addToken("tok", store.Credential{Name: "hello"})
	up, _, _ := newUpstream(t)
	mux := proxy.NewDataMux(testDeps(f, map[string]config.Upstream{
		"ollama": {Protocol: config.ProtocolChatCompletions, BaseURL: up.URL, Path: "v1/chat/completions", Classification: config.ClassFree},
	}))

	require.Equal(t, 200, chatAs(t, mux, "tok", "curl/8.5.0", "10.244.3.9:52110",
		"/upstream/ollama/v1/chat/completions").Code)
	require.Len(t, f.ledger, 1)
	assert.Equal(t, "ua:curl/8.5.0", f.ledger[0].CallerClaim)
	assert.Equal(t, "10.244.3.9", f.ledger[0].CallerAddr)
	assert.Equal(t, store.ActedForNone, f.ledger[0].ActedFor,
		"the attribution vocabulary is untouched; the row beside it is what changed")
}

func TestADeniedLedgerRowSaysWhoCalledToo(t *testing.T) {
	// A denial is ledgered before the call is ever forwarded, and it is
	// the row where an unexpected caller shows up first.
	f := newFakeStore()
	f.addToken("tok", store.Credential{Name: "hello"})
	up, _, _ := newUpstream(t)
	mux := proxy.NewDataMux(testDeps(f, map[string]config.Upstream{
		"ollama": {Protocol: config.ProtocolChatCompletions, BaseURL: up.URL, Path: "v1/chat/completions", Classification: config.ClassFree},
	}))

	require.Equal(t, 403, chatAs(t, mux, "tok", "curl/8.5.0", "10.0.0.7:1",
		"/upstream/nope/v1/chat/completions").Code)
	require.Len(t, f.ledger, 1)
	assert.Equal(t, "denied", f.ledger[0].CostSource)
	assert.Equal(t, "ua:curl/8.5.0", f.ledger[0].CallerClaim)
	assert.Equal(t, "10.0.0.7", f.ledger[0].CallerAddr)
}

func TestAHostileCallerNameCannotBreakALedgerRow(t *testing.T) {
	f := newFakeStore()
	f.addToken("tok", store.Credential{Name: "hello"})
	up, _, _ := newUpstream(t)
	mux := proxy.NewDataMux(testDeps(f, map[string]config.Upstream{
		"ollama": {Protocol: config.ProtocolChatCompletions, BaseURL: up.URL, Path: "v1/chat/completions", Classification: config.ClassFree},
	}))

	// Several kilobytes of quotes and a forged-looking ledger line. No
	// CR or LF: a header carrying one cannot cross an HTTP connection at
	// all — Go's server refuses to parse it inbound and its client
	// refuses to write it outbound — so the wire-reachable hostile value
	// is this one. The store bounds the rest (store/caller_test.go).
	hostile := `evil" ,  2026-09-08T09:00:00 hello ollama qwen2.5:3b 0 0 0 free 200` +
		strings.Repeat(` "x" `, 2000)
	require.Equal(t, 200, chatAs(t, mux, "tok", hostile, "10.0.0.7:1",
		"/upstream/ollama/v1/chat/completions").Code,
		"a hostile name is data; it must not change whether the call is served")
	require.Len(t, f.ledger, 1)
	claim := f.ledger[0].CallerClaim
	assert.NotContains(t, claim, "\n")
	assert.LessOrEqual(t, len(claim), store.MaxCallerClaim)
	assert.Contains(t, claim, `"`, "quotes are data: the JSON encoder escapes them on the way out")
}

func TestACallerThatOffersNoNameIsNotACallerNotRecorded(t *testing.T) {
	f := newFakeStore()
	f.addToken("tok", store.Credential{Name: "hello"})
	up, _, _ := newUpstream(t)
	mux := proxy.NewDataMux(testDeps(f, map[string]config.Upstream{
		"ollama": {Protocol: config.ProtocolChatCompletions, BaseURL: up.URL, Path: "v1/chat/completions", Classification: config.ClassFree},
	}))

	require.Equal(t, 200, chatAs(t, mux, "tok", "", "10.0.0.7:1",
		"/upstream/ollama/v1/chat/completions").Code)
	require.Len(t, f.ledger, 1)
	assert.Equal(t, store.CallerNone, f.ledger[0].CallerClaim)
	assert.NotEqual(t, store.CallerUnrecorded, f.ledger[0].CallerClaim)
}
