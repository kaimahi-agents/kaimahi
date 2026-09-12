package store_test

// The governance-bearing limits are DB-exact, and that is proven
// here against a REAL Postgres under real concurrency — goroutines
// racing the same SQL the replicas run — not argued from the code.
// Set KAIMAHI_TEST_PG_DSN to run (CI's go-plane job provides a service
// container; locally, any throwaway Postgres 16). Skipped otherwise, so
// `go test ./...` without a database still passes.

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/kaimahi-agents/kaimahi/plane/internal/db"
	"github.com/kaimahi-agents/kaimahi/plane/internal/meter"
	"github.com/kaimahi-agents/kaimahi/plane/internal/store"
)

func pgStore(t *testing.T) (*store.Store, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("KAIMAHI_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("KAIMAHI_TEST_PG_DSN not set; skipping Postgres-backed tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	require.NoError(t, db.Migrate(ctx, dsn))
	pool, err := db.NewPool(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return store.New(pool), pool
}

// fresh mints a credential with a unique name so parallel tests and
// repeated runs never share rows.
func fresh(t *testing.T, s *store.Store, prefix string) string {
	t.Helper()
	name := fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	h := sha256.Sum256([]byte(name))
	require.NoError(t, s.CreateCredential(context.Background(), name, h[:], time.Now().Add(time.Hour)))
	return name
}

func i64(v int64) *int64 { return &v }

// race runs fn n times concurrently, released together, and returns
// each call's result.
func race[T any](n int, fn func(i int) T) []T {
	out := make([]T, n)
	var start, done sync.WaitGroup
	start.Add(1)
	done.Add(n)
	for i := range n {
		go func() {
			defer done.Done()
			start.Wait()
			out[i] = fn(i)
		}()
	}
	start.Done()
	done.Wait()
	return out
}

func TestMigrateTwiceConcurrentlyIsSerialAndIdempotent(t *testing.T) {
	dsn := os.Getenv("KAIMAHI_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("KAIMAHI_TEST_PG_DSN not set")
	}
	// Two "replicas" migrating together: both succeed, neither errors
	// on the other's DDL, and the version table ends with one row per
	// migration (the second finds nothing to do under the lock).
	errs := race(2, func(int) error {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		return db.Migrate(ctx, dsn)
	})
	for _, err := range errs {
		require.NoError(t, err)
	}
	_, pool := pgStore(t)
	var versions, distinct int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT COUNT(*), COUNT(DISTINCT version_id) FROM goose_db_version WHERE version_id > 0`).Scan(&versions, &distinct))
	require.Equal(t, distinct, versions, "a migration was recorded twice")
	require.Equal(t, committedMigrations(t), versions,
		"the version table must end with one row per committed migration; a hand-written floor "+
			"goes stale the moment a migration is added and stops noticing that one did not run")
}

func TestConcurrentAdmissionsAgainstOneMoreCallAdmitExactlyOne(t *testing.T) {
	s, _ := pgStore(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		caps func(name string)
		hold store.SpendHold
	}{
		{"tokens", func(n string) { require.NoError(t, s.SetBudget(ctx, n, nil, i64(1))) }, meter.Hold(false)},
		{"cents", func(n string) { require.NoError(t, s.SetBudget(ctx, n, i64(1), nil)) }, meter.Hold(true)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			name := fresh(t, s, "race")
			tc.caps(name)
			month := meter.MonthStartUTC(time.Now())
			// Twenty concurrent calls against a cap with room for exactly
			// one more: without the lock every one of them would read
			// "0 < 1" and be admitted.
			results := race(20, func(int) store.Admission {
				a, err := s.AdmitSpend(ctx, name, tc.hold, month, time.Minute)
				require.NoError(t, err)
				return a
			})
			var admitted, denied int
			for _, a := range results {
				if a.Denied {
					denied++
					require.Equal(t, tc.name, a.Subject)
				} else {
					admitted++
					require.NotEmpty(t, a.ReservationID)
				}
			}
			require.Equal(t, 1, admitted, "exactly one admitted")
			require.Equal(t, 19, denied)
			open, err := s.OpenReservations(ctx, name)
			require.NoError(t, err)
			require.EqualValues(t, 1, open)
		})
	}
}

func TestReservationIsConsumedByTheLedgerWriteAndCountsUntilThen(t *testing.T) {
	s, _ := pgStore(t)
	ctx := context.Background()
	name := fresh(t, s, "hold")
	require.NoError(t, s.SetBudget(ctx, name, nil, i64(100)))
	month := meter.MonthStartUTC(time.Now())

	a, err := s.AdmitSpend(ctx, name, meter.Hold(false), month, time.Minute)
	require.NoError(t, err)
	require.False(t, a.Denied)
	// Committed spend sees the hold; the display sum does not.
	_, tokens, err := s.MonthCommitted(ctx, name, month)
	require.NoError(t, err)
	require.EqualValues(t, 1, tokens)
	_, shown, err := s.MonthUsage(ctx, name, month)
	require.NoError(t, err)
	require.Zero(t, shown)

	require.NoError(t, s.RecordLedger(ctx, store.LedgerEntry{CredentialName: name, Upstream: "u", Model: "m",
		InputTokens: 40, OutputTokens: 30, CostSource: "free", Status: 200}, a.ReservationID))
	open, err := s.OpenReservations(ctx, name)
	require.NoError(t, err)
	require.Zero(t, open, "the row replaced the hold")
	_, tokens, err = s.MonthCommitted(ctx, name, month)
	require.NoError(t, err)
	require.EqualValues(t, 70, tokens)

	// A denial consumes nothing and holds nothing.
	require.NoError(t, s.SetBudget(ctx, name, nil, i64(70)))
	a, err = s.AdmitSpend(ctx, name, meter.Hold(false), month, time.Minute)
	require.NoError(t, err)
	require.True(t, a.Denied)
	require.Empty(t, a.ReservationID)
	require.NoError(t, s.RecordLedger(ctx, store.LedgerEntry{CredentialName: name, Upstream: "u", Model: "m",
		CostSource: "denied", Status: 429}, ""))
	open, err = s.OpenReservations(ctx, name)
	require.NoError(t, err)
	require.Zero(t, open)
}

func TestExpiredReservationStopsCountingAndIsSwept(t *testing.T) {
	s, pool := pgStore(t)
	ctx := context.Background()
	name := fresh(t, s, "expire")
	require.NoError(t, s.SetBudget(ctx, name, nil, i64(1)))
	month := meter.MonthStartUTC(time.Now())
	// A crashed replica's hold: admitted with a TTL that lapses at once.
	a, err := s.AdmitSpend(ctx, name, meter.Hold(false), month, time.Millisecond)
	require.NoError(t, err)
	require.False(t, a.Denied)
	expiredID := a.ReservationID
	time.Sleep(20 * time.Millisecond)
	open, err := s.OpenReservations(ctx, name)
	require.NoError(t, err)
	require.Zero(t, open, "an expired hold no longer counts")
	// The next admission sweeps it and is admitted (the cap has room again).
	a, err = s.AdmitSpend(ctx, name, meter.Hold(false), month, time.Minute)
	require.NoError(t, err)
	require.False(t, a.Denied)
	var remaining int
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM spend_reservation WHERE id = $1`, expiredID).Scan(&remaining))
	require.Zero(t, remaining, "expiry is swept, not only ignored")
	// A late settlement still lands after its hold was swept, without
	// consuming the new call's reservation.
	require.NoError(t, s.RecordLedger(ctx, store.LedgerEntry{CredentialName: name, Upstream: "u", Model: "m",
		InputTokens: 2, CostSource: "free", Status: 200}, expiredID))
	_, tokens, err := s.MonthCommitted(ctx, name, month)
	require.NoError(t, err)
	require.EqualValues(t, 3, tokens)
	open, err = s.OpenReservations(ctx, name)
	require.NoError(t, err)
	require.EqualValues(t, 1, open)
}

