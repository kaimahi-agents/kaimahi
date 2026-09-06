// kaimahi-proxy is the Kaimahi governance plane: the metering and
// enforcing LLM proxy mounted at kagent's ModelConfig baseUrl seam, the
// enforcing MCP gateway mounted at the tool-server seam, and the
// inbound bridge (the plane's one ingress: webhook → governed A2A
// invoke). Five listeners: the LLM data plane, the MCP gateway (own
// Service), the inbound bridge (own Service), the admin plane
// (credentials, budgets, allowlists, ledger, audits) on a port no data
// Service exposes, and the operations listener — Prometheus
// metrics and the readiness/liveness probes — on a port no Service
// exposes at all.
//
// Secrets reach the process only as mounted files (never argv or env
// values); non-secret wiring is env. Migrations run at startup under a
// Postgres advisory lock — idempotent and replica-safe, so a rollout of
// N replicas is its own migration step. The process holds no
// governance state: every budget, grant, dedupe and decision is exact
// in Postgres, so any number of replicas agree.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaimahi-agents/kaimahi/plane/internal/config"
	"github.com/kaimahi-agents/kaimahi/plane/internal/db"
	"github.com/kaimahi-agents/kaimahi/plane/internal/gateway"
	"github.com/kaimahi-agents/kaimahi/plane/internal/inbound"
	"github.com/kaimahi-agents/kaimahi/plane/internal/meter"
	"github.com/kaimahi-agents/kaimahi/plane/internal/metrics"
	"github.com/kaimahi-agents/kaimahi/plane/internal/notify"
	"github.com/kaimahi-agents/kaimahi/plane/internal/ops"
	"github.com/kaimahi-agents/kaimahi/plane/internal/proxy"
	"github.com/kaimahi-agents/kaimahi/plane/internal/redact"
	"github.com/kaimahi-agents/kaimahi/plane/internal/store"
)

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func mustReadSecretFile(path, what string) string {
	raw, err := os.ReadFile(path)
	v := strings.TrimSpace(string(raw))
	if err != nil || v == "" {
		slog.Error("missing required secret file", "what", what, "path", path, "err", err)
		os.Exit(1)
	}
	return v
}

