package gateway

// Every tool-audit row says who called, and a hostile caller cannot use
// that to forge one.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kaimahi-agents/kaimahi/plane/internal/store"
)

// postAs is post() with a caller: the user agent it sends and the address
// it appears to come from.
func postAs(h http.Handler, userAgent, remote string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost,
		"/upstream/kagent-tools/mcp", strings.NewReader(string(body)))
	req.Header.Set("Authorization", goodToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Del("User-Agent")
	if userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}
	if remote != "" {
		req.RemoteAddr = remote
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestEveryToolAuditRowSaysWhoCalled(t *testing.T) {
	future := time.Now().Add(time.Hour)

	t.Run("a client the plane did not deploy is visible as one", func(t *testing.T) {
		// The row this lane exists for: acted_for says 'none' — "there is
		// no person" — for a caller the plane never triggered. The word
		// does not change; what changes is that the row beside it now
		// shows a shell script made the call.
		fs := &fakeStore{credential: store.Credential{Name: "ap-agent", ExpiresAt: &future}}
		postAs(newGateway(t, fs, nil), "curl/8.5.0", "10.244.3.9:52110",
			rpc(t, "tools/call", map[string]any{"name": "payment_schedule"}))

		require.NotEmpty(t, fs.audits)
		row := fs.audits[0]
		assert.Equal(t, store.ActedForNone, row.ActedFor, "the vocabulary is untouched by this lane")
		assert.Equal(t, "ua:curl/8.5.0", row.CallerClaim)
		assert.Equal(t, "10.244.3.9", row.CallerAddr)
	})

	t.Run("a denial carries it too", func(t *testing.T) {
		// A refused attempt is exactly as interesting as a served one, and
		// the denials are where an unexpected caller shows up first.
		fs := &fakeStore{credential: store.Credential{Name: "hello-tools", ExpiresAt: &future}}
		rec := postAs(newGateway(t, fs, nil), "kagent/0.9.12", "10.244.1.4:9",
			rpc(t, "tools/call", map[string]any{"name": "not_allowed"}))
		assert.Equal(t, http.StatusForbidden, rec.Code)
		require.NotEmpty(t, fs.audits)
		assert.Equal(t, "ua:kagent/0.9.12", fs.audits[0].CallerClaim)
	})

	t.Run("a caller that names itself nothing is not a caller that was not recorded", func(t *testing.T) {
		fs := &fakeStore{credential: store.Credential{Name: "hello-tools", ExpiresAt: &future}}
		postAs(newGateway(t, fs, nil), "", "10.244.1.4:9", rpc(t, "tools/call", map[string]any{"name": "x"}))
		require.NotEmpty(t, fs.audits)
		assert.Equal(t, store.CallerNone, fs.audits[0].CallerClaim)
		assert.NotEqual(t, store.CallerUnrecorded, fs.audits[0].CallerClaim)
	})

	t.Run("an expired credential's refusal names its caller", func(t *testing.T) {
		past := time.Now().Add(-time.Hour)
		fs := &fakeStore{credential: store.Credential{Name: "hello-tools", ExpiresAt: &past}}
		postAs(newGateway(t, fs, nil), "curl/8.5.0", "10.244.9.9:1", rpc(t, "ping", nil))
		require.NotEmpty(t, fs.audits)
		assert.Equal(t, "ua:curl/8.5.0", fs.audits[0].CallerClaim,
			"the refusals written before the method is even looked at are audited like the rest")
	})
}

func TestAHostileCallerNameCannotBreakTheAuditRow(t *testing.T) {
	future := time.Now().Add(time.Hour)
	fs := &fakeStore{credential: store.Credential{Name: "hello-tools", ExpiresAt: &future}}

	// Everything a caller could try at once: a forged row, quotes, and
	// several kilobytes of it.
	hostile := `evil" ,` + "\r\n" +
		"2026-09-08T09:00:00 ap-agent   erp   tools/call payment_schedule allowed  200" +
		strings.Repeat(` "x" `, 2000)
	postAs(newGateway(t, fs, nil), hostile, "10.244.1.4:9", rpc(t, "tools/call", map[string]any{"name": "x"}))

	require.NotEmpty(t, fs.audits)
	claim := fs.audits[0].CallerClaim
	assert.NotContains(t, claim, "\n")
	assert.NotContains(t, claim, "\r")
	assert.LessOrEqual(t, len(claim), store.MaxCallerClaim)
	assert.Contains(t, claim, `"`, "quotes are data, not a problem: the JSON encoder escapes them")
}

func TestTheCallerDecidesNothing(t *testing.T) {
	// The guardrail: this is legibility, never a control. A row the store
	// refuses is a row that fails closed, so the caller must not be able to
	// influence admission — and a call is admitted or refused identically
	// whatever it calls itself.
	future := time.Now().Add(time.Hour)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer up.Close()

	for _, name := range []string{"an ordinary client", "no name at all", "a huge name", "one imitating the vocabulary"} {
		ua := map[string]string{
			"an ordinary client":           "curl/8.5.0",
			"no name at all":               "",
			"a huge name":                  strings.Repeat("Z", 9000),
			"one imitating the vocabulary": "ua:none",
		}[name]
		fs := &fakeStore{credential: store.Credential{Name: "hello-tools", ExpiresAt: &future},
			allow: []string{"k8s_get_resources"}}
		h := newGateway(t, fs, up)

		allowed := postAs(h, ua, "10.0.0.1:1", rpc(t, "tools/call", map[string]any{"name": "k8s_get_resources"}))
		assert.Equal(t, http.StatusOK, allowed.Code, "an allowed call stays allowed for %s", name)
		assert.NotContains(t, allowed.Body.String(), "not permitted")

		// A policy denial is answered as a JSON-RPC error over HTTP 200 and
		// audited with the semantic 403; the body is where the refusal is.
		refused := postAs(h, ua, "10.0.0.1:1", rpc(t, "tools/call", map[string]any{"name": "vendor_notify"}))
		assert.Contains(t, refused.Body.String(), "tool not permitted by the Kaimahi allowlist",
			"a refused call stays refused for %s", name)
		require.NotEmpty(t, fs.audits)
		assert.Equal(t, http.StatusForbidden, fs.audits[len(fs.audits)-1].Status)
		assert.Equal(t, "denied", fs.audits[len(fs.audits)-1].Decision)
	}
}

func TestAFailedAuditStillFailsClosedWithACallerOnTheRow(t *testing.T) {
	// The new column must not become a new way for a call to proceed
	// unaudited: the trip and the recovery behave exactly as before.
	future := time.Now().Add(time.Hour)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer up.Close()
	fs := &fakeStore{credential: store.Credential{Name: "hello-tools", ExpiresAt: &future},
		allow: []string{"a"}, auditErr: errors.New("pg down")}
	h := newGateway(t, fs, up)

	postAs(h, "curl/8.5.0", "10.0.0.1:1", rpc(t, "tools/call", map[string]any{"name": "a"}))
	rec := postAs(h, "curl/8.5.0", "10.0.0.1:1", rpc(t, "tools/call", map[string]any{"name": "a"}))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, rec.Body.String(), "tool audit unavailable")
}
