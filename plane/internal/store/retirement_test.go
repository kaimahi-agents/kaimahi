package store_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kaimahi-agents/kaimahi/plane/internal/store"
)

func TestRetiredInboundFilingsAreRejected(t *testing.T) {
	s, _ := pgStore(t)
	ctx := context.Background()
	name := fresh(t, s, "retired-filing")
	_, filed, err := s.FileRequest(ctx, store.Filing{Credential: name, Kind: "inbound", Subject: "demo"})
	require.ErrorIs(t, err, store.ErrBounds)
	require.False(t, filed)
	trail, err := s.ApprovalAudit(ctx, name, 10)
	require.NoError(t, err)
	require.Empty(t, trail, "rejecting a retired kind leaves no filing or audit row")
}

func TestHistoricalInboundApprovalsRemainReadableButCannotMintGrants(t *testing.T) {
	s, pool := pgStore(t)
	ctx := context.Background()
	name := fresh(t, s, "retired-request")
	var id string
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO approval_request (credential_name, kind, subject)
		 VALUES ($1, 'inbound', 'demo') RETURNING id`, name).Scan(&id))

	_, err := s.ApproveRequest(ctx, id, nil, i32(1), nil, store.DecidedByAdmin)
	require.ErrorIs(t, err, store.ErrBounds)
	pending, err := s.PendingApprovals(ctx)
	require.NoError(t, err)
	var found bool
	for _, r := range pending {
		if r.ID == id {
			found = true
			require.Equal(t, "inbound", r.Kind)
			require.Equal(t, "pending", r.Status)
		}
	}
	require.True(t, found, "rejection preserves the historical request")
	grants, _, err := s.Grants(ctx, name, 10)
	require.NoError(t, err)
	require.Empty(t, grants)

	// An operator may still close the old pending request without
	// authorizing any action; its audit remains readable.
	require.NoError(t, s.DenyApprovalRequest(ctx, id, store.DecidedByAdmin))
	trail, err := s.ApprovalAudit(ctx, name, 10)
	require.NoError(t, err)
	require.Len(t, trail, 1)
	require.Equal(t, "denied", trail[0].Action)
	require.Equal(t, "inbound", trail[0].Kind)
}

func TestHistoricalInboundGrantIsInert(t *testing.T) {
	s, pool := pgStore(t)
	ctx := context.Background()
	name := fresh(t, s, "retired-grant")
	var requestID, grantID string
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO approval_request (credential_name, kind, subject, status)
		 VALUES ($1, 'inbound', 'demo', 'approved') RETURNING id`, name).Scan(&requestID))
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO permit_grant (request_id, credential_name, kind, subject, max_uses)
		 VALUES ($1, $2, 'inbound', 'demo', 1) RETURNING id`, requestID, name).Scan(&grantID))

	grants, live, err := s.Grants(ctx, name, 10)
	require.NoError(t, err)
	require.Len(t, grants, 1)
	require.Equal(t, grantID, grants[0].ID, "the old row is not deleted")
	require.EqualValues(t, 0, grants[0].Uses)
	require.False(t, live[0], "no inbound dispatcher remains to execute this grant")
	counts, err := s.LiveGrantCounts(ctx)
	require.NoError(t, err)
	require.NotContains(t, counts, "inbound", "retired grants are not reported as executable")
	_, ok, err := s.ConsumeToolGrant(ctx, name, "demo", digestOf("demo"))
	require.NoError(t, err)
	require.False(t, ok, "an old inbound grant cannot become a tool grant")
}
