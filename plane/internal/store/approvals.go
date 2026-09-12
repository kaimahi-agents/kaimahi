package store

// Approvals: pending requests, bounded grants, and the approvals'
// own audit trail. Everything decision-relevant is evaluated in SQL at
// call time — liveness (expiry, use count) is part of every consuming
// or listing query, so an expired grant is simply not a grant and no
// reaper is needed for correctness.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrNotPending marks a decision attempted on a request that is no
// longer pending (already approved or denied).
var ErrNotPending = errors.New("store: request is not pending")

// ErrBounds marks an approval whose bounds are invalid for the request:
// no bound at all (an unbounded grant is a config change, not an
// approval), an amount mismatched to the kind, or a retired kind.
var ErrBounds = errors.New("store: invalid grant bounds")

type ApprovalRequest struct {
	ID             string `json:"id"`
	CredentialName string `json:"credential"`
	Kind           string `json:"kind"`
	Subject        string `json:"subject"`
	Status         string `json:"status"`
	Detail         string `json:"detail"`
	// ArgDigest/ArgSummary carry the exact CALL a tool request is
	// about: the digest a grant is welded to, and the transaction line an
	// approver reads. Empty on budget and inbound requests, which have no
	// arguments, and on tool requests filed before argument binding.
	ArgDigest  string     `json:"arg_digest,omitempty"`
	ArgSummary string     `json:"arg_summary,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	DecidedAt  *time.Time `json:"decided_at,omitempty"`
	// DecidedBy names who decided: DecidedByAdmin for the admin
	// bearer, "slack:<user id>" on historical Slack decisions; empty while pending.
	DecidedBy string `json:"decided_by"`
}

// Grant is one bounded permit. At least one of ExpiresAt/MaxUses is
// always set (schema CHECK); Amount is set exactly on budget grants.
type Grant struct {
	ID             string     `json:"id"`
	RequestID      string     `json:"request_id"`
	CredentialName string     `json:"credential"`
	Kind           string     `json:"kind"`
	Subject        string     `json:"subject"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	MaxUses        *int32     `json:"max_uses,omitempty"`
	Uses           int32      `json:"uses"`
	Amount         *int64     `json:"amount,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	DecidedBy      string     `json:"decided_by"`
	// ArgDigest is the call this tool grant admits — and only that
	// call. NULL means a verb-level grant, a closed class: only grants
	// that predate migration 00008 carry it (ApproveRequest refuses to
	// mint another), and the gateway honours those unchanged.
	ArgDigest *string `json:"arg_digest,omitempty"`
}

type ApprovalAuditEntry struct {
	RequestID      string    `json:"request_id"`
	CredentialName string    `json:"credential"`
	Kind           string    `json:"kind"`
	Subject        string    `json:"subject"`
	Action         string    `json:"action"`
	Bounds         string    `json:"bounds"`
	ArgDigest      string    `json:"arg_digest,omitempty"`
	ArgSummary     string    `json:"arg_summary,omitempty"`
	DecidedBy      string    `json:"decided_by"`
	CreatedAt      time.Time `json:"created_at"`
}

// DecidedByAdmin is the identity the admin path records: the admin
// bearer is the only writer that port admits, and it is not a person.
const DecidedByAdmin = "admin"

// grantLive is the one liveness predicate every consuming and listing
// query shares: not expired, not exhausted — evaluated by Postgres at
// call time, never from a cached copy.
const grantLive = `(expires_at IS NULL OR expires_at > now())
	AND (max_uses IS NULL OR uses < max_uses)`

// Filing is one approval request to file. ArgDigest/ArgSummary are set
// on tool requests and empty everywhere else.
type Filing struct {
	Credential string
	Kind       string
	Subject    string
	Detail     string
	ArgDigest  string
	ArgSummary string
}

// FileApprovalRequest files a pending request, deduplicated per
// (credential, kind, subject, arg_digest) among pending rows: refiling
// while an identical one is pending is a no-op (filed=false), but two
// attempts at the SAME tool with DIFFERENT policy-relevant arguments are
// two different requests (before argument binding they collapsed into
// one, and one approval covered both). A fresh filing also writes
// the 'requested' audit row in the same transaction.
func (s *Store) FileApprovalRequest(ctx context.Context, f Filing) (filed bool, err error) {
	_, filed, err = s.FileRequest(ctx, f)
	return filed, err
}

// FileRequest is FileApprovalRequest returning the fresh request's id as
// well. id is empty when deduped.
func (s *Store) FileRequest(ctx context.Context, f Filing) (id string, filed bool, err error) {
	if f.Kind != "tool" && f.Kind != "budget" {
		return "", false, fmt.Errorf("%w: approval kind %q is not supported", ErrBounds, f.Kind)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	err = tx.QueryRow(ctx,
		`INSERT INTO approval_request (credential_name, kind, subject, detail, arg_digest, arg_summary)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (credential_name, kind, subject, arg_digest) WHERE status = 'pending' DO NOTHING
		 RETURNING id`,
		f.Credential, f.Kind, f.Subject, auditText(f.Detail), f.ArgDigest, auditText(f.ArgSummary)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil // an identical request is already pending — deduped
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" { // foreign_key_violation
		return "", false, fmt.Errorf("%w: no such credential", ErrNotFound)
	}
	if err != nil {
		return "", false, err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO approval_audit (request_id, credential_name, kind, subject, action, arg_digest, arg_summary)
		 VALUES ($1, $2, $3, $4, 'requested', $5, $6)`,
		id, f.Credential, f.Kind, f.Subject, f.ArgDigest, auditText(f.ArgSummary)); err != nil {
		return "", false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", false, err
	}
	return id, true, nil
}

