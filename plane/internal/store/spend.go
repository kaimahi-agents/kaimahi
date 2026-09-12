package store

// Exact budgets: the one transaction that admits spend. Every
// budget decision for a credential runs under a lock on that
// credential's row, reads the caps from the locked row, counts the
// ledger PLUS the open reservations, denies when a cap is reached,
// and leaves a reservation the ledger write later consumes.
// Serial per credential by construction, so N replicas admit exactly
// what one replica admitting one call at a time would.

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// SpendHold is what an admitted call commits against each cap until its
// ledger row lands: the least it can spend, never an estimate.
type SpendHold struct {
	Cents  int64
	Tokens int64
}

// Admission is AdmitSpend's verdict. ReservationID is set when the call
// is admitted under a cap (empty when the credential has no caps —
// nothing to reserve against); Subject names the exceeded cap when
// Denied.
type Admission struct {
	ReservationID string
	Denied        bool
	Subject       string
}

// lockCredential serializes admissions for one credential. FOR NO KEY
// UPDATE is exclusive against itself but permits the KEY SHARE locks
// taken by inserts referencing the credential.
// ErrNotFound when the credential does not exist.
func lockCredential(ctx context.Context, tx pgx.Tx, name string) (Credential, error) {
	var c Credential
	err := tx.QueryRow(ctx,
		`SELECT name, cap_cents, cap_tokens, expires_at, created_at FROM credential WHERE name = $1 FOR NO KEY UPDATE`,
		name).Scan(&c.Name, &c.CapCents, &c.CapTokens, &c.ExpiresAt, &c.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Credential{}, ErrNotFound
	}
	return c, err
}

// AdmitSpend decides one call against the credential's monthly caps,
// exactly. Under the credential lock: expired reservations are swept,
// committed spend (ledger since monthStart + open holds) is compared
// with the caps read from the locked row, a reached cap denies the call
// (a denial consumes nothing), and an admitted call leaves a reservation that
// expires after ttl. Fail closed is the caller's job on error.
func (s *Store) AdmitSpend(ctx context.Context, credential string, hold SpendHold,
	monthStart time.Time, ttl time.Duration) (Admission, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Admission{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	cred, err := lockCredential(ctx, tx, credential)
	if err != nil {
		return Admission{}, err
	}
	if cred.CapCents == nil && cred.CapTokens == nil {
		// No cap of either kind: nothing to count against, nothing to
		// hold. The row lock is released with the (empty) commit.
		return Admission{}, tx.Commit(ctx)
	}

	// A reservation a crashed replica never consumed stops counting when
	// it expires; sweeping here keeps the table at "calls in flight".
	if _, err := tx.Exec(ctx,
		`DELETE FROM spend_reservation WHERE credential_name = $1 AND expires_at <= now()`,
		credential); err != nil {
		return Admission{}, err
	}
	cents, tokens, err := monthCommitted(ctx, tx, credential, monthStart)
	if err != nil {
		return Admission{}, err
	}

	if cred.CapCents != nil && cents >= *cred.CapCents {
		return Admission{Denied: true, Subject: "cents"}, nil
	}
	if cred.CapTokens != nil && tokens >= *cred.CapTokens {
		return Admission{Denied: true, Subject: "tokens"}, nil
	}

	var id string
	if err := tx.QueryRow(ctx,
		`INSERT INTO spend_reservation (credential_name, hold_cents, hold_tokens, expires_at)
		 VALUES ($1, $2, $3, now() + $4) RETURNING id`,
		credential, hold.Cents, hold.Tokens, ttl).Scan(&id); err != nil {
		return Admission{}, err
	}
	return Admission{ReservationID: id}, tx.Commit(ctx)
}

// MonthCommitted is the unlocked read of committed spend: the ledger
// since monthStart plus the holds of every open reservation. Retained
// for the Postgres-backed reservation proof; admission uses the same
// query under the credential lock. The admin ledger shows MonthUsage
// (rows only), so an in-flight call is never displayed as spend.
func (s *Store) MonthCommitted(ctx context.Context, credential string, monthStart time.Time) (cents, tokens int64, err error) {
	return monthCommitted(ctx, s.pool, credential, monthStart)
}

// rowQuerier is the one method the shared queries need, satisfied by
// both the pool and a transaction.
type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func monthCommitted(ctx context.Context, q rowQuerier, credential string, monthStart time.Time) (cents, tokens int64, err error) {
	err = q.QueryRow(ctx,
		`SELECT
		   (SELECT COALESCE(SUM(cost_cents), 0) FROM ledger_entry
		     WHERE credential_name = $1 AND created_at >= $2)
		 + (SELECT COALESCE(SUM(hold_cents), 0) FROM spend_reservation
		     WHERE credential_name = $1 AND created_at >= $2 AND expires_at > now()),
		   (SELECT COALESCE(SUM(input_tokens + output_tokens), 0) FROM ledger_entry
		     WHERE credential_name = $1 AND created_at >= $2)
		 + (SELECT COALESCE(SUM(hold_tokens), 0) FROM spend_reservation
		     WHERE credential_name = $1 AND created_at >= $2 AND expires_at > now())`,
		credential, monthStart).Scan(&cents, &tokens)
	return cents, tokens, err
}

// OpenReservations counts the reservations still counting against a
// credential (all credentials when empty) — the metrics gauge, and what
// a test reads to prove every admitted call consumed its hold.
func (s *Store) OpenReservations(ctx context.Context, credential string) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM spend_reservation
		 WHERE ($1 = '' OR credential_name = $1) AND expires_at > now()`, credential).Scan(&n)
	return n, err
}
