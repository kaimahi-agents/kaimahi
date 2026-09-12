package store_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/plane/internal/meter"
	"github.com/kaimahi-agents/kaimahi/plane/internal/store"
	"github.com/stretchr/testify/require"
)

func TestHistoricalToolBindingsRemainIntactWithoutAuthority(t *testing.T) {
	s, pool := pgStore(t)
	ctx := context.Background()
	name := fresh(t, s, "retired-tool")
	require.NoError(t, s.SetBudget(ctx, name, nil, i64(0)))
	digest := strings.Repeat("b", 64)
	for _, bound := range []*string{nil, &digest} {
		requestDigest := ""
		if bound != nil {
			requestDigest = *bound
		}
		for _, status := range []string{"pending", "approved", "denied"} {
			var id string
			require.NoError(t, pool.QueryRow(ctx,
				`INSERT INTO approval_request (credential_name,kind,subject,status,arg_digest,arg_summary)
				 VALUES ($1,'tool','tokens',$2,$3,'historical call') RETURNING id`, name, status, requestDigest).Scan(&id))
			action := status
			if status == "pending" {
				action = "requested"
			}
			_, err := pool.Exec(ctx,
				`INSERT INTO approval_audit (request_id,credential_name,kind,subject,action,arg_digest,arg_summary,decided_by)
				 VALUES ($1,$2,'tool','tokens',$3,$4,'historical call','admin')`, id, name, action, requestDigest)
			require.NoError(t, err)
			if status == "approved" {
				_, err = pool.Exec(ctx,
					`INSERT INTO permit_grant (request_id,credential_name,kind,subject,expires_at,max_uses,arg_digest)
					 VALUES ($1,$2,'tool','tokens',now() + interval '1 hour',1,$3)`, id, name, bound)
				require.NoError(t, err)
			}
		}
	}
	before := approvalHistory(t, pool, name)
	month := meter.MonthStartUTC(time.Now())
	a, err := s.AdmitSpend(ctx, name, meter.Hold(false), month, time.Minute)
	require.NoError(t, err)
	require.True(t, a.Denied, "neither bound nor pre-binding grants override model caps")

	// Raising the actual cap, not a historical permit, admits a call.
	require.NoError(t, s.SetBudget(ctx, name, nil, i64(1)))
	a, err = s.AdmitSpend(ctx, name, meter.Hold(false), month, time.Minute)
	require.NoError(t, err)
	require.False(t, a.Denied)
	require.NotEmpty(t, a.ReservationID)
	require.NoError(t, s.RecordLedger(ctx, store.LedgerEntry{CredentialName: name, Upstream: "o", Model: "m", InputTokens: 1, CostSource: "free", Status: 200}, a.ReservationID))
	open, err := s.OpenReservations(ctx, name)
	require.NoError(t, err)
	require.Zero(t, open)
	a, err = s.AdmitSpend(ctx, name, meter.Hold(false), month, time.Minute)
	require.NoError(t, err)
	require.True(t, a.Denied)
	require.Equal(t, before, approvalHistory(t, pool, name), "SQL retains statuses, NULL bindings, digests, summaries, bounds, and uses")
}
