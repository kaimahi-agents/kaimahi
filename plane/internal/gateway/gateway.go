// Package gateway is the enforcing MCP gateway: the governance seam
// between a kagent agent and the tool servers it calls. It RELAYS the MCP
// streamable-HTTP protocol (kagent still runs the tools — no MCP runtime
// here) and enforces, all fail-closed:
//
//   - upstream tool servers come only from the operator-owned upstream
//     table the proxy parsed at boot — the committed base plus any
//     operator overlay merged over it — and the gateway forwards
//     nowhere else, which IS the egress rule at this layer;
//   - protocol scope is tools only: initialize, notifications/initialized,
//     tools/list, tools/call (ping is answered locally, touching no
//     upstream); every other method is denied, not relayed;
//   - a per-credential tool allowlist is enforced on tools/call and
//     PROJECTED onto tools/list — an agent never sees a tool it cannot
//     call, and kagent's controller discovery sees the same projection;
//   - tools/call outcomes and every attributable denial are audited
//     (401/503 pre-auth refusals have no credential to attribute). Like
//     the spend ledger, the allowed row is written after the response it
//     describes; a failed write trips the gateway to 503 for all
//     SUBSEQUENT traffic until a write succeeds — the same
//     fail-closed-degradation contract the spend plane runs under.
//
// Authentication is exactly the LLM proxy's: a Kaimahi-issued kmh_ opaque
// token in the Authorization header (Bearer prefix optional — kagent's
// headersFrom sends the Secret value verbatim), known to the store only
// by sha256.
package gateway

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
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/kaimahi-agents/kaimahi/plane/internal/config"
	"github.com/kaimahi-agents/kaimahi/plane/internal/egress"
	"github.com/kaimahi-agents/kaimahi/plane/internal/metrics"
	"github.com/kaimahi-agents/kaimahi/plane/internal/store"
)

const (
	maxRequestBody  = 4 << 20 // JSON-RPC tool calls; far beyond any sane arguments payload
	maxBufferedResp = 8 << 20 // a buffered tools/list listing
)

// JSON-RPC error codes the gateway answers denials with. -32601 is the
// standard "method not found"; -32001 is an implementation-defined code
// for a tool outside the credential's allowlist.
const (
	codeMethodNotAllowed = -32601
	codeToolNotPermitted = -32001
)

// Store is what the gateway needs from Postgres. *store.Store satisfies it.
type Store interface {
	CredentialByTokenHash(ctx context.Context, tokenHash []byte) (store.Credential, error)
	ToolAllowlist(ctx context.Context, credentialName string) ([]string, error)
	RecordToolAudit(ctx context.Context, e store.ToolAuditEntry) error
	// Approvals: bounded grants admit tools outside the static
	// allowlist (consuming a use, liveness evaluated in SQL at call
	// time), and a denial files a pending approval request.
	// A grant admits one CALL — the digest of its canonical policy
	// fields must match — and a filed request carries that call. The one
	// exception is the closed legacy class: a grant recorded before
	// argument binding existed carries a NULL digest, is honoured for any
	// call on that tool, and is consumed only after an exact match. No
	// new one can be minted (store/approvals.go).
	ConsumeToolGrant(ctx context.Context, credential, tool, argDigest string) (grantID string, ok bool, err error)
	// Identity on the call: who the run this tool call falls inside is
	// being made for. Resolution only — never enforcement.
	ActorFor(ctx context.Context, credential string) (store.Attribution, error)
	LiveToolGrantSubjects(ctx context.Context, credential string) ([]string, error)
	FileApprovalRequest(ctx context.Context, f store.Filing) (filed bool, err error)
}

type Deps struct {
	Store     Store
	Upstreams map[string]config.ToolUpstream
	// Policy is the argument-policy surface built from the same
	// committed config: which argument fields each tool declares
	// policy-relevant, and the standing constraints each credential
	// carries on them. The zero value declares and constrains nothing,
	// so every call falls through to the allowlist and to approvals.
	Policy config.PolicySet
	// Client makes IN-CLUSTER upstream calls. Nil gets a default that
	// never FOLLOWS a redirect (standing guidance); the relay paths then
	// refuse the 3xx itself with a 502. Calls are bounded at 5 minutes.
	Client *http.Client
	// InternetClient makes every call to an upstream marked
	// `internet: true`: the ONE hardened client main builds
	// (internal/egress) and shares with the LLM proxy. Nil means no
	// hosted upstream can be reached — such a call fails closed (502,
	// audited) rather than falling back to the plain client.
	InternetClient *http.Client
}