func TestFailedAdmissionRollsBackItsReservationSweep(t *testing.T) {
	s, pool := pgStore(t)
	ctx := context.Background()
	name := fresh(t, s, "rollback-admit")
	require.NoError(t, s.SetBudget(ctx, name, nil, i64(1)))
	month := meter.MonthStartUTC(time.Now())
	a, err := s.AdmitSpend(ctx, name, meter.Hold(false), month, -time.Second)
	require.NoError(t, err)
	_, err = s.AdmitSpend(ctx, name, store.SpendHold{Tokens: -1}, month, time.Minute)
	require.Error(t, err, "a rejected reservation insert must abort the whole transaction")
	var remaining int
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM spend_reservation WHERE id = $1`, a.ReservationID).Scan(&remaining))
	require.Equal(t, 1, remaining, "the earlier sweep must roll back too")
	open, err := s.OpenReservations(ctx, name)
	require.NoError(t, err)
	require.Zero(t, open)
}

func TestFailedSettlementKeepsHoldAndRollsBackLedgerInsert(t *testing.T) {
	s, _ := pgStore(t)
	ctx := context.Background()
	name := fresh(t, s, "rollback-ledger")
	require.NoError(t, s.SetBudget(ctx, name, nil, i64(1)))
	month := meter.MonthStartUTC(time.Now())
	a, err := s.AdmitSpend(ctx, name, meter.Hold(false), month, time.Minute)
	require.NoError(t, err)
	e := store.LedgerEntry{CredentialName: name, Upstream: "u", Model: "m", InputTokens: 1, CostSource: "free", Status: 200}
	require.Error(t, s.RecordLedger(ctx, e, "not-a-uuid"), "failure after the insert must roll it back")
	rows, err := s.Ledger(ctx, name, 10)
	require.NoError(t, err)
	require.Empty(t, rows)
	open, err := s.OpenReservations(ctx, name)
	require.NoError(t, err)
	require.EqualValues(t, 1, open)
	denied, err := s.AdmitSpend(ctx, name, meter.Hold(false), month, time.Minute)
	require.NoError(t, err)
	require.True(t, denied.Denied, "the failed settlement cannot reopen headroom")
	require.NoError(t, s.RecordLedger(ctx, e, a.ReservationID))
	open, err = s.OpenReservations(ctx, name)
	require.NoError(t, err)
	require.Zero(t, open)
}

func TestNoCapsHoldsNothing(t *testing.T) {
	s, _ := pgStore(t)
	ctx := context.Background()
	name := fresh(t, s, "uncapped")
	a, err := s.AdmitSpend(ctx, name, meter.Hold(true), meter.MonthStartUTC(time.Now()), time.Minute)
	require.NoError(t, err)
	require.False(t, a.Denied)
	require.Empty(t, a.ReservationID)
	_, err = s.AdmitSpend(ctx, "no-such-credential", meter.Hold(true), meter.MonthStartUTC(time.Now()), time.Minute)
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestConcurrentOverCapCallsCannotConsumeHistoricalBudgetGrant(t *testing.T) {
	s, pool := pgStore(t)
	ctx := context.Background()
	name := fresh(t, s, "bgrant")
	require.NoError(t, s.SetBudget(ctx, name, nil, i64(1)))
	month := meter.MonthStartUTC(time.Now())
	require.NoError(t, s.RecordLedger(ctx, store.LedgerEntry{CredentialName: name, Upstream: "u", Model: "m",
		InputTokens: 1, CostSource: "free", Status: 200}, ""))
	var id string
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO approval_request (credential_name,kind,subject,status) VALUES ($1,'budget','tokens','approved') RETURNING id`, name).Scan(&id))
	_, err := pool.Exec(ctx,
		`INSERT INTO permit_grant (request_id,credential_name,kind,subject,expires_at,max_uses,amount)
		 VALUES ($1,$2,'budget','tokens',now() + interval '1 hour',1,1000)`, id, name)
	require.NoError(t, err)
	before := approvalHistory(t, pool, name)

	results := race(20, func(int) store.Admission {
		a, err := s.AdmitSpend(ctx, name, meter.Hold(false), month, time.Minute)
		require.NoError(t, err)
		return a
	})
	for _, a := range results {
		require.True(t, a.Denied)
		require.Equal(t, "tokens", a.Subject)
		require.Empty(t, a.ReservationID)
	}
	require.Equal(t, before, approvalHistory(t, pool, name))
	open, err := s.OpenReservations(ctx, name)
	require.NoError(t, err)
	require.Zero(t, open)
}

