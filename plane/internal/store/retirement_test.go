package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/kaimahi-agents/kaimahi/plane/internal/meter"
)

// Historical rows are SQL/backups-only. Compare whole rows so retirement
// cannot silently normalize statuses, consume uses, or rewrite bounds.
func approvalHistory(t *testing.T, pool *pgxpool.Pool, name string) []string {
	t.Helper()
	var out []string
	for _, table := range []string{"approval_request", "permit_grant", "approval_audit"} {
		var rows string
		require.NoError(t, pool.QueryRow(context.Background(),
			`SELECT COALESCE(jsonb_agg(to_jsonb(h) ORDER BY id), '[]'::jsonb)::text FROM `+table+` h WHERE credential_name = $1`, name).Scan(&rows))
		out = append(out, rows)
	}
	return out
}

func TestHistoricalGrantsCannotBypassCapsOrChangeHistory(t *testing.T) {
	s, pool := pgStore(t)
	ctx := context.Background()
	for _, kind := range []string{"budget", "tool", "inbound"} {
		for _, caps := range []struct {
			name, subject string
			cents, tokens *int64
		}{
			{"cents", "cents", i64(0), nil},
			{"tokens", "tokens", nil, i64(0)},
			{"both", "cents", i64(0), i64(0)},
		} {
			t.Run(kind+"/"+caps.name, func(t *testing.T) {
				name := fresh(t, s, "retired-history")
				subject := caps.subject
				require.NoError(t, s.SetBudget(ctx, name, caps.cents, caps.tokens))
				for _, status := range []string{"pending", "approved", "denied"} {
					var id string
					require.NoError(t, pool.QueryRow(ctx,
						`INSERT INTO approval_request (credential_name,kind,subject,status,detail,decided_at,decided_by)
						 VALUES ($1,$2,$3,$4,'historical detail',CASE WHEN $4 = 'pending' THEN NULL ELSE now() END,
						 CASE WHEN $4 = 'pending' THEN '' ELSE 'admin' END) RETURNING id`, name, kind, subject, status).Scan(&id))
					action := status
					if status == "pending" {
						action = "requested"
					}
					_, err := pool.Exec(ctx,
						`INSERT INTO approval_audit (request_id,credential_name,kind,subject,action,bounds,decided_by)
						 VALUES ($1,$2,$3,$4,$5,'historical bounds','admin')`, id, name, kind, subject, action)
					require.NoError(t, err)
					if status == "approved" {
						_, err = pool.Exec(ctx,
							`INSERT INTO permit_grant (request_id,credential_name,kind,subject,expires_at,max_uses,uses,amount,decided_by)
							 VALUES ($1,$2,$3,$4,now() + interval '1 hour',100,2,CASE WHEN $3 = 'budget' THEN 1000 ELSE NULL END,'admin')`, id, name, kind, subject)
						require.NoError(t, err)
					}
				}
				before := approvalHistory(t, pool, name)
				for range 3 {
					a, err := s.AdmitSpend(ctx, name, meter.Hold(true), meter.MonthStartUTC(time.Now()), time.Minute)
					require.NoError(t, err)
					require.True(t, a.Denied, "historical grants must confer no authority")
					require.Equal(t, subject, a.Subject, "cents takes precedence when both caps are exceeded")
					require.Empty(t, a.ReservationID)
				}
				require.Equal(t, before, approvalHistory(t, pool, name), "no uses consumed, filings, decisions, or audit writes")
				open, err := s.OpenReservations(ctx, name)
				require.NoError(t, err)
				require.Zero(t, open)
			})
		}
	}
}
