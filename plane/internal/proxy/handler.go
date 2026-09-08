package proxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/kaimahi-agents/kaimahi/plane/internal/config"
	"github.com/kaimahi-agents/kaimahi/plane/internal/egress"
	"github.com/kaimahi-agents/kaimahi/plane/internal/meter"
	"github.com/kaimahi-agents/kaimahi/plane/internal/metrics"
	"github.com/kaimahi-agents/kaimahi/plane/internal/pricing"
	"github.com/kaimahi-agents/kaimahi/plane/internal/store"
)

const (
	maxRequestBody  = 10 << 20 // an LLM chat request; far beyond any sane prompt
	maxBufferedResp = 50 << 20
)

type handler struct {
	d Deps
	// ledgerDegraded trips when a ledger write fails and clears on the
	// next success. While tripped, the data plane denies: the meter reads
	// budgets from ledger sums, so unrecorded spend would silently
	// un-enforce every cap (fail closed — no ledger, no egress).
	ledgerDegraded atomic.Bool
}

// NewDataMux serves the governed data plane: the surface kagent's OpenAI
// client talks to. One route — POST /upstream/{name}/{path...} — plus a
// health probe. Everything else 404s with no upstream contact.
func NewDataMux(d Deps) *http.ServeMux {
	h := &handler{d: d}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /upstream/{name}/{path...}", h.forward)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	return mux
}

// record appends a ledger row on a cancel-free context: a client
// disconnect must not drop the record of a call the proxy already made
// (ported audit rule). Bounded so a stalled pool cannot hang the response.
func (h *handler) record(r *http.Request, e store.LedgerEntry, reservation string) {
	// Every ledger row leaves through here, so this is where WHO CALLED
	// is stamped: the client's own word for itself, and the address the
	// plane observed. The spend trail had the same blind spot the tool
	// trail did — nothing in a row told an agent apart from a script
	// holding its token. It decides nothing; the budget gate and the
	// price gate ran before this and do not read it.
	caller := store.CallerOf(r)
	e.CallerClaim, e.CallerAddr = caller.Claim, caller.Addr
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	if err := h.d.Store.RecordLedger(ctx, e, reservation); err != nil {
		h.ledgerDegraded.Store(true)
		metrics.SetDegraded(metrics.SeamProxy, true)
		slog.Error("proxy: ledger append failed; denying traffic until a write succeeds",
			"credential", e.CredentialName, "upstream", e.Upstream, "status", e.Status, "err", err)
		return
	}
	h.ledgerDegraded.Store(false)
	metrics.SetDegraded(metrics.SeamProxy, false)
}

// attribute resolves who the call is being made for. A store failure is
// an attribution that was LOST, not an absence of one: it is stamped
// 'unknown' and logged, and the request continues — attribution
// describes a call, it does not authorise one.
func (h *handler) attribute(r *http.Request, cred store.Credential) store.Attribution {
	att, err := h.d.Store.ActorFor(r.Context(), cred.Name)
	if err != nil {
		slog.Error("proxy: attribution read failed; the call is recorded as unknown",
			"credential", cred.Name, "err", err)
	}
	return att
}

// reasonFor classifies a refusal for the decisions metric — a fixed
// vocabulary keyed on the messages this package writes, never the
// message itself (free text is not a label value).
func reasonFor(status int, msg string) metrics.Reason {
	switch {
	case strings.HasPrefix(msg, "unknown upstream"), strings.HasPrefix(msg, "path not allowed"):
		return metrics.ReasonRoute
	case strings.HasPrefix(msg, "request body"):
		return metrics.ReasonBadRequest
	case strings.HasPrefix(msg, "model has no configured price"):
		return metrics.ReasonUnpricedModel
	case strings.HasPrefix(msg, "spend ledger unavailable"):
		return metrics.ReasonAuditDegraded
	case strings.HasPrefix(msg, "monthly"):
		return metrics.ReasonBudget
	case strings.HasPrefix(msg, "metering unavailable"):
		return metrics.ReasonMetering
	case strings.HasPrefix(msg, "upstream credential unavailable"):
		return metrics.ReasonUpstreamCredential
	case strings.HasPrefix(msg, "upstream request build failed"):
		return metrics.ReasonUpstreamUnreachable
	case strings.HasPrefix(msg, store.ExpiredPrefix):
		return metrics.ReasonCredentialExpired
	case status == http.StatusUnauthorized:
		return metrics.ReasonUnauthorized
	case status == http.StatusTooManyRequests:
		return metrics.ReasonBudget
	}
	return metrics.ReasonOther
}