func (d Deps) clientFor(up config.ToolUpstream) (*http.Client, error) {
	if up.Internet {
		if d.InternetClient == nil {
			return nil, egress.ErrNoClient
		}
		return d.InternetClient, nil
	}
	if d.Client != nil {
		return d.Client, nil
	}
	return &http.Client{
		Timeout: 5 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, nil
}

type handler struct {
	d Deps
	// auditDegraded trips when an audit write fails and clears on the
	// next success. While tripped the gateway denies everything: an
	// action that cannot be recorded must not happen (the rule the spend
	// ledger runs under, applied to tool calls).
	auditDegraded atomic.Bool
}

// NewMux serves the governed MCP surface. One relay route —
// /upstream/{name}/mcp — mirroring the LLM data plane's shape: POST
// carries every JSON-RPC message, DELETE terminates a session
// (terminateOnClose), and GET answers 405 via the mux (spec-legal: the
// gateway offers no server-initiated stream).
func NewMux(d Deps) *http.ServeMux {
	h := &handler{d: d}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /upstream/{name}/mcp", h.relay)
	mux.HandleFunc("DELETE /upstream/{name}/mcp", h.terminate)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	return mux
}

// audit appends one tool-audit row on a cancel-free context: a client
// disconnect must not drop the record of a decision already made.
func (h *handler) audit(r *http.Request, e store.ToolAuditEntry) {
	// Every tool-audit row leaves through here, so this is where the
	// identity the call was made for is stamped — there is no path that
	// can forget to.
	att := store.AttributionFrom(r.Context())
	e.ActedFor, e.RunID = att.ActedFor, att.RunID
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	if err := h.d.Store.RecordToolAudit(ctx, e); err != nil {
		h.auditDegraded.Store(true)
		metrics.SetDegraded(metrics.SeamGateway, true)
		slog.Error("gateway: audit append failed; denying tool traffic until a write succeeds",
			"credential", e.CredentialName, "upstream", e.Upstream, "method", e.Method, "err", err)
		return
	}
	h.auditDegraded.Store(false)
	metrics.SetDegraded(metrics.SeamGateway, false)
}

// reasonFor classifies a refusal for the decisions metric — a fixed
// vocabulary keyed on the messages this package writes, never the
// message itself.
func reasonFor(status int, msg string) metrics.Reason {
	switch {
	case strings.HasPrefix(msg, "unknown tool upstream"):
		return metrics.ReasonRoute
	case strings.HasPrefix(msg, "tool audit unavailable"):
		return metrics.ReasonAuditDegraded
	case strings.HasPrefix(msg, "request body"), strings.HasPrefix(msg, "JSON-RPC batches"),
		strings.HasPrefix(msg, "tools/call "), strings.HasPrefix(msg, "upstream tool listing"):
		return metrics.ReasonBadRequest
	case strings.HasPrefix(msg, "grant check unavailable"):
		return metrics.ReasonGrantCheck
	case strings.HasPrefix(msg, "tool allowlist unavailable"):
		return metrics.ReasonCredentialStore
	case strings.HasPrefix(msg, "tool not permitted"):
		return metrics.ReasonAllowlist
	case strings.HasPrefix(msg, "tool call not permitted"):
		return metrics.ReasonConstraint
	case strings.HasPrefix(msg, "method not relayed"):
		return metrics.ReasonMethod
	case strings.HasPrefix(msg, store.ExpiredPrefix):
		return metrics.ReasonCredentialExpired
	case status == http.StatusUnauthorized:
		return metrics.ReasonUnauthorized
	}
	return metrics.ReasonOther
}

// httpDeny refuses pre-protocol (plain HTTP status), audited.
func (h *handler) httpDeny(w http.ResponseWriter, r *http.Request, cred store.Credential,
	upstream, method, tool string, status int, msg string) {
	h.audit(r, store.ToolAuditEntry{CredentialName: cred.Name, Upstream: upstream,
		Method: method, Tool: tool, Decision: "denied", Status: status, Detail: msg})
	metrics.Decide(metrics.SeamGateway, metrics.Denied, reasonFor(status, msg))
	http.Error(w, msg, status)
}

// rpcDeny refuses in-protocol: a JSON-RPC error the MCP client surfaces
// cleanly, audited as a 403 denial. Notifications (no id) cannot carry a
// response, so they get the spec's 202 with an empty body.
func (h *handler) rpcDeny(w http.ResponseWriter, r *http.Request, cred store.Credential,
	upstream, method, tool string, id json.RawMessage, code int, msg string) {
	h.audit(r, store.ToolAuditEntry{CredentialName: cred.Name, Upstream: upstream,
		Method: method, Tool: tool, Decision: "denied", Status: http.StatusForbidden, Detail: msg})
	metrics.Decide(metrics.SeamGateway, metrics.Denied, reasonFor(http.StatusForbidden, msg))
	if len(id) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeRPC(w, map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": code, "message": msg},
	})
}

