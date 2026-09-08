package store

// Inbound: the inbound audit trail and the one transaction that
// admits an event. Admission is where the ingress guarantees meet the
// approvals machinery: the admitted row (which IS the replay guard, via
// its partial unique index) and the grant use are committed together or
// not at all.

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	// ErrReplay marks a delivery the plane has already admitted for this
	// hook. The caller answers 409; no grant use is consumed.
	ErrReplay = errors.New("store: delivery already admitted (replay)")
	// ErrNoGrant marks an event with no live 'inbound' grant to consume.
	// The caller denies and files an approval request.
	ErrNoGrant = errors.New("store: no live inbound grant")
)

// InboundAuditEntry is one append-only inbound row (see migration 00004
// for the decision vocabulary).
type InboundAuditEntry struct {
	ID             string `json:"id,omitempty"`
	Hook           string `json:"hook"`
	CredentialName string `json:"credential"`
	DeliveryID     string `json:"delivery_id"`
	Decision       string `json:"decision"`
	Status         int    `json:"status"`
	Detail         string `json:"detail"`
	Agent          string `json:"agent"`
	InputTokens    int64  `json:"input_tokens"`
	OutputTokens   int64  `json:"output_tokens"`
	// ActedFor names the person the event came from, where the source
	// names one ('slack:<user id>'), and 'none' where it does not — a
	// signed webhook has a sender the plane authenticated but no human
	// to name. Same closed vocabulary as the ledger (identity.go).
	ActedFor  string    `json:"acted_for"`
	CreatedAt time.Time `json:"created_at"`
}

// CredentialByName resolves a credential the config names (an HMAC
// hook's identity, a hook's budget credential) — no token involved.
func (s *Store) CredentialByName(ctx context.Context, name string) (Credential, error) {
	var c Credential
	err := s.pool.QueryRow(ctx,
		`SELECT name, cap_cents, cap_tokens, expires_at, created_at FROM credential WHERE name = $1`, name).
		Scan(&c.Name, &c.CapCents, &c.CapTokens, &c.ExpiresAt, &c.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Credential{}, ErrNotFound
	}
	return c, err
}

// RecordInboundAudit appends one inbound row. Committed rows are never
// modified (AdmitInboundEvent's one UPDATE happens inside its own
// transaction, before commit), like the spend ledger and tool_audit.
func (s *Store) RecordInboundAudit(ctx context.Context, e InboundAuditEntry) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO inbound_audit (hook, credential_name, delivery_id, decision, status, detail, agent, input_tokens, output_tokens, acted_for)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		auditText(e.Hook), e.CredentialName, auditText(e.DeliveryID), e.Decision, e.Status, auditText(e.Detail), auditText(e.Agent), e.InputTokens, e.OutputTokens,
		actedFor(e.ActedFor))
	return err
}

// AdmitInboundEvent admits one delivery under a live 'inbound' grant for
// the hook's credential, atomically: the admitted audit row (whose
// partial unique index rejects a replay) and the grant use (the same
// credential-locked consume the tool path uses) commit together.
// Ordering matters: the row is inserted FIRST, so a replay fails on the
// index before any use could be burned, and a missing grant rolls the
// row back so nothing is recorded as admitted that was not.
func (s *Store) AdmitInboundEvent(ctx context.Context, hook, credential, delivery, agent, actedForID string) (eventID, grantID string, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	err = tx.QueryRow(ctx,
		`INSERT INTO inbound_audit (hook, credential_name, delivery_id, decision, status, agent, acted_for)
		 VALUES ($1, $2, $3, 'admitted', 202, $4, $5) RETURNING id`,
		auditText(hook), credential, auditText(delivery), auditText(agent), actedFor(actedForID)).Scan(&eventID)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation on the admitted index
		return "", "", ErrReplay
	}
	if err != nil {
		return "", "", err
	}
	// The use is consumed under the credential lock like every other
	// grant consume, so concurrent events on one hook take turns.
	if _, err := lockCredential(ctx, tx, credential); err != nil {
		return "", "", err
	}
	grantID, ok, err := consumeGrantLocked(ctx, tx, credential, "inbound", hook)
	if err != nil {
		return "", "", err
	}
	if !ok {
		return "", "", ErrNoGrant
	}
	if _, err := tx.Exec(ctx,
		`UPDATE inbound_audit SET detail = $2 WHERE id = $1`, eventID, "granted "+grantID); err != nil {
		return "", "", err
	}
	return eventID, grantID, tx.Commit(ctx)
}

// InboundAudit returns the newest inbound rows for one hook (all hooks
// when empty), newest first.
func (s *Store) InboundAudit(ctx context.Context, hook string, limit int) ([]InboundAuditEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id, hook, credential_name, delivery_id, decision, status, detail, agent, input_tokens, output_tokens, acted_for, created_at
		 FROM inbound_audit
		 WHERE ($1 = '' OR hook = $1)
		 ORDER BY created_at DESC LIMIT $2`,
		hook, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InboundAuditEntry
	for rows.Next() {
		var e InboundAuditEntry
		if err := rows.Scan(&e.ID, &e.Hook, &e.CredentialName, &e.DeliveryID, &e.Decision, &e.Status,
			&e.Detail, &e.Agent, &e.InputTokens, &e.OutputTokens, &e.ActedFor, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// LiveBudgetGrantSum is the read-only half of AdmitSpend's grant step: the
// headroom live budget grants add to a cap right now, consuming nothing.
// The inbound door uses it to refuse an event whose spend could not be
// admitted at the proxy anyway, while leaving the consumption to the
// proxy (one use per admitted call, never two per event).
func (s *Store) LiveBudgetGrantSum(ctx context.Context, credential, subject string) (int64, error) {
	var extra int64
	err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(amount), 0) FROM permit_grant
		 WHERE credential_name = $1 AND kind = 'budget' AND subject = $2 AND `+grantLive,
		credential, subject).Scan(&extra)
	return extra, err
}
