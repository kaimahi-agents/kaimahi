package store

// The replica-independent reads the metrics collector makes at
// scrape time. Names only — a credential's name is public in the repo
// and printed by every audit command; nothing here returns a token, an
// id, or free text.

import (
	"context"
	"time"

	"github.com/kaimahi-agents/kaimahi/plane/internal/metrics"
)

// LedgerMonthTotals sums the ledger per credential name since
// monthStart (rows only — an in-flight hold is not spend).
func (s *Store) LedgerMonthTotals(ctx context.Context, monthStart time.Time) ([]metrics.LedgerTotal, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT credential_name, COALESCE(SUM(cost_cents), 0), COALESCE(SUM(input_tokens + output_tokens), 0)
		 FROM ledger_entry WHERE created_at >= $1 GROUP BY credential_name ORDER BY credential_name`, monthStart)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []metrics.LedgerTotal
	for rows.Next() {
		var t metrics.LedgerTotal
		if err := rows.Scan(&t.Credential, &t.Cents, &t.Tokens); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// CredentialDeadlines reports how long each governed credential has
// left. A legacy credential (no expiry) is counted as such rather than
// given an invented deadline — an operator must be able to see the
// no-expiry class shrink, not have it hidden behind a large number.
func (s *Store) CredentialDeadlines(ctx context.Context, now time.Time) ([]metrics.CredentialDeadline, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT name, expires_at FROM credential ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []metrics.CredentialDeadline
	for rows.Next() {
		var name string
		var expires *time.Time
		if err := rows.Scan(&name, &expires); err != nil {
			return nil, err
		}
		d := metrics.CredentialDeadline{Credential: name, Legacy: expires == nil}
		if expires != nil {
			d.Seconds = expires.Sub(now).Seconds()
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