// rpcDenyCall is rpcDeny for a tools/call: the denial row carries the
// digest and summary of the call that was refused, so a denied attempt
// and the approval it files describe the same transaction.
func (h *handler) rpcDenyCall(w http.ResponseWriter, r *http.Request, cred store.Credential,
	upstream, method, tool string, b CallBinding, id json.RawMessage, code int, msg string) {
	h.audit(r, store.ToolAuditEntry{CredentialName: cred.Name, Upstream: upstream,
		Method: method, Tool: tool, Decision: "denied", Status: http.StatusForbidden, Detail: msg,
		ArgDigest: b.Digest, ArgSummary: b.Summary})
	metrics.Decide(metrics.SeamGateway, metrics.Denied, reasonFor(http.StatusForbidden, msg))
	if len(id) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeRPC(w, map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": code, "message": msg},
	})
}

func writeRPC(w http.ResponseWriter, msg any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(msg)
}

// authenticate resolves the inbound kmh_ token (Bearer prefix optional —
// headersFrom sends the Secret value verbatim). Same contract as the
// LLM proxy:
// unknown token 401, store failure 503, neither audited (no credential
// to attribute).
// It also returns the request to use from here on: the identity the
// credential is acting for is resolved once, at the door, and carried
// on its context so every audit row this request writes is stamped
// with it.
func (h *handler) authenticate(w http.ResponseWriter, r *http.Request) (store.Credential, *http.Request, bool) {
	token := r.Header.Get("Authorization")
	if after, ok := strings.CutPrefix(token, "Bearer "); ok {
		token = after
	}
	if token == "" {
		metrics.Decide(metrics.SeamGateway, metrics.Denied, metrics.ReasonUnauthorized)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return store.Credential{}, r, false
	}
	hash := sha256.Sum256([]byte(token))
	cred, err := h.d.Store.CredentialByTokenHash(r.Context(), hash[:])
	if errors.Is(err, store.ErrNotFound) {
		metrics.Decide(metrics.SeamGateway, metrics.Denied, metrics.ReasonUnauthorized)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return store.Credential{}, r, false
	}
	if err != nil {
		slog.Error("gateway: credential lookup failed", "err", err)
		metrics.Decide(metrics.SeamGateway, metrics.Denied, metrics.ReasonCredentialStore)
		http.Error(w, "credential store unavailable", http.StatusServiceUnavailable)
		return store.Credential{}, r, false
	}
	att, aerr := h.d.Store.ActorFor(r.Context(), cred.Name)
	if aerr != nil {
		// A lost attribution is recorded as lost and changes nothing
		// else: attribution describes a call, it does not authorise one.
		slog.Error("gateway: attribution read failed; the call is recorded as unknown",
			"credential", cred.Name, "err", aerr)
	}
	r = r.WithContext(store.WithAttribution(r.Context(), att))

	// Credentials expire. Refused here rather than filtered out of the
	// lookup, so the row and the message name the real problem — and
	// audited, because unlike an unknown token there IS a credential to
	// attribute the refusal to.
	if cred.Expired(time.Now()) {
		h.httpDeny(w, r, cred, r.PathValue("name"), "", "", http.StatusForbidden, store.ExpiredMessage(cred))
		return store.Credential{}, r, false
	}
	return cred, r, true
}

