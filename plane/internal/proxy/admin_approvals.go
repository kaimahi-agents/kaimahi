package proxy

// The admin surface for approvals: the human decision point. Same
// posture as the rest of the admin plane — bearer-token auth on a port
// no Service exposes, strict input validation (the ported permit
// discipline: unknown fields rejected, unbounded grants refused).

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/kaimahi-agents/kaimahi/plane/internal/gateway"
	"github.com/kaimahi-agents/kaimahi/plane/internal/store"
)

var (
	uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	// Grant subjects: a tool name, or a budget cap name.
	subjectRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
)

const (
	maxTTLSeconds = 30 * 24 * 60 * 60 // a month-long grant is a config change in disguise
	maxUsesLimit  = 1_000_000
	maxAmount     = 1_000_000_000_000 // tokens or cents; far beyond any sane raise
)

// decodeStrict decodes one JSON object, rejecting unknown fields and
// trailing data (ported fail-closed parsing posture).
func decodeStrict(w http.ResponseWriter, r *http.Request, into any) bool {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 65536))
	if err != nil {
		http.Error(w, "body unreadable or too large", http.StatusBadRequest)
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
		return false
	}
	if dec.Decode(new(struct{})) != io.EOF {
		http.Error(w, "trailing data after document", http.StatusBadRequest)
		return false
	}
	return true
}

// fileRequest handles POST /admin/requests: explicit filing (`make
// request`), same dedupe as auto-filing.
func (h *handler) fileRequest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Credential string `json:"credential"`
		Kind       string `json:"kind"`
		Subject    string `json:"subject"`
		// Arguments, tool requests only: the CALL the operator wants
		// pre-approved, as a JSON object. Omitted means the argument-less
		// call — never "any call": since argument binding, an approval is
		// welded to one call's digest, so a request has to name one.
		Arguments json.RawMessage `json:"arguments,omitempty"`
	}
	if !decodeStrict(w, r, &req) {
		return
	}
	if !credentialName.MatchString(req.Credential) ||
		(req.Kind != "tool" && req.Kind != "budget") ||
		!subjectRe.MatchString(req.Subject) ||
		(req.Kind == "budget" && req.Subject != "tokens" && req.Subject != "cents") {
		http.Error(w, "body must be {\"credential\": ..., \"kind\": \"tool\"|\"budget\", \"subject\": ...} (budget subjects: tokens|cents)", http.StatusBadRequest)
		return
	}
	if req.Kind != "tool" && len(req.Arguments) > 0 {
		http.Error(w, "arguments are meaningful only on tool requests", http.StatusBadRequest)
		return
	}
	filing := store.Filing{Credential: req.Credential, Kind: req.Kind, Subject: req.Subject,
		Detail: "filed explicitly via admin"}
	if req.Kind == "tool" {
		// The same binding the gateway computes for the same call, from
		// the same declarations — one implementation, so an operator's
		// pre-approval and the agent's retry cannot disagree.
		fields, declared := h.d.Config.Policy().Declared(req.Subject)
		bind, err := gateway.BindArguments(req.Subject, req.Arguments, fields, declared)
		if err != nil {
			http.Error(w, "invalid tool arguments: "+err.Error(), http.StatusBadRequest)
			return
		}
		filing.ArgDigest, filing.ArgSummary = bind.Digest, bind.Summary
	}
	filed, err := h.d.Store.FileApprovalRequest(r.Context(), filing)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "no such credential", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("admin: file request", "credential", req.Credential, "err", err)
		http.Error(w, "store error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"filed": filed, "deduped": !filed})
}

func (h *handler) listApprovals(w http.ResponseWriter, r *http.Request) {
	pending, err := h.d.Store.PendingApprovals(r.Context())
	if err != nil {
		slog.Error("admin: list approvals", "err", err)
		http.Error(w, "store error", http.StatusInternalServerError)
		return
	}
	if pending == nil {
		pending = []store.ApprovalRequest{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"pending": pending})
}