// deny is the single exit for every pre-forward refusal: the denial is
// ledgered (zero usage, cost_source=denied), then answered. reservation
// is the hold an already-admitted call took — a refusal after
// admission (no upstream credential, an unbuildable request) releases
// it through the same ledger write; empty before admission.
func (h *handler) deny(w http.ResponseWriter, r *http.Request, cred store.Credential,
	att store.Attribution, upstream, model string, status int, msg string, reservation string) {
	h.record(r, store.LedgerEntry{
		CredentialName: cred.Name,
		Upstream:       upstream,
		Model:          model,
		CostSource:     "denied",
		Status:         status,
		ActedFor:       att.ActedFor,
		RunID:          att.RunID,
	}, reservation)
	metrics.Decide(metrics.SeamProxy, metrics.Denied, reasonFor(status, msg))
	http.Error(w, msg, status)
}

func (h *handler) forward(w http.ResponseWriter, r *http.Request) {
	// Authenticate the Kaimahi-issued token. It travels where the OpenAI
	// API key would (the SDK can send exactly one credential header) and
	// is known to the store only by hash.
	bearer, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if bearer == "" {
		metrics.Decide(metrics.SeamProxy, metrics.Denied, metrics.ReasonUnauthorized)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	hash := sha256.Sum256([]byte(bearer))
	cred, err := h.d.Store.CredentialByTokenHash(r.Context(), hash[:])
	if errors.Is(err, store.ErrNotFound) {
		metrics.Decide(metrics.SeamProxy, metrics.Denied, metrics.ReasonUnauthorized)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if err != nil {
		// Fail closed, but distinguishably: no credential visibility, no egress.
		slog.Error("proxy: credential lookup failed", "err", err)
		metrics.Decide(metrics.SeamProxy, metrics.Denied, metrics.ReasonCredentialStore)
		http.Error(w, "credential store unavailable", http.StatusServiceUnavailable)
		return
	}

	// Identity on the call: who this credential is acting for RIGHT NOW,
	// resolved once, at the door, and stamped on every row this request
	// writes — denials included. Resolution is not enforcement: a failed
	// read is stamped 'unknown' ("we cannot say"), never 'none' ("nobody
	// was there"), and never admits or denies anything on its own.
	att := h.attribute(r, cred)

	name := r.PathValue("name")

	// Credentials expire. A token bounded by allowlist, budget and
	// constraint but not by TIME lives until someone deletes its row,
	// which is the one thing nobody does. Checked here rather than
	// filtered out of the lookup above, so an operator is told what is
	// actually wrong instead of hunting an "unknown token".
	if cred.Expired(time.Now()) {
		h.deny(w, r, cred, att, name, "", http.StatusForbidden, store.ExpiredMessage(cred), "")
		return
	}

	// Authorize the route. One (method, path) per upstream IS the blast
	// radius: anything else is denied before any upstream contact.
	up, ok := h.d.Config.Upstreams[name]
	if !ok {
		h.deny(w, r, cred, att, name, "", http.StatusForbidden, "unknown upstream", "")
		return
	}
	if r.PathValue("path") != up.Path {
		h.deny(w, r, cred, att, name, "", http.StatusForbidden, "path not allowed", "")
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBody))
	if err != nil {
		h.deny(w, r, cred, att, name, "", http.StatusBadRequest, "request body unreadable or too large", "")
		return
	}
	var req struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		h.deny(w, r, cred, att, name, "", http.StatusBadRequest, "request body is not valid JSON", "")
		return
	}

	// Priced-pair gate (ported pattern): under a cents budget, a metered
	// model with no configured price cannot be admitted — its spend could
	// never be charged against the budget. Fail closed; a token-only
	// budget still governs unpriced models.
	price, priced := up.Prices[req.Model]
	if up.Classification == config.ClassMetered && cred.CapCents != nil && !priced {
		h.deny(w, r, cred, att, name, req.Model, http.StatusForbidden,
			"model has no configured price; a cents budget requires one (set a price or use a token budget)", "")
		return
	}

	// A tripped ledger fails the plane closed: budgets are enforced from
	// ledger sums, so spend that cannot be recorded must not happen. The
	// denial's own record attempt is the recovery probe.
	if h.ledgerDegraded.Load() {
		h.deny(w, r, cred, att, name, req.Model, http.StatusServiceUnavailable, "spend ledger unavailable", "")
		return
	}

	// Budget admission, fail closed and exact: one locked store
	// transaction decides and, under a cap, holds the least this call
	// can spend until its ledger row lands. Everything past this point
	// carries the reservation to the ledger write that consumes it.
	res, err := h.d.Meter.Reserve(r.Context(), cred, up.Classification == config.ClassMetered && priced)
	if err != nil {
		status := http.StatusForbidden
		msg := err.Error()
		var d meter.Denial
		if errors.As(err, &d) && (d.Status == http.StatusForbidden || d.Status == http.StatusTooManyRequests) {
			status = d.Status
		}
		// Deny-and-pend: a budget-cap denial files a pending
		// approval request (deduped in the store). Filing failure never
		// un-denies — the denial is the safe state.
		if d.BudgetSubject != "" {
			fctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
			filing := store.Filing{Credential: cred.Name, Kind: "budget", Subject: d.BudgetSubject,
				Detail: "denied " + req.Model + " via upstream " + name}
			if _, ferr := h.d.Store.FileApprovalRequest(fctx, filing); ferr != nil {
				slog.Error("proxy: filing approval request failed (denial stands)",
					"credential", cred.Name, "subject", d.BudgetSubject, "err", ferr)
			} else {
				msg += "; approval request filed — run 'make approvals'"
			}
			cancel()
		}
		h.deny(w, r, cred, att, name, req.Model, status, msg, "")
		return
	}

	// Resolve the real upstream credential — a Secret-mounted file only
	// the proxy can read; the agent side never sees it. Read per request
	// so rotation (Copilot's tokens expire) needs no restart.
	var secret string
	if up.CredentialFile != "" {
		raw, err := os.ReadFile(up.CredentialFile)
		secret = strings.TrimSpace(string(raw))
		if err != nil || secret == "" {
			slog.Error("proxy: upstream credential unavailable", "upstream", name, "err", err)
			h.deny(w, r, cred, att, name, req.Model, http.StatusServiceUnavailable,
				"upstream credential unavailable", res.ID)
			return
		}
	}

	// On streamed requests ask the upstream to append the usage chunk;
	// without it a streamed response would be unmeterable.
	outBody := body
	if req.Stream {
		if outBody, err = withIncludeUsage(body); err != nil {
			h.deny(w, r, cred, att, name, req.Model, http.StatusBadRequest, "request body is not a JSON object", res.ID)
			return
		}
	}

	outReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
		strings.TrimSuffix(up.BaseURL, "/")+"/"+up.Path, bytes.NewReader(outBody))
	if err != nil {
		h.deny(w, r, cred, att, name, req.Model, http.StatusBadGateway, "upstream request build failed", res.ID)
		return
	}
	copyRequestHeaders(outReq.Header, r.Header)
	outReq.ContentLength = int64(len(outBody))
	if up.CredentialFile != "" {
		header := up.CredentialHeader
		if header == "" || strings.EqualFold(header, "authorization") {
			outReq.Header.Set("Authorization", "Bearer "+secret)
		} else {
			outReq.Header.Set(header, secret)
		}
	}
	for k, v := range up.ExtraHeaders {
		outReq.Header.Set(k, v)
	}

	// The admission stands whatever the upstream does next; the outcome
	// is what the metric's reason carries (ok, an upstream error, or
	// unreachable), and the latency is the upstream's, measured here.
	admitted := metrics.Allowed
	admittedBy := metrics.ReasonOK
	if res.Granted {
		admitted, admittedBy = metrics.Granted, metrics.ReasonBudget
	}
	started := time.Now()
	var resp *http.Response
	client, err := h.d.clientFor(up)
	if err == nil {
		resp, err = client.Do(outReq)
	}
	if err != nil {
		slog.Error("proxy: upstream call failed", "upstream", name, "err", err)
		metrics.ObserveUpstream(metrics.SeamProxy, name, time.Since(started))
		reason := metrics.ReasonUpstreamUnreachable
		if egress.IsRefusal(err) {
			reason = metrics.ReasonEgressRefused
		}
		metrics.Decide(metrics.SeamProxy, admitted, reason)
		// The attempt is ledgered even though it failed — spend is
		// recorded before failures are honored (standing guidance); a
		// transport failure has no usage to bill, so tokens are zero.
		h.record(r, ledgerFor(cred, att, name, req.Model, up, priced, price, usage{}, http.StatusBadGateway), res.ID)
		http.Error(w, "upstream unreachable", http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	var u usage
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		copyResponseHeaders(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)
		u = relayStream(w, resp.Body)
	} else {
		// Buffer BEFORE the status goes to the client: a body the
		// hardened client cuts — too large, or stalled past its lifetime —
		// must fail closed as a 502, never reach the agent as a 200 with
		// half a payload, and must still be ledgered (spend is recorded
		// before failures are honored; the usage envelope never arrived,
		// so the tokens are zero and the row says 502).
		raw, err := readBounded(resp.Body)
		if err != nil {
			slog.Error("proxy: upstream response cut; failing closed", "upstream", name, "err", err)
			metrics.ObserveUpstream(metrics.SeamProxy, name, time.Since(started))
			metrics.Decide(metrics.SeamProxy, admitted, metrics.ReasonUpstreamError)
			h.record(r, ledgerFor(cred, att, name, req.Model, up, priced, price, usage{}, http.StatusBadGateway), res.ID)
			http.Error(w, "upstream response cut", http.StatusBadGateway)
			return
		}
		copyResponseHeaders(w.Header(), resp.Header)
		w.Header().Del("Content-Length")
		w.WriteHeader(resp.StatusCode)
		u = relayBuffered(w, raw)
	}
	metrics.ObserveUpstream(metrics.SeamProxy, name, time.Since(started))
	if resp.StatusCode < 300 && u == (usage{}) {
		slog.Warn("proxy: no usage in upstream response; ledgering zero tokens",
			"upstream", name, "model", req.Model, "stream", req.Stream)
	}
	if resp.StatusCode < 300 {
		metrics.Decide(metrics.SeamProxy, admitted, admittedBy)
	} else {
		metrics.Decide(metrics.SeamProxy, admitted, metrics.ReasonUpstreamError)
	}
	h.record(r, ledgerFor(cred, att, name, req.Model, up, priced, price, u, resp.StatusCode), res.ID)
}

