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

func TestRetiredToolFilingsAreRejected(t *testing.T) {
	s, _ := pgStore(t)
	ctx := context.Background()
	name := fresh(t, s, "retired-tool-filing")
	f := store.Filing{Credential: name, Kind: "tool", Subject: "tokens"}
	_, filed, err := s.FileRequest(ctx, f)
	require.ErrorIs(t, err, store.ErrBounds)
	require.False(t, filed)
	filed, err = s.FileApprovalRequest(ctx, f)
	require.ErrorIs(t, err, store.ErrBounds)
	require.False(t, filed)
	trail, err := s.ApprovalAudit(ctx, name, 10)
	require.NoError(t, err)
	require.Empty(t, trail)
}

func TestHistoricalToolRequestsRemainReadableAndDeniable(t *testing.T) {
	s, pool := pgStore(t)
	ctx := context.Background()
	for _, digest := range []string{"", strings.Repeat("a", 64)} {
		name := fresh(t, s, "retired-tool-request")
		var id string
		require.NoError(t, pool.QueryRow(ctx, `INSERT INTO approval_request (credential_name,kind,subject,arg_digest,arg_summary) VALUES ($1,'tool','tokens',$2,'historical call') RETURNING id`, name, digest).Scan(&id))
		_, err := s.ApproveRequest(ctx, id, nil, i32(1), nil, store.DecidedByAdmin)
		require.ErrorIs(t, err, store.ErrBounds)
		pending, err := s.PendingApprovals(ctx)
		require.NoError(t, err)
		var found bool
		for _, r := range pending {
			if r.ID == id {
				found = true
				require.Equal(t, "pending", r.Status)
				require.Equal(t, digest, r.ArgDigest)
				require.Equal(t, "historical call", r.ArgSummary)
			}
		}
		require.True(t, found)
		grants, _, err := s.Grants(ctx, name, 10)
		require.NoError(t, err)
		require.Empty(t, grants)
		require.NoError(t, s.DenyApprovalRequest(ctx, id, store.DecidedByAdmin))
		trail, err := s.ApprovalAudit(ctx, name, 10)
		require.NoError(t, err)
		require.Len(t, trail, 1)
		require.Equal(t, "denied", trail[0].Action)
		require.Equal(t, digest, trail[0].ArgDigest)
		require.Equal(t, "historical call", trail[0].ArgSummary)
	}
}

func TestHistoricalToolGrantsAreInertWhileBudgetGrantsStillAdmitSpend(t *testing.T) {
	s, pool := pgStore(t)
	ctx := context.Background()
	name := fresh(t, s, "retired-tool-grant")
	require.NoError(t, s.SetBudget(ctx, name, nil, i64(0)))
	var requestID string
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO approval_request (credential_name,kind,subject,status) VALUES ($1,'tool','tokens','approved') RETURNING id`, name).Scan(&requestID))
	digest := strings.Repeat("b", 64)
	for _, bound := range []*string{nil, &digest} {
		_, err := pool.Exec(ctx, `INSERT INTO permit_grant (request_id,credential_name,kind,subject,max_uses,arg_digest) VALUES ($1,$2,'tool','tokens',1,$3)`, requestID, name, bound)
		require.NoError(t, err)
	}
	grants, live, err := s.Grants(ctx, name, 10)
	require.NoError(t, err)
	require.Len(t, grants, 2)
	require.Equal(t, []bool{false, false}, live)
	require.Equal(t, &digest, grants[0].ArgDigest)
	require.Nil(t, grants[1].ArgDigest)
	counts, err := s.LiveGrantCounts(ctx)
	require.NoError(t, err)
	require.NotContains(t, counts, "tool")
	month := meter.MonthStartUTC(time.Now())
	admission, err := s.AdmitSpend(ctx, name, meter.Hold(false), month, time.Minute)
	require.NoError(t, err)
	require.True(t, admission.Denied, "a tool grant with the same subject cannot cover a budget")
	id, _, err := s.FileRequest(ctx, store.Filing{Credential: name, Kind: "budget", Subject: "tokens"})
	require.NoError(t, err)
	budget, err := s.ApproveRequest(ctx, id, nil, i32(1), i64(10), store.DecidedByAdmin)
	require.NoError(t, err)
	require.Nil(t, budget.ArgDigest)
	grants, live, err = s.Grants(ctx, name, 10)
	require.NoError(t, err)
	require.Equal(t, budget.ID, grants[0].ID)
	require.True(t, live[0])
	counts, err = s.LiveGrantCounts(ctx)
	require.NoError(t, err)
	require.Positive(t, counts["budget"])
	admission, err = s.AdmitSpend(ctx, name, meter.Hold(false), month, time.Minute)
	require.NoError(t, err)
	require.True(t, admission.Granted)
	require.NotEmpty(t, admission.ReservationID)
	require.NoError(t, s.RecordLedger(ctx, store.LedgerEntry{CredentialName: name, Upstream: "o", Model: "m", InputTokens: 1, CostSource: "free", Status: 200}, admission.ReservationID))
	open, err := s.OpenReservations(ctx, name)
	require.NoError(t, err)
	require.Zero(t, open)
	grants, live, err = s.Grants(ctx, name, 10)
	require.NoError(t, err)
	require.Equal(t, []bool{false, false, false}, live)
	require.EqualValues(t, 1, grants[0].Uses)
	require.Zero(t, grants[1].Uses)
	require.Zero(t, grants[2].Uses)
	admission, err = s.AdmitSpend(ctx, name, meter.Hold(false), month, time.Minute)
	require.NoError(t, err)
	require.True(t, admission.Denied, "the budget use remains exact")
}
