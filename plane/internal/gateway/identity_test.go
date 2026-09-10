package gateway

// The MCP gateway's half of the two gaps: an expired credential is
// refused before any tool is reached and the refusal is audited, and
// every tool-audit row names who the call was made for.

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kaimahi-agents/kaimahi/plane/internal/store"
)

func TestExpiredCredentialReachesNoToolAndIsAudited(t *testing.T) {
	upstreamHit := false
	up := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { upstreamHit = true }))
	defer up.Close()

	past := time.Now().Add(-time.Hour)
	fs := &fakeStore{credential: store.Credential{Name: "hello-tools", ExpiresAt: &past}, allow: []string{"k8s_get_resources"}}
	h := newGateway(t, fs, up)

	rec := post(h, goodToken, rpc(t, "tools/call", map[string]any{"name": "k8s_get_resources"}))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), `expired credential "hello-tools"`)
	assert.Contains(t, rec.Body.String(), "kmx credential renew hello-tools --ttl 720h")
	assert.False(t, upstreamHit, "an expired credential must not reach a tool server")

	require.Len(t, fs.audits, 1, "unlike an unknown token there IS a credential to attribute this to")
	assert.Equal(t, "denied", fs.audits[0].Decision)
	assert.Equal(t, http.StatusForbidden, fs.audits[0].Status)
	assert.Contains(t, fs.audits[0].Detail, "expired credential")

	// A legacy credential (no expiry) is unaffected.
	fs2 := &fakeStore{credential: store.Credential{Name: "hello-tools"}, allow: []string{"a"}}
	assert.Equal(t, http.StatusOK, post(newGateway(t, fs2, up), goodToken, rpc(t, "ping", nil)).Code)
}

func TestEveryToolAuditRowNamesWhoTheCallWasFor(t *testing.T) {
	future := time.Now().Add(time.Hour)

	t.Run("a person, when a run names one", func(t *testing.T) {
		fs := &fakeStore{
			credential: store.Credential{Name: "ap-agent", ExpiresAt: &future},
			actor:      store.Attribution{ActedFor: "slack:U0CIPERSON", RunID: "run-1"},
		}
		// No allowlist: the call is denied, and the DENIAL is the row
		// that has to name the person — a refused attempt is exactly as
		// interesting as a served one.
		rec := post(newGateway(t, fs, nil), goodToken, rpc(t, "tools/call", map[string]any{"name": "payment_schedule"}))
		assert.Equal(t, http.StatusForbidden, rec.Code)
		require.NotEmpty(t, fs.audits)
		assert.Equal(t, "slack:U0CIPERSON", fs.audits[len(fs.audits)-1].ActedFor)
		assert.Equal(t, "run-1", fs.audits[len(fs.audits)-1].RunID)
	})

	t.Run("nobody, and it says so", func(t *testing.T) {
		fs := &fakeStore{credential: store.Credential{Name: "hello-tools", ExpiresAt: &future}}
		post(newGateway(t, fs, nil), goodToken, rpc(t, "tools/call", map[string]any{"name": "x"}))
		require.NotEmpty(t, fs.audits)
		assert.Equal(t, store.ActedForNone, fs.audits[0].ActedFor)
		assert.Empty(t, fs.audits[0].RunID)
	})

	t.Run("lost, and that is a different word", func(t *testing.T) {
		fs := &fakeStore{
			credential: store.Credential{Name: "hello-tools", ExpiresAt: &future},
			actorErr:   errors.New("pg blip"),
		}
		post(newGateway(t, fs, nil), goodToken, rpc(t, "tools/call", map[string]any{"name": "x"}))
		require.NotEmpty(t, fs.audits)
		assert.Equal(t, store.ActedForUnknown, fs.audits[0].ActedFor,
			"a lost attribution is never reported as 'nobody was there'")
	})
}