type usage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
}

// ledgerFor prices one forwarded call. cost_source is explicit: 'free' is
// a classification, never an inference; 'unpriced' keeps the token counts
// honest when a metered model has no configured price.
func ledgerFor(cred store.Credential, att store.Attribution, upstream, model string, up config.Upstream,
	priced bool, price pricing.Price, u usage, status int) store.LedgerEntry {
	// Usage is upstream-reported input, not truth: clamp to the ledger's
	// valid range so a hostile count can neither wrap the cost math nor
	// fail the row's CHECK constraints (which would trip the plane).
	clamp := func(n int64) int64 {
		return min(max(n, 0), pricing.MaxTokens)
	}
	in, out := clamp(u.PromptTokens), clamp(u.CompletionTokens)
	e := store.LedgerEntry{
		CredentialName: cred.Name,
		Upstream:       upstream,
		Model:          model,
		InputTokens:    in,
		OutputTokens:   out,
		Status:         status,
		ActedFor:       att.ActedFor,
		RunID:          att.RunID,
	}
	switch {
	case up.Classification == config.ClassFree:
		e.CostSource = "free"
	case priced:
		e.CostSource = "priced"
		e.CostCents = pricing.CostCents(price, in, out)
	default:
		e.CostSource = "unpriced"
	}
	return e
}

