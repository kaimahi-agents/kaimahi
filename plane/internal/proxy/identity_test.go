package proxy_test

// The LLM proxy's half of the two gaps: an expired credential is
// refused with a message an operator can act on and a ledger row that
// records the refusal, and every ledger row names who the call was made
// for.

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/kaimahi-agents/kaimahi/plane/internal/config"
	"github.com/kaimahi-agents/kaimahi/plane/internal/proxy"
	"github.com/kaimahi-agents/kaimahi/plane/internal/store"
)

func freeUpstream(t *testing.T) map[string]config.Upstream {
	t.Helper()
	srv, _, _ := newUpstream(t)
	return map[string]config.Upstream{"ollama": {
		BaseURL: srv.URL, Path: "v1/chat/completions",
		Protocol: config.ProtocolChatCompletions, Classification: config.ClassFree}}
}

func TestExpiredCredentialIsRefusedNamedAndLedgered(t *testing.T) {
	f := newFakeStore()
	past := time.Now().Add(-time.Minute)
	f.addToken("kmh_expired", store.Credential{Name: "hello-world", ExpiresAt: &past})
	mux := proxy.NewDataMux(testDeps(f, freeUpstream(t)))

	w := doChat(t, mux, "kmh_expired", "/upstream/ollama/v1/chat/completions", chatBody)
	require.Equal(t, http.StatusForbidden, w.Code,
		"an expired credential is a policy refusal about a REAL credential, not an unknown token")
	body := w.Body.String()
	require.Contains(t, body, `expired credential "hello-world"`)
	require.Contains(t, body, past.UTC().Format(time.RFC3339), "the message says WHEN, so an operator is not guessing")
	require.Contains(t, body, "kmx credential renew hello-world --ttl 720h", "and it says the fix")

	require.Len(t, f.ledger, 1, "the refusal is audited like every other refusal")
	require.Equal(t, "denied", f.ledger[0].CostSource)
	require.Equal(t, http.StatusForbidden, f.ledger[0].Status)
}

func TestLegacyCredentialWithoutAnExpiryIsStillServed(t *testing.T) {
	f := newFakeStore()
	f.addToken("kmh_legacy", store.Credential{Name: "hello-world"})
	mux := proxy.NewDataMux(testDeps(f, freeUpstream(t)))

	w := doChat(t, mux, "kmh_legacy", "/upstream/ollama/v1/chat/completions", chatBody)
	require.Equal(t, http.StatusOK, w.Code,
		"expiring the whole estate at migration time would be an outage, not a control")
	require.Len(t, f.ledger, 1)
	require.Equal(t, "free", f.ledger[0].CostSource)
}

func TestEveryLedgerRowNamesWhoTheCallWasFor(t *testing.T) {
	future := time.Now().Add(time.Hour)

	t.Run("a person, when a run names one", func(t *testing.T) {
		f := newFakeStore()
		f.addToken("kmh_ok", store.Credential{Name: "ap-agent", ExpiresAt: &future})
		f.actor = store.Attribution{ActedFor: "slack:U0CIPERSON", RunID: "run-under-test"}
		mux := proxy.NewDataMux(testDeps(f, freeUpstream(t)))

		require.Equal(t, http.StatusOK, doChat(t, mux, "kmh_ok", "/upstream/ollama/v1/chat/completions", chatBody).Code)
		require.Len(t, f.ledger, 1)
		require.Equal(t, "slack:U0CIPERSON", f.ledger[0].ActedFor)
		require.Equal(t, "run-under-test", f.ledger[0].RunID)
	})

	t.Run("nobody, and it says so", func(t *testing.T) {
		f := newFakeStore()
		f.addToken("kmh_ok", store.Credential{Name: "hello-world", ExpiresAt: &future})
		mux := proxy.NewDataMux(testDeps(f, freeUpstream(t)))

		require.Equal(t, http.StatusOK, doChat(t, mux, "kmh_ok", "/upstream/ollama/v1/chat/completions", chatBody).Code)
		require.Len(t, f.ledger, 1)
		require.Equal(t, store.ActedForNone, f.ledger[0].ActedFor,
			"an operator-driven turn is VALID and complete — it is not a missing value")
		require.Empty(t, f.ledger[0].RunID)
	})

	t.Run("lost, and that is a different word", func(t *testing.T) {
		f := newFakeStore()
		f.addToken("kmh_ok", store.Credential{Name: "hello-world", ExpiresAt: &future})
		f.actorErr = errors.New("attribution read failed")
		mux := proxy.NewDataMux(testDeps(f, freeUpstream(t)))

		require.Equal(t, http.StatusOK, doChat(t, mux, "kmh_ok", "/upstream/ollama/v1/chat/completions", chatBody).Code,
			"attribution describes a call; it never admits or denies one")
		require.Len(t, f.ledger, 1)
		require.Equal(t, store.ActedForUnknown, f.ledger[0].ActedFor)
	})

	t.Run("and a denial carries it too", func(t *testing.T) {
		f := newFakeStore()
		f.addToken("kmh_ok", store.Credential{Name: "ap-agent", ExpiresAt: &future})
		f.actor = store.Attribution{ActedFor: "slack:U0CIPERSON"}
		mux := proxy.NewDataMux(testDeps(f, map[string]config.Upstream{}))

		require.Equal(t, http.StatusForbidden, doChat(t, mux, "kmh_ok", "/upstream/nope/v1/chat/completions", chatBody).Code)
		require.Len(t, f.ledger, 1)
		require.Equal(t, "slack:U0CIPERSON", f.ledger[0].ActedFor,
			"who was refused is exactly as interesting as who was served")
	})

	_ = context.Background
}