// PendingApprovals lists pending requests, oldest first (the queue).
func (s *Store) PendingApprovals(ctx context.Context) ([]ApprovalRequest, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, credential_name, kind, subject, status, detail, arg_digest, arg_summary, created_at, decided_at, decided_by
		 FROM approval_request WHERE status = 'pending' ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	return scanRequests(rows)
}

func scanRequests(rows pgx.Rows) ([]ApprovalRequest, error) {
	defer rows.Close()
	var out []ApprovalRequest
	for rows.Next() {
		var r ApprovalRequest
		if err := rows.Scan(&r.ID, &r.CredentialName, &r.Kind, &r.Subject,
			&r.Status, &r.Detail, &r.ArgDigest, &r.ArgSummary, &r.CreatedAt, &r.DecidedAt, &r.DecidedBy); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ApproveRequest decides a pending request and mints its bounded grant
// atomically: grant insert, request status flip, and the 'approved'
// audit row commit together or not at all — a failed audit write fails
// the approval (fail closed, stronger than the breaker contract).
// Bound validation (at least one of expiresAt/maxUses; amount exactly
// on budget kinds) is the caller's job and the schema's backstop.
// decidedBy names the approver (DecidedByAdmin for current decisions)
// and is written onto the request, the grant and the audit row alike.
func (s *Store) ApproveRequest(ctx context.Context, id string,
	expiresAt *time.Time, maxUses *int32, amount *int64, decidedBy string) (Grant, error) {
	if decidedBy == "" {
		return Grant{}, fmt.Errorf("%w: a decision must name who decided", ErrBounds)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Grant{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var r ApprovalRequest
	err = tx.QueryRow(ctx,
		`SELECT id, credential_name, kind, subject, status, arg_digest, arg_summary
		 FROM approval_request WHERE id = $1 FOR UPDATE`,
		id).Scan(&r.ID, &r.CredentialName, &r.Kind, &r.Subject, &r.Status, &r.ArgDigest, &r.ArgSummary)
	if errors.Is(err, pgx.ErrNoRows) {
		return Grant{}, ErrNotFound
	}
	if err != nil {
		return Grant{}, err
	}
	if r.Status != "pending" {
		return Grant{}, ErrNotPending
	}
	// Historical inbound requests remain readable and deniable, but no
	// dispatcher remains to execute them. Never mint another such grant.
	if r.Kind != "tool" && r.Kind != "budget" {
		return Grant{}, fmt.Errorf("%w: approval kind %q is not supported", ErrBounds, r.Kind)
	}
	// Ported permit discipline: a grant allowing everything forever is
	// an error, not a wide grant — at least one bound REQUIRED; budget
	// grants carry exactly one amount, tool grants none.
	if expiresAt == nil && maxUses == nil {
		return Grant{}, fmt.Errorf("%w: at least one of TTL and USES is required", ErrBounds)
	}
	if (r.Kind == "budget") != (amount != nil) {
		return Grant{}, fmt.Errorf("%w: AMOUNT is required for budget grants and forbidden otherwise", ErrBounds)
	}
	// A tool grant is welded to the CALL its request carries. A tool
	// request with no digest predates argument binding (or was filed by a
	// path that named no call), and minting a verb-level grant from it
	// would re-open exactly the hole argument binding closes — so it is refused,
	// with the two honest ways forward named.
	var argDigest *string
	if r.Kind == "tool" {
		if r.ArgDigest == "" {
			return Grant{}, fmt.Errorf("%w: this tool request carries no call to bind (filed before argument binding); let the agent retry so a bound request is filed, or widen the allowlist with 'make tool-allow'", ErrBounds)
		}
		d := r.ArgDigest
		argDigest = &d
	}

	g := Grant{RequestID: r.ID, CredentialName: r.CredentialName, Kind: r.Kind, Subject: r.Subject,
		ExpiresAt: expiresAt, MaxUses: maxUses, Amount: amount, DecidedBy: decidedBy, ArgDigest: argDigest}
	if err := tx.QueryRow(ctx,
		`INSERT INTO permit_grant (request_id, credential_name, kind, subject, expires_at, max_uses, amount, decided_by, arg_digest)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id, created_at`,
		r.ID, r.CredentialName, r.Kind, r.Subject, expiresAt, maxUses, amount, decidedBy, argDigest).Scan(&g.ID, &g.CreatedAt); err != nil {
		return Grant{}, err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE approval_request SET status = 'approved', decided_at = now(), decided_by = $2 WHERE id = $1`,
		r.ID, decidedBy); err != nil {
		return Grant{}, err
	}
	bounds := ""
	if expiresAt != nil {
		bounds += fmt.Sprintf("expires=%s ", expiresAt.UTC().Format(time.RFC3339))
	}
	if maxUses != nil {
		bounds += fmt.Sprintf("uses=%d ", *maxUses)
	}
	if amount != nil {
		bounds += fmt.Sprintf("amount=%d ", *amount)
	}
	bounds = strings.TrimSpace(bounds)
	if _, err := tx.Exec(ctx,
		`INSERT INTO approval_audit (request_id, credential_name, kind, subject, action, bounds, decided_by, arg_digest, arg_summary)
		 VALUES ($1, $2, $3, $4, 'approved', $5, $6, $7, $8)`,
		r.ID, r.CredentialName, r.Kind, r.Subject, bounds, decidedBy, r.ArgDigest, auditText(r.ArgSummary)); err != nil {
		return Grant{}, err
	}
	return g, tx.Commit(ctx)
}

// DenyApprovalRequest decides a pending request negatively; the request
// stays on record (denied), and a fresh denial can file a new one.
// decidedBy as for ApproveRequest.
func (s *Store) DenyApprovalRequest(ctx context.Context, id string, decidedBy string) error {
	if decidedBy == "" {
		return fmt.Errorf("%w: a decision must name who decided", ErrBounds)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var r ApprovalRequest
	err = tx.QueryRow(ctx,
		`SELECT id, credential_name, kind, subject, status, arg_digest, arg_summary
		 FROM approval_request WHERE id = $1 FOR UPDATE`,
		id).Scan(&r.ID, &r.CredentialName, &r.Kind, &r.Subject, &r.Status, &r.ArgDigest, &r.ArgSummary)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if r.Status != "pending" {
		return ErrNotPending
	}
	if _, err := tx.Exec(ctx,
		`UPDATE approval_request SET status = 'denied', decided_at = now(), decided_by = $2 WHERE id = $1`,
		r.ID, decidedBy); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO approval_audit (request_id, credential_name, kind, subject, action, decided_by, arg_digest, arg_summary)
		 VALUES ($1, $2, $3, $4, 'denied', $5, $6, $7)`,
		r.ID, r.CredentialName, r.Kind, r.Subject, decidedBy, r.ArgDigest, auditText(r.ArgSummary)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ConsumeToolGrant admits one tool call under a live grant, consuming
// one use atomically: the consume runs under the credential's row
// lock (lockCredential), so concurrent consumers — on one replica or
// across replicas — take turns and each sees the previous one's commit:
// a grant with N uses left admits exactly N concurrent calls, never
// N+1 and (unlike the FOR UPDATE SKIP LOCKED it replaces) never
// fewer. ok=false means no consumable grant — the caller denies.
func (s *Store) ConsumeToolGrant(ctx context.Context, credential, tool, argDigest string) (grantID string, ok bool, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := lockCredential(ctx, tx, credential); err != nil {
		return "", false, err
	}
	grantID, ok, err = consumeToolGrantLocked(ctx, tx, credential, tool, argDigest)
	if err != nil {
		return "", false, err
	}
	return grantID, ok, tx.Commit(ctx)
}

// rowQuerier is the one method the shared queries need, satisfied by
// both the pool and a transaction.
type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// consumeGrantLocked is the shared use-consuming step for every grant
// kind: one use from the oldest live grant matching (credential, kind,
// subject), or ok=false when none is consumable. The caller holds the
// credential lock (lockCredential in the same tx), which is what makes
// the plain read-then-update exact: no other consumer for this
// credential can be between the SELECT and the UPDATE.
func consumeGrantLocked(ctx context.Context, tx pgx.Tx, credential, kind, subject string) (grantID string, ok bool, err error) {
	err = tx.QueryRow(ctx,
		`UPDATE permit_grant SET uses = uses + 1
		 WHERE id = (
		   SELECT id FROM permit_grant
		   WHERE credential_name = $1 AND kind = $2 AND subject = $3 AND `+grantLive+`
		   ORDER BY created_at LIMIT 1
		 ) RETURNING id`,
		credential, kind, subject).Scan(&grantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return grantID, true, nil
}

// consumeToolGrantLocked is consumeGrantLocked for tool grants, which
// admit ONE CALL: the grant's digest must equal the digest of
// the call being made. A mismatch consumes nothing, so the caller denies
// and files a request for the call actually attempted — an approval can
// never be spent on a different transaction. The one exception is the
// closed legacy class (arg_digest IS NULL, migration 00008): those
// verb-level grants are honoured, and preferred LAST, so an exact match
// is always burned first.
func consumeToolGrantLocked(ctx context.Context, tx pgx.Tx, credential, tool, argDigest string) (grantID string, ok bool, err error) {
	err = tx.QueryRow(ctx,
		`UPDATE permit_grant SET uses = uses + 1
		 WHERE id = (
		   SELECT id FROM permit_grant
		   WHERE credential_name = $1 AND kind = 'tool' AND subject = $2
		     AND (arg_digest = $3 OR arg_digest IS NULL) AND `+grantLive+`
		   ORDER BY (arg_digest IS NULL), created_at LIMIT 1
		 ) RETURNING id`,
		credential, tool, argDigest).Scan(&grantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return grantID, true, nil
}

// LiveToolGrantSubjects lists the tools a credential can currently call
// via grants — for the tools/list projection (read-only: listing burns
// no uses).
func (s *Store) LiveToolGrantSubjects(ctx context.Context, credential string) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT DISTINCT subject FROM permit_grant
		 WHERE credential_name = $1 AND kind = 'tool' AND `+grantLive+`
		 ORDER BY subject`, credential)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// BudgetNeed is one exceeded cap an admission must cover via grants
// (AdmitSpend, spend.go).
type BudgetNeed struct {
	Subject string // "tokens" or "cents"
	Used    int64
	Cap     int64
}

// Grants lists a credential's grants (all credentials when empty),
// newest first, with liveness computed by the same predicate consumers
// use. Historical inbound grants remain visible but are never live:
// their dispatcher has been retired, regardless of expiry or uses.
func (s *Store) Grants(ctx context.Context, credential string, limit int) ([]Grant, []bool, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id, request_id, credential_name, kind, subject, expires_at, max_uses, uses, amount, created_at, decided_by, arg_digest,
		        (kind IN ('tool', 'budget') AND `+grantLive+`) AS live
		 FROM permit_grant
		 WHERE ($1 = '' OR credential_name = $1)
		 ORDER BY created_at DESC LIMIT $2`,
		credential, limit)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var out []Grant
	var live []bool
	for rows.Next() {
		var g Grant
		var l bool
		if err := rows.Scan(&g.ID, &g.RequestID, &g.CredentialName, &g.Kind, &g.Subject,
			&g.ExpiresAt, &g.MaxUses, &g.Uses, &g.Amount, &g.CreatedAt, &g.DecidedBy, &g.ArgDigest, &l); err != nil {
			return nil, nil, err
		}
		out = append(out, g)
		live = append(live, l)
	}
	return out, live, rows.Err()
}

// ApprovalAudit returns the newest approval-trail rows for one
// credential (all when empty), newest first.
func (s *Store) ApprovalAudit(ctx context.Context, credential string, limit int) ([]ApprovalAuditEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx,
		`SELECT request_id, credential_name, kind, subject, action, bounds, decided_by, arg_digest, arg_summary, created_at
		 FROM approval_audit
		 WHERE ($1 = '' OR credential_name = $1)
		 ORDER BY created_at DESC LIMIT $2`,
		credential, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ApprovalAuditEntry
	for rows.Next() {
		var e ApprovalAuditEntry
		if err := rows.Scan(&e.RequestID, &e.CredentialName, &e.Kind, &e.Subject,
			&e.Action, &e.Bounds, &e.DecidedBy, &e.ArgDigest, &e.ArgSummary, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