// readBounded reads a whole non-streamed body, refusing one larger than
// the buffer (the hardened client caps hosted bodies at its own 8 MiB
// bound; this 50 MiB one is for in-cluster upstreams) — an error, never a silent
// truncation.
func readBounded(body io.Reader) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(body, maxBufferedResp+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > maxBufferedResp {
		return nil, egress.ErrResponseTooLarge
	}
	return raw, nil
}

// relayBuffered writes a fully read non-streamed response through while
// extracting usage from the JSON body. The relay is byte-faithful: parse
// failures only cost usage extraction, never the response.
func relayBuffered(w http.ResponseWriter, raw []byte) usage {
	_, _ = w.Write(raw)
	var envelope struct {
		Usage usage `json:"usage"`
	}
	_ = json.Unmarshal(raw, &envelope)
	return envelope.Usage
}

// relayStream forwards SSE lines as they arrive (flushing each) and scans
// the data chunks for the final usage payload requested via
// stream_options.include_usage.
func relayStream(w http.ResponseWriter, body io.Reader) usage {
	flusher, _ := w.(http.Flusher)
	var u usage
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for sc.Scan() {
		line := sc.Text()
		_, _ = io.WriteString(w, line+"\n")
		if flusher != nil {
			flusher.Flush()
		}
		payload, ok := strings.CutPrefix(line, "data: ")
		if !ok || payload == "[DONE]" {
			continue
		}
		var chunk struct {
			Usage *usage `json:"usage"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err == nil && chunk.Usage != nil {
			u = *chunk.Usage
		}
	}
	if err := sc.Err(); err != nil {
		slog.Error("proxy: relaying stream", "err", err)
	}
	return u
}

// withIncludeUsage sets stream_options.include_usage on the request JSON,
// preserving any other stream options the client sent.
func withIncludeUsage(body []byte) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	opts := map[string]json.RawMessage{}
	if raw, ok := m["stream_options"]; ok {
		if err := json.Unmarshal(raw, &opts); err != nil {
			return nil, err
		}
	}
	opts["include_usage"] = json.RawMessage("true")
	rawOpts, err := json.Marshal(opts)
	if err != nil {
		return nil, err
	}
	m["stream_options"] = rawOpts
	return json.Marshal(m)
}

// hopByHop are headers that must not be forwarded in either direction.
var hopByHop = map[string]bool{
	"Connection": true, "Keep-Alive": true, "Proxy-Authenticate": true,
	"Proxy-Authorization": true, "Te": true, "Trailer": true,
	"Transfer-Encoding": true, "Upgrade": true,
}

// copyRequestHeaders forwards client headers minus hop-by-hop, every
// credential slot (the Kaimahi token must never reach the upstream — the
// real credential goes in the upstream's native slot only), and
// Accept-Encoding (the proxy must read plaintext bodies to meter usage).
func copyRequestHeaders(dst, src http.Header) {
	for k, vs := range src {
		ck := http.CanonicalHeaderKey(k)
		if hopByHop[ck] || ck == "Authorization" || ck == "X-Api-Key" ||
			ck == "Api-Key" || ck == "Accept-Encoding" || ck == "Content-Length" || ck == "Host" {
			continue
		}
		dst[ck] = append([]string(nil), vs...)
	}
}

func copyResponseHeaders(dst, src http.Header) {
	for k, vs := range src {
		ck := http.CanonicalHeaderKey(k)
		if hopByHop[ck] {
			continue
		}
		dst[ck] = append([]string(nil), vs...)
	}
}