func (h *handler) relay(w http.ResponseWriter, r *http.Request) {
	cred, r, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	up, ok := h.d.Upstreams[name]
	if !ok {
		h.httpDeny(w, r, cred, name, "", "", http.StatusForbidden, "unknown tool upstream")
		return
	}

	// A tripped audit trail fails the gateway closed; the denial's own
	// record attempt is the recovery probe.
	if h.auditDegraded.Load() {
		h.httpDeny(w, r, cred, name, "", "", http.StatusServiceUnavailable, "tool audit unavailable")
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBody))
	if err != nil {
		h.httpDeny(w, r, cred, name, "", "", http.StatusBadRequest, "request body unreadable or too large")
		return
	}
	if trimmed := bytes.TrimSpace(body); len(trimmed) > 0 && trimmed[0] == '[' {
		// Single-message only: a batch could smuggle a denied method
		// past a first-element check. Fail closed.
		h.httpDeny(w, r, cred, name, "", "", http.StatusBadRequest, "JSON-RPC batches are not relayed")
		return
	}
	// Parse ONCE, into the canonical tree every consumer reads: the
	// policy decision, the argument digest, the audit summary and the
	// bytes forwarded upstream all come from it, so no parser difference
	// can put them out of step. A duplicated key at any depth is refused
	// here (canon.go), not collapsed.
	body, msg, err := canonicalize(body)
	if err != nil {
		h.httpDeny(w, r, cred, name, "", "", http.StatusBadRequest, canonRefusal(err))
		return
	}
	method, _ := msg["method"].(string)
	id := rawID(msg)

	switch method {
	case "initialize", "notifications/initialized":
		// The mandatory MCP lifecycle handshake, relayed verbatim.
		h.forward(w, r, name, up, body)

	case "ping":
		// Answered locally: the spec demands a prompt response, and a
		// liveness check earns no upstream contact through a governance
		// gateway.
		if len(id) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		writeRPC(w, map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{}})

	case "tools/list":
		allowed, ok := h.allowlist(w, r, cred, name, method)
		if !ok {
			return
		}
		if allowed, ok = h.projectable(w, r, cred, name, method, allowed); !ok {
			return
		}
		h.forwardProjected(w, r, cred, name, up, body, allowed)

	case "tools/call":
		tool := toolNameOf(msg)
		if tool == "" {
			h.httpDeny(w, r, cred, name, method, "", http.StatusBadRequest, "tools/call params carry no tool name")
			return
		}
		// Arguments are policy inputs now, so they must be an object
		// the plane can read. Anything else is refused rather than
		// forwarded unexamined.
		args, ok := argumentsOf(msg)
		if !ok {
			h.httpDeny(w, r, cred, name, method, tool, http.StatusBadRequest, "tools/call arguments must be a JSON object")
			return
		}
		fields, declared := h.d.Policy.Declared(tool)
		bind := Bind(tool, args, fields, declared)

		allowed, ok := h.allowlist(w, r, cred, name, method)
		if !ok {
			return
		}

		// Three ways a call is admitted, in order:
		//   1. a STANDING CONSTRAINT the call is inside — routine
		//      traffic proceeds with no human, and no grant is burned;
		//   2. the static allowlist — but ONLY where no constraint exists
		//      for this credential and tool, because a constraint is a
		//      BOUND ("may call payment_schedule when amount_cents <=
		//      1000000, and never otherwise"), not merely another way in;
		//   3. a live grant welded to THIS call's digest — or, from the
		//      closed legacy class, one recorded before argument binding
		//      existed, which carries no digest and is tried last.
		// Anything else is denied and files a request carrying the call.
		detail, outside := "", ""
		admitted := false
		if cs, constrained := h.d.Policy.Constraints(cred.Name, tool); constrained {
			if inside, why := config.Satisfied(cs, args); inside {
				admitted, detail = true, "within standing constraint"
			} else {
				outside = why
			}
		} else if slices.Contains(allowed, tool) {
			admitted = true
		}
		if !admitted {
			// The use is consumed before the forward, so an upstream
			// failure burns it (conservative direction, unchanged).
			grantID, ok, err := h.d.Store.ConsumeToolGrant(r.Context(), cred.Name, tool, bind.Digest)
			if err != nil {
				slog.Error("gateway: grant check failed", "credential", cred.Name, "tool", tool, "err", err)
				h.httpDeny(w, r, cred, name, method, tool, http.StatusServiceUnavailable, "grant check unavailable")
				return
			}
			if !ok {
				// Deny-and-pend, now carrying the CALL: the request
				// and its dedup key hold the digest and the summary, so two
				// attempts with different policy-relevant arguments file TWO
				// requests and one approval can never cover both. A filing
				// failure never un-denies and never trips the breaker.
				denyMsg := "tool not permitted by the Kaimahi allowlist for this call"
				if outside != "" {
					denyMsg = "tool call not permitted: outside the standing constraint (" + outside + ")"
				}
				filed := store.Filing{Credential: cred.Name, Kind: "tool", Subject: tool,
					Detail: "denied tools/call via upstream " + name, ArgDigest: bind.Digest, ArgSummary: bind.Summary}
				if h.fileRequest(r, filed) {
					denyMsg += "; approval request filed — run 'make approvals'"
				}
				h.rpcDenyCall(w, r, cred, name, method, tool, bind, id, codeToolNotPermitted, denyMsg)
				return
			}
			detail = "granted " + grantID
		}
		started := time.Now()
		out := h.forward(w, r, name, up, body)
		metrics.ObserveUpstream(metrics.SeamGateway, name, time.Since(started))
		switch {
		case strings.HasPrefix(detail, "granted "):
			metrics.Decide(metrics.SeamGateway, metrics.Granted, metrics.ReasonGrant)
		case detail != "":
			metrics.Decide(metrics.SeamGateway, metrics.Allowed, metrics.ReasonConstraint)
		case out.refused:
			metrics.Decide(metrics.SeamGateway, metrics.Allowed, metrics.ReasonEgressRefused)
		case out.status >= 200 && out.status < 300:
			metrics.Decide(metrics.SeamGateway, metrics.Allowed, metrics.ReasonAllowlist)
		case out.status == http.StatusBadGateway:
			metrics.Decide(metrics.SeamGateway, metrics.Allowed, metrics.ReasonUpstreamUnreachable)
		default:
			metrics.Decide(metrics.SeamGateway, metrics.Allowed, metrics.ReasonUpstreamError)
		}
		// The audit row carries how the call was admitted (a constraint, a
		// grant, or the allowlist), the digest and summary of the call
		// itself — so the approved call and the call that ran are provably
		// the same one — AND what became of the forward.
		if out.note != "" {
			if detail != "" {
				detail += "; "
			}
			detail += out.note
		}
		h.audit(r, store.ToolAuditEntry{CredentialName: cred.Name, Upstream: name,
			Method: method, Tool: tool, Decision: "allowed", Status: out.status, Detail: detail,
			ArgDigest: bind.Digest, ArgSummary: bind.Summary})

	default:
		h.rpcDeny(w, r, cred, name, method, "", id,
			codeMethodNotAllowed, "method not relayed by the Kaimahi gateway (tools only)")
	}
}

