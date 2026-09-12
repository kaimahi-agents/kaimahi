package ops_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/kaimahi-agents/kaimahi/plane/internal/ops"
)

type pinger struct{ err error }

func (p *pinger) Ping(_ context.Context) error { return p.err }

func get(mux http.Handler, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

func TestReadinessFollowsTheStoreAndLivenessDoesNot(t *testing.T) {
	p := &pinger{}
	mux := ops.NewMux(ops.Deps{Ready: p})
	require.Equal(t, 200, get(mux, "/readyz").Code)
	require.Equal(t, 200, get(mux, "/livez").Code)

	// Postgres goes away: readiness drops, liveness is untouched — the
	// pod leaves the Service and is NOT restarted.
	p.err = errors.New("connection refused")
	require.Equal(t, 503, get(mux, "/readyz").Code)
	require.Equal(t, 200, get(mux, "/livez").Code)
	p.err = nil
	require.Equal(t, 200, get(mux, "/readyz").Code)
}

func TestDrainingDropsReadinessNotLiveness(t *testing.T) {
	var draining atomic.Bool
	mux := ops.NewMux(ops.Deps{Ready: &pinger{}, Draining: &draining})
	require.Equal(t, 200, get(mux, "/readyz").Code)
	draining.Store(true)
	w := get(mux, "/readyz")
	require.Equal(t, 503, w.Code)
	require.Contains(t, w.Body.String(), "draining")
	require.Equal(t, 200, get(mux, "/livez").Code)
}

func TestLivenessFailsOnlyOnASaturatedPoolMakingNoProgress(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	st := ops.PoolStats{Acquired: 4, Max: 4, AcquireCount: 100}
	mux := ops.NewMux(ops.Deps{
		Stats:      func() ops.PoolStats { return st },
		Now:        func() time.Time { return now },
		StallAfter: 60 * time.Second,
	})
	require.Equal(t, 200, get(mux, "/livez").Code)
	// Saturated, but acquires keep completing (a busy plane): live.
	now = now.Add(70 * time.Second)
	st.AcquireCount = 101
	require.Equal(t, 200, get(mux, "/livez").Code)
	// Saturated with nothing completing for longer than StallAfter: a
	// local fault (a leak or a deadlock) — not live.
	now = now.Add(30 * time.Second)
	require.Equal(t, 200, get(mux, "/livez").Code, "not yet past the threshold")
	now = now.Add(31 * time.Second)
	w := get(mux, "/livez")
	require.Equal(t, 503, w.Code)
	require.Contains(t, w.Body.String(), "saturated")
	// A slow database never looks like this: callers bound their
	// queries and return connections, so the pool is not saturated.
	st.Acquired = 3
	require.Equal(t, 200, get(mux, "/livez").Code)
}

func TestLivenessChecksTheDataListenersOnLoopback(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer ok.Close()
	wedged := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer wedged.Close()
	mux := ops.NewMux(ops.Deps{Listeners: []string{ok.URL}})
	require.Equal(t, 200, get(mux, "/livez").Code)
	mux = ops.NewMux(ops.Deps{Listeners: []string{ok.URL, wedged.URL}, Client: &http.Client{Timeout: 200 * time.Millisecond}})
	w := get(mux, "/livez")
	require.Equal(t, 503, w.Code)
	require.Contains(t, w.Body.String(), "not answering")
}

// Liveness checks each listener using its declared scheme. It must
// VERIFY a seam it dials over TLS: a probe that
// skipped verification would keep reporting the plane live on the day its own
// certificate expired, which is the failure this is here to catch.
func TestLivenessVerifiesTheSeamsItDialsOverTLS(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	plaintext := httptest.NewServer(handler)
	defer plaintext.Close()
	secure := httptest.NewTLSServer(handler)
	defer secure.Close()

	mux := ops.NewMux(ops.Deps{
		Listeners: []string{plaintext.URL, secure.URL},
		Client:    secure.Client(),
	})
	require.Equal(t, 200, get(mux, "/livez").Code)

	// The same listener, dialled by a client that does not trust it.
	mux = ops.NewMux(ops.Deps{
		Listeners: []string{secure.URL},
		Client:    &http.Client{Timeout: time.Second},
	})
	w := get(mux, "/livez")
	require.Equal(t, 503, w.Code)
	require.Contains(t, w.Body.String(), "not answering")
}

func TestMetricsServesPrometheusText(t *testing.T) {
	mux := ops.NewMux(ops.Deps{Ready: &pinger{}})
	w := get(mux, "/metrics")
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Header().Get("Content-Type"), "text/plain")
	require.Contains(t, w.Body.String(), "# TYPE kaimahi_decisions_total counter")
	require.Contains(t, w.Body.String(), "kaimahi_build_info{")
}