// approve handles POST /admin/approvals/{id}/approve. The grant it
// mints is BOUNDED by construction: at-least-one-bound and value ranges
// are validated here; the amount↔kind pairing (which needs the
// request's kind) is enforced by the store and the schema CHECKs.
func (h *handler) approve(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !uuidRe.MatchString(id) {
		http.Error(w, "request id must be a UUID (see /admin/approvals)", http.StatusBadRequest)
		return
	}
	var req struct {
		TTLSeconds *int64 `json:"ttl_seconds"`
		MaxUses    *int32 `json:"max_uses"`
		Amount     *int64 `json:"amount"`
	}
	if !decodeStrict(w, r, &req) {
		return
	}
	if req.TTLSeconds == nil && req.MaxUses == nil {
		http.Error(w, "an unbounded grant is a config change, not an approval — set ttl_seconds and/or max_uses", http.StatusBadRequest)
		return
	}
	if (req.TTLSeconds != nil && (*req.TTLSeconds < 1 || *req.TTLSeconds > maxTTLSeconds)) ||
		(req.MaxUses != nil && (*req.MaxUses < 1 || *req.MaxUses > maxUsesLimit)) ||
		(req.Amount != nil && (*req.Amount < 1 || *req.Amount > maxAmount)) {
		http.Error(w, "bounds out of range", http.StatusBadRequest)
		return
	}
	var expiresAt *time.Time
	if req.TTLSeconds != nil {
		t := time.Now().Add(time.Duration(*req.TTLSeconds) * time.Second)
		expiresAt = &t
	}
	// The admin bearer is the identity this port admits.
	g, err := h.d.Store.ApproveRequest(r.Context(), id, expiresAt, req.MaxUses, req.Amount, store.DecidedByAdmin)
	switch {
	case errors.Is(err, store.ErrNotFound):
		http.Error(w, "no such request", http.StatusNotFound)
		return
	case errors.Is(err, store.ErrNotPending):
		http.Error(w, "request already decided", http.StatusConflict)
		return
	case errors.Is(err, store.ErrBounds):
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	case err != nil:
		slog.Error("admin: approve", "request", id, "err", err)
		http.Error(w, "store error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(g)
}

func (h *handler) denyRequest(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !uuidRe.MatchString(id) {
		http.Error(w, "request id must be a UUID (see /admin/approvals)", http.StatusBadRequest)
		return
	}
	err := h.d.Store.DenyApprovalRequest(r.Context(), id, store.DecidedByAdmin)
	switch {
	case errors.Is(err, store.ErrNotFound):
		http.Error(w, "no such request", http.StatusNotFound)
	case errors.Is(err, store.ErrNotPending):
		http.Error(w, "request already decided", http.StatusConflict)
	case err != nil:
		slog.Error("admin: deny", "request", id, "err", err)
		http.Error(w, "store error", http.StatusInternalServerError)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (h *handler) listGrants(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("credential")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	grants, live, err := h.d.Store.Grants(r.Context(), name, limit)
	if err != nil {
		slog.Error("admin: list grants", "err", err)
		http.Error(w, "store error", http.StatusInternalServerError)
		return
	}
	type row struct {
		store.Grant
		Live bool `json:"live"`
		// CredentialExpiresAt: a grant that outlives the credential it
		// was given on is a promise the plane cannot keep, so an
		// operator reads the two deadlines side by side. Absent for a
		// legacy credential, which has none.
		CredentialExpiresAt *time.Time `json:"credential_expires_at,omitempty"`
	}
	// One read of the credential table serves every row; grant lists are
	// demo-scale, and this keeps the store's Grants query untouched.
	deadlines := map[string]*time.Time{}
	if creds, cerr := h.d.Store.ListCredentials(r.Context()); cerr != nil {
		slog.Error("admin: credential deadlines for grants", "err", cerr)
	} else {
		for _, c := range creds {
			deadlines[c.Name] = c.ExpiresAt
		}
	}
	out := make([]row, 0, len(grants))
	for i, g := range grants {
		out = append(out, row{Grant: g, Live: live[i], CredentialExpiresAt: deadlines[g.CredentialName]})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"grants": out})
}

func (h *handler) approvalAudit(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("credential")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	entries, err := h.d.Store.ApprovalAudit(r.Context(), name, limit)
	if err != nil {
		slog.Error("admin: approval audit", "err", err)
		http.Error(w, "store error", http.StatusInternalServerError)
		return
	}
	if entries == nil {
		entries = []store.ApprovalAuditEntry{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"entries": entries})
}