// rawID re-renders the message id for a JSON-RPC response. It comes
// from the canonical tree, so the id echoed back is the one enforcement
// was decided on. Absent (a notification) yields nil, which the deny
// paths answer with the spec's 202.
func rawID(msg map[string]any) json.RawMessage {
	raw, ok := msg["id"]
	if !ok || raw == nil {
		return nil
	}
	out, err := marshalCanonical(raw)
	if err != nil {
		return nil
	}
	return out
}

// canonRefusal is the client-facing reason a message was refused before
// any policy ran. Every one starts with "request body" so reasonFor
// classifies it as a bad request.
func canonRefusal(err error) string {
	if errors.Is(err, errDuplicateKey) {
		// Refused, not collapsed: see canon.go.
		return "request body carries a duplicate JSON key"
	}
	if errors.Is(err, errTooDeep) || errors.Is(err, errTooLarge) {
		return "request body is nested too deeply or too complex to canonicalize"
	}
	return "request body is not a JSON-RPC message"
}

// allowlist reads the credential's static tool allowlist, failing the
// request closed (503, audited) when it cannot be read. An empty list
// is a valid answer: nothing callable without a live grant.
func (h *handler) allowlist(w http.ResponseWriter, r *http.Request, cred store.Credential,
	upstream, method string) ([]string, bool) {
	allowed, err := h.d.Store.ToolAllowlist(r.Context(), cred.Name)
	if err != nil {
		slog.Error("gateway: allowlist read failed", "credential", cred.Name, "err", err)
		h.httpDeny(w, r, cred, upstream, method, "", http.StatusServiceUnavailable, "tool allowlist unavailable")
		return nil, false
	}
	return allowed, true
}

// projectable is the tools/list projection set: the static allowlist
// plus tools currently callable via live grants (a read-only liveness
// query — listing burns no uses). Visible = callable right now.
func (h *handler) projectable(w http.ResponseWriter, r *http.Request, cred store.Credential,
	upstream, method string, allowed []string) ([]string, bool) {
	granted, err := h.d.Store.LiveToolGrantSubjects(r.Context(), cred.Name)
	if err != nil {
		slog.Error("gateway: grant projection read failed", "credential", cred.Name, "err", err)
		h.httpDeny(w, r, cred, upstream, method, "", http.StatusServiceUnavailable, "tool allowlist unavailable")
		return nil, false
	}
	for _, g := range granted {
		if !slices.Contains(allowed, g) {
			allowed = append(allowed, g)
		}
	}
	// A tool this credential carries a standing constraint for is
	// callable right now for arguments inside those bounds, so it is
	// visible too — the same "visible means callable" rule live grants
	// follow.
	for _, c := range h.d.Policy.ConstrainedTools(cred.Name) {
		if !slices.Contains(allowed, c) {
			allowed = append(allowed, c)
		}
	}
	return allowed, true
}

// fileRequest files a pending approval request on a cancel-free context
// (a client disconnect must not drop the filing). Reports whether a
// request is now pending — freshly filed or already deduped both count;
// only a store failure (logged) reports false.
func (h *handler) fileRequest(r *http.Request, f store.Filing) bool {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	if _, err := h.d.Store.FileApprovalRequest(ctx, f); err != nil {
		slog.Error("gateway: filing approval request failed (denial stands)",
			"credential", f.Credential, "kind", f.Kind, "subject", f.Subject, "err", err)
		return false
	}
	return true
}

