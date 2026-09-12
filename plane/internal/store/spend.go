package store

// Exact budgets: the one transaction that admits spend. Every
// budget decision for a credential runs under a lock on that
// credential's row, reads the caps from the locked row, counts the
// ledger PLUS the open reservations, consumes grant uses if a cap is
// exceeded, and leaves a reservation the ledger write later consumes.
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
// Denied; Granted says a live budget grant admitted an over-cap call
// (one use consumed per exceeded cap, all in this transaction).
type Admission struct {
	ReservationID string
	Denied        bool
	Subject       string
	Granted       bool
}

// lockCredential serializes every governance decision for one
// credential: FOR NO KEY UPDATE is exclusive against itself (two
// admissions, a grant consume and an admission) but does not block the
// KEY SHARE locks that inserting a grant or a request for the
// credential takes, so an approval never waits on the data path.
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
// with the caps read from the locked row, an exceeded cap is covered by
// live budget grants or the call is denied (a denial consumes nothing),
// and an admitted call under caps leaves a reservation of hold that
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

	var needs []BudgetNeed
	if cred.CapCents != nil && cents >= *cred.CapCents {
		needs = append(needs, BudgetNeed{Subject: "cents", Used: cents, Cap: *cred.CapCents})
	}
	if cred.CapTokens != nil && tokens >= *cred.CapTokens {
		needs = append(needs, BudgetNeed{Subject: "tokens", Used: tokens, Cap: *cred.CapTokens})
	}
	// Every exceeded cap must be covered by live grants, or the call is
	// denied and no use is consumed (the rollback undoes any consumed
	// before the uncovered one).
	for _, n := range needs {
		var extra int64
		if err := tx.QueryRow(ctx,
			`SELECT COALESCE(SUM(amount), 0) FROM permit_grant
			 WHERE credential_name = $1 AND kind = 'budget' AND subject = $2 AND `+grantLive,
			credential, n.Subject).Scan(&extra); err != nil {
			return Admission{}, err
		}
		if extra <= 0 || n.Used >= n.Cap+extra {
			return Admission{Denied: true, Subject: n.Subject}, nil
		}
		if _, ok, err := consumeGrantLocked(ctx, tx, credential, "budget", n.Subject); err != nil {
			return Admission{}, err
		} else if !ok {
			return Admission{Denied: true, Subject: n.Subject}, nil
		}
	}

	var id string
	if err := tx.QueryRow(ctx,
		`INSERT INTO spend_reservation (credential_name, hold_cents, hold_tokens, expires_at)
		 VALUES ($1, $2, $3, now() + $4) RETURNING id`,
		credential, hold.Cents, hold.Tokens, ttl).Scan(&id); err != nil {
		return Admission{}, err
	}
	return Admission{ReservationID: id, Granted: len(needs) > 0}, tx.Commit(ctx)
}

// MonthCommitted is the unlocked read of committed spend: the ledger
// since monthStart plus the holds of every open reservation. Retained
// for the Postgres-backed reservation proof; admission uses the same
// query under the credential lock. The admin ledger shows MonthUsage
// (rows only), so an in-flight call is never displayed as spend.
func (s *Store) MonthCommitted(ctx context.Context, credential string, monthStart time.Time) (cents, tokens int64, err error) {
	return monthCommitted(ctx, s.pool, credential, monthStart)
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
