// Package ops is the plane's operations listener: Prometheus
// metrics and the two probes, on a port of their own that no data
// Service exposes and no agent or edge reaches. No auth — the port is
// cluster-internal under the namespace default-deny, opened only to a
// scraper by an explicit NetworkPolicy allowance (kubelet's probes are
// node-originated and need none).
//
// The two probes mean different things and must never be confused:
//
//   - /readyz says "route traffic to me": it needs Postgres, because a
//     plane that cannot read credentials or write the ledger fails every
//     call closed anyway. A Postgres outage drops readiness on every
//     replica — and nothing else.
//   - /livez says "this process is not worth keeping": it reports only
//     a LOCAL, unrecoverable fault — a data listener that no longer
//     answers on loopback, or a connection pool that has been fully
//     checked out with no acquire completing for a long time (a leak
//     or a deadlock; callers bound every query, so a slow database
//     cannot look like this). It never consults Postgres or an
//     upstream, so an outage anywhere else never restarts the proxy.
package ops

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/kaimahi-agents/kaimahi/plane/internal/metrics"
)

// Pinger is what readiness asks: the pool's Ping. *pgxpool.Pool
// satisfies it.
type Pinger interface {
	Ping(ctx context.Context) error
}

// PoolStats is the slice of pgxpool.Stat liveness reads.
type PoolStats struct {
	Acquired     int32
	Max          int32
	AcquireCount int64
}

type Deps struct {
	Ready Pinger
	// Draining is set by main on SIGTERM: readiness answers 503 from
	// that moment (the conventional shape — stop being routed to before
	// the listeners close), while liveness is unaffected.
	Draining *atomic.Bool
	// Stats reports the pool right now; nil skips the stall check.
	Stats func() PoolStats
	// Listeners are loopback ORIGINS of the data listeners
	// (scheme://host:port); liveness GETs each one's /healthz —
	// unconditional "ok" handlers, so this proves only that the listener
	// answers. The scheme is carried per listener rather than assumed;
	// both production data seams serve TLS.
	Listeners []string
	// StallAfter is how long the pool may be saturated with no acquire
	// completing before the process is declared stuck (default 60s).
	StallAfter time.Duration
	Now        func() time.Time
	Client     *http.Client
}

type handler struct {
	d  Deps
	mu sync.Mutex
	// Stall detection state: the last acquire count seen and the last
	// time the pool was seen making progress (or not saturated).
	lastAcquire int64
	healthyAt   time.Time
}

// NewMux serves /metrics, /readyz and /livez.
func NewMux(d Deps) *http.ServeMux {
	if d.StallAfter <= 0 {
		d.StallAfter = 60 * time.Second
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Client == nil {
		d.Client = &http.Client{Timeout: 2 * time.Second}
	}
	h := &handler{d: d, healthyAt: d.Now()}
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(metrics.Registry(), promhttp.HandlerOpts{}))
	mux.HandleFunc("GET /readyz", h.ready)
	mux.HandleFunc("GET /livez", h.live)
	return mux
}

func (h *handler) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if h.d.Draining != nil && h.d.Draining.Load() {
		http.Error(w, "not ready: draining", http.StatusServiceUnavailable)
		return
	}
	if h.d.Ready == nil {
		http.Error(w, "not ready: no store", http.StatusServiceUnavailable)
		return
	}
	if err := h.d.Ready.Ping(ctx); err != nil {
		http.Error(w, "not ready: store unreachable", http.StatusServiceUnavailable)
		return
	}
	_, _ = w.Write([]byte("ready"))
}

func (h *handler) live(w http.ResponseWriter, r *http.Request) {
	if reason := h.stalled(); reason != "" {
		slog.Error("ops: liveness failing (local fault)", "reason", reason)
		http.Error(w, "not live: "+reason, http.StatusServiceUnavailable)
		return
	}
	for _, addr := range h.d.Listeners {
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, addr+"/healthz", nil)
		if err != nil {
			http.Error(w, "not live: bad listener address", http.StatusServiceUnavailable)
			return
		}
		resp, err := h.d.Client.Do(req)
		if err != nil {
			slog.Error("ops: liveness failing (listener not answering on loopback)", "listener", addr, "err", err)
			http.Error(w, fmt.Sprintf("not live: listener %s not answering", addr), http.StatusServiceUnavailable)
			return
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			http.Error(w, fmt.Sprintf("not live: listener %s answered %d", addr, resp.StatusCode), http.StatusServiceUnavailable)
			return
		}
	}
	_, _ = w.Write([]byte("live"))
}

// stalled samples the pool: it is fine whenever it is not fully
// checked out, or when any acquire completed since the last sample.
// Only a pool that stays saturated with no acquire completing for
// StallAfter is a fault — and a local one: every caller bounds its
// query, so a slow or absent database returns connections; only a
// caller that never returns one keeps the pool full.
func (h *handler) stalled() string {
	if h.d.Stats == nil {
		return ""
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	st := h.d.Stats()
	now := h.d.Now()
	if st.Max <= 0 || st.Acquired < st.Max || st.AcquireCount != h.lastAcquire {
		h.healthyAt = now
	}
	h.lastAcquire = st.AcquireCount
	if since := now.Sub(h.healthyAt); since > h.d.StallAfter {
		return fmt.Sprintf("connection pool saturated with no progress for %s", since.Truncate(time.Second))
	}
	return ""
}