// outcome is what a forward reports for the audit row: the status the
// client was answered with, a note when something other than the
// upstream's own answer decided it (an egress refusal, a redirect, a cut
// body), and whether the egress policy refused the dial before any byte
// left.
type outcome struct {
	status  int
	note    string
	refused bool
}

// MsgUpstreamRefused is the 502 body for a call the hardened dialer
// refused to make (docs/hosted-upstreams.md).
const MsgUpstreamRefused = "tool upstream refused by the egress policy"

// forward relays one message upstream byte-faithfully and reports the
// outcome for the caller's audit row. SSE responses stream through with
// per-line flushes; any other body is buffered (bounded) BEFORE the
// status goes to the client, so a body the hardened client cuts — too
// large, or stalled past its lifetime — fails closed as a 502 rather
// than a 200 with a truncated payload. A stream that is cut mid-way
// ends, and the row says so.
func (h *handler) forward(w http.ResponseWriter, r *http.Request, name string,
	up config.ToolUpstream, body []byte) outcome {
	resp, err := h.do(r, up, body)
	if errors.Is(err, errCredentialUnavailable) {
		http.Error(w, "tool upstream credential unavailable", http.StatusServiceUnavailable)
		return outcome{status: http.StatusServiceUnavailable}
	}
	if egress.IsRefusal(err) {
		slog.Error("gateway: tool upstream refused by the egress policy", "upstream", name, "err", err)
		http.Error(w, MsgUpstreamRefused, http.StatusBadGateway)
		return outcome{status: http.StatusBadGateway, note: "egress refused: " + err.Error(), refused: true}
	}
	if err != nil {
		slog.Error("gateway: tool upstream call failed", "upstream", name, "err", err)
		http.Error(w, "tool upstream unreachable", http.StatusBadGateway)
		return outcome{status: http.StatusBadGateway, note: "upstream unreachable: " + err.Error()}
	}
	defer func() { _ = resp.Body.Close() }()
	// A redirect is refused, not relayed: the client never followed it
	// (see Deps.clientFor), and a Location header must not leak an escape
	// hatch from the upstream table the proxy booted with.
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		slog.Error("gateway: tool upstream answered a redirect; refusing", "upstream", name, "status", resp.StatusCode)
		http.Error(w, MsgUpstreamRedirected, http.StatusBadGateway)
		return outcome{status: http.StatusBadGateway, note: MsgUpstreamRedirected}
	}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		copyResponseHeaders(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)
		if err := relayStream(w, resp.Body); err != nil {
			slog.Error("gateway: relaying stream", "upstream", name, "err", err)
			return outcome{status: resp.StatusCode, note: "upstream stream cut: " + err.Error()}
		}
		return outcome{status: resp.StatusCode}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBufferedResp+1))
	if err == nil && int64(len(raw)) > maxBufferedResp {
		err = egress.ErrResponseTooLarge
	}
	if err != nil {
		slog.Error("gateway: tool upstream response cut; failing closed", "upstream", name, "err", err)
		msg := "tool upstream response cut"
		if egress.IsBodyCut(err) {
			msg = "tool upstream response cut by the egress policy"
		}
		http.Error(w, msg, http.StatusBadGateway)
		return outcome{status: http.StatusBadGateway, note: "upstream body cut: " + err.Error()}
	}
	copyResponseHeaders(w.Header(), resp.Header)
	w.Header().Del("Content-Length")
	w.WriteHeader(resp.StatusCode)
	if _, err := w.Write(raw); err != nil {
		slog.Error("gateway: relaying tool response", "upstream", name, "err", err)
	}
	return outcome{status: resp.StatusCode}
}