func main() {
	dataAddr := env("DATA_ADDR", ":8080")
	mcpAddr := env("MCP_ADDR", ":8081")
	inboundAddr := env("INBOUND_ADDR", ":8082")
	adminAddr := env("ADMIN_ADDR", ":9091")
	// The operations listener — Prometheus metrics and the two
	// probes — on a port of its own that no Service exposes.
	opsAddr := env("OPS_ADDR", ":9092")
	// The kagent controller's origin: the ONLY place the inbound bridge
	// dials (per-agent A2A endpoints live under it).
	a2aBase := env("A2A_BASE", inbound.DefaultA2ABase)
	configFile := env("CONFIG_FILE", "/etc/kaimahi/upstreams.json")
	// The operator overlay. Fragments an operator added by
	// onboarding their own MCP server (`kmx tools add`) live in their
	// own ConfigMap, mounted here, and are merged over the committed
	// table at boot. The volume is optional: an absent directory is an
	// empty overlay, which is every cluster where nobody has onboarded
	// anything. Set CONFIG_DIR="" to read the base table alone.
	configDir := env("CONFIG_DIR", config.DefaultConfigDir)
	adminTokenFile := env("ADMIN_TOKEN_FILE", "/etc/kaimahi/admin/token")
	pgPasswordFile := env("PGPASSWORD_FILE", "/etc/kaimahi/pg/password")

	pgPassword := mustReadSecretFile(pgPasswordFile, "postgres password")
	adminToken := mustReadSecretFile(adminTokenFile, "admin token")

	configBase, fragments, err := config.Read(configFile, configDir)
	if err != nil {
		slog.Error("reading upstream config", "err", err)
		os.Exit(1)
	}
	mergedConfig, err := config.Merge(configBase, fragments)
	if err != nil {
		slog.Error("merging the operator overlay", "err", err, "dir", configDir)
		os.Exit(1)
	}
	cfg, err := config.Parse(mergedConfig)
	if err != nil {
		slog.Error("loading upstream config", "err", err)
		os.Exit(1)
	}
	for _, f := range fragments {
		slog.Info("operator overlay merged", "fragment", f.Name, "dir", configDir)
	}

	// Redacting logger: defense in depth — nothing logs secrets on
	// purpose; this catches accidents. Values known at boot only; a
	// rotated upstream credential regains redaction on the next rollout.
	secrets := []string{pgPassword, adminToken}
	for _, u := range cfg.Upstreams {
		if u.CredentialFile == "" {
			continue
		}
		if raw, err := os.ReadFile(u.CredentialFile); err == nil {
			secrets = append(secrets, strings.TrimSpace(string(raw)))
		} else {
			// Not fatal (the upstream is optional and read per request),
			// but say so: this credential is outside the redactor until
			// the next rollout.
			slog.Warn("upstream credential unreadable at boot; value not redacted in logs",
				"file", u.CredentialFile, "err", err)
		}
	}
	// Tool upstream credentials too: the GitHub token is plane
	// custody exactly like the Copilot one, and must be redacted the same.
	for name, t := range cfg.ToolUpstreams {
		if t.CredentialFile == "" {
			continue
		}
		if raw, err := os.ReadFile(t.CredentialFile); err == nil {
			secrets = append(secrets, strings.TrimSpace(string(raw)))
		} else {
			slog.Warn("tool upstream credential unreadable at boot; value not redacted in logs",
				"upstream", name, "file", t.CredentialFile, "err", err)
		}
	}
	for name, h := range cfg.InboundHooks {
		if h.SigningSecretFile == "" {
			continue
		}
		if raw, err := os.ReadFile(h.SigningSecretFile); err == nil {
			secrets = append(secrets, strings.TrimSpace(string(raw)))
		} else {
			slog.Warn("inbound signing secret unreadable at boot; value not redacted in logs",
				"hook", name, "file", h.SigningSecretFile, "err", err)
		}
	}
	slog.SetDefault(slog.New(redact.Handler{
		Inner: slog.NewTextHandler(os.Stderr, nil),
		R:     redact.New(secrets),
	}))

	dsn := fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable",
		url.QueryEscape(env("PGUSER", "kaimahi")), url.QueryEscape(pgPassword),
		env("PGHOST", "kaimahi-postgres"), env("PGPORT", "5432"),
		url.QueryEscape(env("PGDATABASE", "kaimahi")))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	// Postgres may still be starting alongside us; retry rather than
	// crash-loop through image pulls.
	var pool = retryConnect(ctx, dsn)
	if pool == nil {
		os.Exit(1)
	}
	defer pool.Close()

	st := store.New(pool)
	mtr := &meter.Meter{Store: st}
	// The store-derived metrics (ledger totals by credential name, live
	// grants, open holds) are read at scrape time — replica-independent
	// truths that live in Postgres, not in this process.
	metrics.RegisterStore(st, func() time.Time { return meter.MonthStartUTC(time.Now()) })
	metrics.PrimeUpstreams(metrics.SeamProxy, slices.Sorted(maps.Keys(cfg.Upstreams)))
	metrics.PrimeUpstreams(metrics.SeamGateway, slices.Sorted(maps.Keys(cfg.ToolUpstreams)))

	// The approval notifier and the Slack command replier are one
	// poster: a governed post through the plane's OWN gateway listener
	// (loopback) under the plane's own credential. Optional — without
	// the config block nobody is told, exactly as before. Every data
	// path gets the store wrapped so the ONE filing function notifies
	// once per fresh filing, whichever site filed it; with no notifier
	// the wrapper is the store.
	filing := notify.Store{Store: st, Filer: st}
	var poster *notify.Poster
	gatewayURL := "http://127.0.0.1" + portOf(mcpAddr)
	if n := cfg.ApprovalNotifier; n != nil {
		poster = notify.New(notify.Deps{
			GatewayURL:     gatewayURL,
			Upstream:       n.ToolUpstream,
			Tool:           n.Tool,
			CredentialFile: n.CredentialFile,
			ChannelFile:    n.ChannelFile,
		})
		filing.N = poster
	}
	deps := proxy.Deps{
		Store:      filing,
		Meter:      mtr,
		Config:     cfg,
		ConfigBase: configBase,
	}
	// The ONE hardened client for every upstream marked internet —
	// Copilot on the LLM seam, the hosted MCP servers on the tool seam —
	// built once, each host vetted now (a private answer refuses the
	// config loudly here), and injected into BOTH seams below.
	internetClient, err := hardenedClient(ctx, cfg)
	if err != nil {
		slog.Error("hosted upstream configuration refused", "err", err)
		os.Exit(1)
	}

	// ReadTimeout bounds slow request-body writers (chat requests are
	// small; streamed RESPONSES are unaffected — WriteTimeout stays 0 so
	// long generations can flush indefinitely).
	// The MCP gateway shares this process (and its pool, redactor,
	// and fail-closed machinery); its listener gets its own Service so
	// the tool seam has its own address.
	gwDeps := gateway.Deps{Store: filing, Upstreams: cfg.ToolUpstreams, Policy: cfg.Policy()}
	deps, gwDeps = wireInternet(deps, gwDeps, internetClient)
	// The inbound bridge: same process, same pool and fail-closed
	// machinery, its own Service. Its workers invoke agents asynchronously
	// and run until shutdown.
	bridgeDeps := inbound.Deps{Store: filing, Meter: mtr, Hooks: cfg.InboundHooks, A2ABase: a2aBase}
	if poster != nil {
		bridgeDeps.Replier = poster
	}
	bridge := inbound.New(bridgeDeps)
	bridgeCtx, stopBridge := context.WithCancel(context.Background())
	bridgeDone := make(chan struct{})
	go func() { bridge.Run(bridgeCtx); close(bridgeDone) }()
	// The poster outlives the bridge: a filing during the servers' drain
	// still gets its post, and shutdown then bounds the poster like it
	// bounds the bridge.
	posterCtx, stopPoster := context.WithCancel(context.Background())
	posterDone := make(chan struct{})
	if poster != nil {
		go func() { poster.Run(posterCtx); close(posterDone) }()
	} else {
		close(posterDone)
	}

	dataSrv := &http.Server{Addr: dataAddr, Handler: proxy.NewDataMux(deps),
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 2 * time.Minute, IdleTimeout: 2 * time.Minute}
	mcpSrv := &http.Server{Addr: mcpAddr, Handler: gateway.NewMux(gwDeps),
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 2 * time.Minute, IdleTimeout: 2 * time.Minute}
	// Webhook payloads are small and bounded per hook; a slow writer gets
	// 30 seconds, not two minutes.
	inboundSrv := &http.Server{Addr: inboundAddr, Handler: bridge.Mux(),
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 2 * time.Minute}
	adminSrv := &http.Server{Addr: adminAddr, Handler: proxy.NewAdminMux(deps, adminTokenFile),
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 2 * time.Minute}

	// Readiness needs Postgres (a plane that cannot read credentials
	// or write the ledger fails every call closed anyway); liveness
	// reports only a LOCAL fault — a data listener not answering on
	// loopback, or a pool checked out with no progress — so a Postgres
	// outage or a slow upstream never restarts the proxy.
	var draining atomic.Bool
	opsSrv := &http.Server{Addr: opsAddr, Handler: ops.NewMux(ops.Deps{
		Ready:    pool,
		Draining: &draining,
		Stats: func() ops.PoolStats {
			s := pool.Stat()
			return ops.PoolStats{Acquired: s.AcquiredConns(), Max: s.MaxConns(), AcquireCount: s.AcquireCount()}
		},
		Listeners: []string{"127.0.0.1" + portOf(dataAddr), "127.0.0.1" + portOf(mcpAddr), "127.0.0.1" + portOf(inboundAddr)},
	}), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 2 * time.Minute}

	errCh := make(chan error, 5)
	go func() { errCh <- dataSrv.ListenAndServe() }()
	go func() { errCh <- mcpSrv.ListenAndServe() }()
	go func() { errCh <- inboundSrv.ListenAndServe() }()
	go func() { errCh <- adminSrv.ListenAndServe() }()
	go func() { errCh <- opsSrv.ListenAndServe() }()
	slog.Info("kaimahi-proxy up", "data", dataAddr, "mcp", mcpAddr, "inbound", inboundAddr, "admin", adminAddr, "ops", opsAddr,
		"version", metrics.Version(),
		"upstreams", len(cfg.Upstreams), "tool_upstreams", len(cfg.ToolUpstreams), "inbound_hooks", len(cfg.InboundHooks),
		"hosted_upstreams", len(cfg.InternetHosts()),
		"approval_notifier", cfg.ApprovalNotifier != nil, "notifier_gateway", gatewayURL)

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		// Readiness drops first — stop being routed to — then the
		// listeners drain; the ops listener itself stays up through the
		// drain so the probe keeps answering (503) until the end.
		draining.Store(true)
		// Stop accepting events first, then let workers finish the
		// event they are on (bounded by the shutdown budget).
		_ = inboundSrv.Shutdown(shutdownCtx)
		stopBridge()
		_ = dataSrv.Shutdown(shutdownCtx)
		_ = mcpSrv.Shutdown(shutdownCtx)
		_ = adminSrv.Shutdown(shutdownCtx)
		// The ops listener closes last (a deferred call runs after the
		// drain below), so the probes answer throughout.
		defer func() { _ = opsSrv.Shutdown(shutdownCtx) }()
		select {
		case <-bridgeDone:
		case <-shutdownCtx.Done():
			slog.Warn("inbound workers did not drain before shutdown; queued events are lost (their admitted rows stand without an outcome)")
		}
		if poster != nil {
			if n := poster.Drain(shutdownCtx); n > 0 {
				slog.Warn("notifier did not drain before shutdown; queued posts are lost (the requests stay filed)", "queued", n)
			}
		}
		stopPoster()
		<-posterDone
	case err := <-errCh:
		// Any listener stopping before a shutdown signal is abnormal —
		// even ErrServerClosed — so exit nonzero and let Kubernetes
		// restart the pod rather than report a clean exit.
		slog.Error("server stopped unexpectedly", "err", err)
		os.Exit(1)
	}
}

func retryConnect(ctx context.Context, dsn string) *pgxpool.Pool {
	deadline := time.Now().Add(90 * time.Second)
	for {
		// Bound each attempt — migrate and pool ping together — so a
		// hung connection cannot outlive the retry budget silently. The
		// pool itself is not tied to attemptCtx; it only bounds startup.
		attemptCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		pool, err := connectOnce(attemptCtx, dsn)
		cancel()
		if err == nil {
			return pool
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			slog.Error("database startup failed", "err", err)
			return nil
		}
		slog.Warn("waiting for postgres", "err", err)
		select {
		case <-time.After(3 * time.Second):
		case <-ctx.Done():
			return nil
		}
	}
}

func connectOnce(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	if err := db.Migrate(ctx, dsn); err != nil {
		return nil, err
	}
	return db.NewPool(ctx, dsn)
}

// portOf returns the ":port" part of a listen address such as ":8081" or
// "0.0.0.0:8081" — the loopback origin the notifier dials.
func portOf(addr string) string {
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		return addr[i:]
	}
	return ":" + addr
}