// TestAdmissionHotPathCost measures the locked admission's cost so the
// PR can state it; it never fails on timing (a loaded runner is not a
// regression) — it prints the numbers.
func TestAdmissionHotPathCost(t *testing.T) {
	s, _ := pgStore(t)
	ctx := context.Background()
	name := fresh(t, s, "cost")
	require.NoError(t, s.SetBudget(ctx, name, nil, i64(1_000_000)))
	month := meter.MonthStartUTC(time.Now())
	const n = 200
	start := time.Now()
	for range n {
		a, err := s.AdmitSpend(ctx, name, meter.Hold(false), month, time.Minute)
		require.NoError(t, err)
		require.NoError(t, s.RecordLedger(ctx, store.LedgerEntry{CredentialName: name, Upstream: "u", Model: "m",
			InputTokens: 1, CostSource: "free", Status: 200}, a.ReservationID))
	}
	serial := time.Since(start) / n
	start = time.Now()
	errs := race(n, func(int) error {
		a, err := s.AdmitSpend(ctx, name, meter.Hold(false), month, time.Minute)
		if err != nil {
			return err
		}
		return s.RecordLedger(ctx, store.LedgerEntry{CredentialName: name, Upstream: "u", Model: "m",
			InputTokens: 1, CostSource: "free", Status: 200}, a.ReservationID)
	})
	concurrent := time.Since(start) / n
	// A timing that hides a failed call is not a measurement.
	for _, err := range errs {
		require.NoError(t, err)
	}
	t.Logf("admit+record per call: serial %v, %d-way concurrent (amortised) %v", serial, n, concurrent)
}

// committedMigrations counts the migration files the plane embeds, so the
// number this test expects is read off the tree rather than typed into it. The
// number typed in here was 7 while ten migrations were committed, which is the
// failure mode of every hand-written count: the tree moves and the count does
// not, and a floor below the truth stops proving that every migration ran.
func committedMigrations(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join("..", "db", "migrations"))
	require.NoError(t, err)
	n := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			n++
		}
	}
	require.Greater(t, n, 0,
		"no migration files found; a count with nothing in it would let this test pass by having nothing to check")
	return n
}