// forwardProjected relays a tools/list and rewrites the listing to the
// credential's allowlist before it reaches the client — the projection
// kagent's controller discovery stores as discoveredTools, so an agent
// never sees a tool its credential cannot call. The upstream may answer
// as plain JSON or as an SSE-framed response; either way the projected
// answer goes back as application/json (always in the client's Accept
// set). A 2xx answer the gateway cannot parse is failed closed: an
// unprojectable listing must not reach the agent.
func (h *handler) forwardProjected(w http.ResponseWriter, r *http.Request, cred store.Credential,
	name string, up config.ToolUpstream, body []byte, allowed []string) {
	resp, err := h.do(r, up, body)
	if errors.Is(err, errCredentialUnavailable) {
		http.Error(w, "tool upstream credential unavailable", http.StatusServiceUnavailable)
		return
	}
	if egress.IsRefusal(err) {
		slog.Error("gateway: tool upstream refused by the egress policy", "upstream", name, "err", err)
		http.Error(w, MsgUpstreamRefused, http.StatusBadGateway)
		return
	}
	if err != nil {
		slog.Error("gateway: tool upstream call failed", "upstream", name, "err", err)
		http.Error(w, "tool upstream unreachable", http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBufferedResp+1))
	if err == nil && int64(len(raw)) > maxBufferedResp {
		err = egress.ErrResponseTooLarge
	}
	if err != nil {
		slog.Error("gateway: reading tools/list response", "upstream", name, "err", err)
		http.Error(w, "tool upstream response unreadable", http.StatusBadGateway)
		return
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		slog.Error("gateway: tool upstream answered a redirect; refusing", "upstream", name, "status", resp.StatusCode)
		http.Error(w, MsgUpstreamRedirected, http.StatusBadGateway)
		return
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// Nothing to project on an upstream error; relay it verbatim.
		copyResponseHeaders(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(raw)
		return
	}

	payload := raw
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		payload = lastSSEData(raw)
	}
	var rpc struct {
		JSONRPC string                     `json:"jsonrpc"`
		ID      json.RawMessage            `json:"id"`
		Error   json.RawMessage            `json:"error,omitempty"`
		Result  map[string]json.RawMessage `json:"result,omitempty"`
	}
	if err := json.Unmarshal(payload, &rpc); err != nil || rpc.JSONRPC == "" {
		slog.Error("gateway: unparseable tools/list response; failing closed", "upstream", name, "err", err)
		http.Error(w, "unprojectable tool-server response", http.StatusBadGateway)
		return
	}
	if rpc.Result != nil {
		var tools []json.RawMessage
		if raw, ok := rpc.Result["tools"]; ok {
			if err := json.Unmarshal(raw, &tools); err != nil {
				slog.Error("gateway: unparseable tools listing; failing closed", "upstream", name, "err", err)
				http.Error(w, "unprojectable tool-server response", http.StatusBadGateway)
				return
			}
		}
		kept := make([]json.RawMessage, 0, len(tools))
		for _, t := range tools {
			var tool struct {
				Name string `json:"name"`
			}
			if json.Unmarshal(t, &tool) == nil && slices.Contains(allowed, tool.Name) {
				kept = append(kept, t)
			}
		}
		keptRaw, err := json.Marshal(kept)
		if err != nil {
			http.Error(w, "projection failed", http.StatusInternalServerError)
			return
		}
		rpc.Result["tools"] = keptRaw
		slog.Info("gateway: projected tools/list", "credential", cred.Name,
			"upstream", name, "offered", len(tools), "projected", len(kept))
	}

	// The session header (Mcp-Session-Id) must survive the rewrite; the
	// upstream's framing headers must not.
	copyResponseHeaders(w.Header(), resp.Header)
	w.Header().Del("Content-Length")
	out := map[string]any{"jsonrpc": rpc.JSONRPC, "id": rpc.ID}
	if rpc.Error != nil {
		out["error"] = rpc.Error
	} else {
		out["result"] = rpc.Result
	}
	writeRPC(w, out)
}

// terminate relays a session DELETE (terminateOnClose) so upstream
// sessions are cleaned up. Not a tool action: authenticated and confined
// to the upstream table, but not audited.
func (h *handler) terminate(w http.ResponseWriter, r *http.Request) {
	cred, r, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	up, ok := h.d.Upstreams[name]
	if !ok {
		h.httpDeny(w, r, cred, name, "", "", http.StatusForbidden, "unknown tool upstream")
		return
	}
	outReq, err := http.NewRequestWithContext(r.Context(), http.MethodDelete, up.URL, nil)
	if err != nil {
		http.Error(w, "upstream request build failed", http.StatusBadGateway)
		return
	}
	copyRequestHeaders(outReq.Header, r.Header)
	if err := injectCredential(outReq, up); err != nil {
		http.Error(w, "tool upstream credential unavailable", http.StatusServiceUnavailable)
		return
	}
	client, err := h.d.clientFor(up)
	if err != nil {
		http.Error(w, MsgUpstreamRefused, http.StatusBadGateway)
		return
	}
	resp, err := client.Do(outReq)
	if egress.IsRefusal(err) {
		http.Error(w, MsgUpstreamRefused, http.StatusBadGateway)
		return
	}
	if err != nil {
		http.Error(w, "tool upstream unreachable", http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		slog.Error("gateway: tool upstream answered a redirect; refusing", "upstream", name, "status", resp.StatusCode)
		http.Error(w, MsgUpstreamRedirected, http.StatusBadGateway)
		return
	}
	copyResponseHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (h *handler) do(r *http.Request, up config.ToolUpstream, body []byte) (*http.Response, error) {
	outReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, up.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	copyRequestHeaders(outReq.Header, r.Header)
	// Committed, non-secret headers first — they narrow what a hosted
	// server offers (X-MCP-Toolsets and friends) and must not be able to
	// displace the credential, which goes on last. Load already refuses
	// an extra header naming the credential slot; the ordering here means
	// a future one could not win even if it slipped through.
	for k, v := range up.ExtraHeaders {
		outReq.Header.Set(k, v)
	}
	if err := injectCredential(outReq, up); err != nil {
		return nil, err
	}
	outReq.ContentLength = int64(len(body))
	client, err := h.d.clientFor(up)
	if err != nil {
		return nil, err
	}
	return client.Do(outReq)
}

// MsgUpstreamRedirected is the 502 body for a redirect the gateway
// refused to follow — a 502 issued before any byte of the message reached
// the upstream, which the notifier may therefore retry. (The hosted
// dialer's MsgUpstreamRefused is the other such 502; the notifier's upstream is
// in-cluster, so it never sees that one.)
const MsgUpstreamRedirected = "tool upstream redirected (refused)"

// errCredentialUnavailable marks a tool upstream whose own credential
// could not be read. It is NOT an unreachable upstream: the request is
// failed closed as 503 (the LLM proxy's contract for the same case), so
// a missing or unreadable Secret can never downgrade to an unauthenticated
// call that a permissive tool server might still honour.
var errCredentialUnavailable = errors.New("upstream credential unavailable")

// injectCredential puts the tool server's OWN credential in its native
// header slot, read per request from proxy-side custody. The client's
// kmh_ token was already stripped by copyRequestHeaders, so this is the
// only credential the upstream ever sees, and it exists only in the
// proxy pod.
func injectCredential(outReq *http.Request, up config.ToolUpstream) error {
	if up.CredentialFile == "" {
		return nil
	}
	raw, err := os.ReadFile(up.CredentialFile)
	if err != nil {
		slog.Error("gateway: tool upstream credential unavailable", "url", up.URL, "err", err)
		return errCredentialUnavailable
	}
	secret := strings.TrimSpace(string(raw))
	if secret == "" {
		slog.Error("gateway: tool upstream credential is empty", "url", up.URL)
		return errCredentialUnavailable
	}
	if up.CredentialHeader == "" || strings.EqualFold(up.CredentialHeader, "authorization") {
		outReq.Header.Set("Authorization", "Bearer "+secret)
		return nil
	}
	outReq.Header.Set(up.CredentialHeader, secret)
	return nil
}

// relayStream forwards SSE lines as they arrive, flushing each so a
// long-running tool call streams through. It reports the read error that
// ended the stream early (a cut body), nil on a clean end.
func relayStream(w http.ResponseWriter, body io.Reader) error {
	flusher, _ := w.(http.Flusher)
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for sc.Scan() {
		_, _ = io.WriteString(w, sc.Text()+"\n")
		if flusher != nil {
			flusher.Flush()
		}
	}
	return sc.Err()
}

// lastSSEData extracts the final event's data payload from an SSE-framed
// body — for a buffered tools/list response, that is the JSON-RPC
// response message. Per the SSE format the space after "data:" is
// optional, and an event's data may span multiple data lines (joined
// with newlines); a blank line ends an event.
func lastSSEData(raw []byte) []byte {
	var last, current []byte
	flush := func() {
		if len(current) > 0 {
			last, current = current, nil
		}
	}
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 0, 64*1024), maxBufferedResp)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			flush()
			continue
		}
		payload, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		payload = strings.TrimPrefix(payload, " ")
		if current != nil {
			current = append(current, '\n')
		}
		current = append(current, payload...)
	}
	flush()
	return last
}

// hopByHop are headers that must not be forwarded in either direction.
var hopByHop = map[string]bool{
	"Connection": true, "Keep-Alive": true, "Proxy-Authenticate": true,
	"Proxy-Authorization": true, "Te": true, "Trailer": true,
	"Transfer-Encoding": true, "Upgrade": true,
}

// copyRequestHeaders forwards client headers minus hop-by-hop, every
// credential slot (the kmh_ token must never reach the tool server — a
// keyed upstream gets its own credential injected from proxy-side
// custody by injectCredential, never passed through), and
// Accept-Encoding (the gateway must read plaintext bodies to project
// listings).
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
