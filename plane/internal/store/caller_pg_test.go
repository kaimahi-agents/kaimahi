package store_test

// PostgreSQL-backed model caller attribution and schema-bound proofs.

import (
	"context"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/plane/internal/store"
	"github.com/stretchr/testify/require"
)

func TestLedgerCarriesWhoCalledAndKeepsTheTwoFactsApart(t *testing.T) {
	s, _ := pgStore(t)
	ctx := context.Background()
	cred := fresh(t, s, "caller-ledger")
	require.NoError(t, s.RecordLedger(ctx, store.LedgerEntry{
		CredentialName: cred, Upstream: "ollama", Model: "qwen2.5:3b", CostSource: "free", Status: 200,
		ActedFor: store.ActedForNone, CallerClaim: "ua:curl/8.5.0", CallerAddr: "10.244.3.9"}, ""))
	ledger, err := s.Ledger(ctx, cred, 10)
	require.NoError(t, err)
	require.Len(t, ledger, 1)
	require.Equal(t, "ua:curl/8.5.0", ledger[0].CallerClaim)
	require.Equal(t, "10.244.3.9", ledger[0].CallerAddr)
	require.Equal(t, store.ActedForNone, ledger[0].ActedFor)
	// A writer that resolved no caller says unrecorded, never none.
	require.NoError(t, s.RecordLedger(ctx, store.LedgerEntry{CredentialName: cred, Upstream: "ollama", Model: "m", CostSource: "free", Status: 200}, ""))
	ledger, err = s.Ledger(ctx, cred, 10)
	require.NoError(t, err)
	require.Equal(t, store.CallerUnrecorded, ledger[0].CallerClaim)
	require.Equal(t, store.CallerUnrecorded, ledger[0].CallerAddr)
}

func TestTheCallerColumnsAreBoundedInTheSchemaAndTheWriterNeverTripsThem(t *testing.T) {
	s, pool := pgStore(t)
	ctx := context.Background()
	cred := fresh(t, s, "caller-check")
	for _, claim := range []string{"ua:" + strings.Repeat("A", 400), "curl/8.5.0"} {
		_, err := pool.Exec(ctx, `INSERT INTO ledger_entry (credential_name,upstream,model,cost_source,status,caller_claim) VALUES ($1,'ollama','m','free',200,$2)`, cred, claim)
		require.Error(t, err, "an unbounded or unmarked claim must not reach the column")
	}
	for _, hostile := range []string{strings.Repeat("A", 4000), "ua:" + strings.Repeat("B", 4000), "line\nbreak \"quoted\"", "", "none", "legacy", "unrecorded"} {
		require.NoError(t, s.RecordLedger(ctx, store.LedgerEntry{CredentialName: cred, Upstream: "ollama", Model: "m", CostSource: "free", Status: 200, CallerClaim: hostile, CallerAddr: hostile}, ""), "column must accept the sanitized value for %q", hostile)
	}
}

func TestAForgedUpstreamNameCannotAddALineEither(t *testing.T) {
	s, _ := pgStore(t)
	ctx := context.Background()
	cred := fresh(t, s, "caller-upstream")
	require.NoError(t, s.RecordLedger(ctx, store.LedgerEntry{CredentialName: cred,
		Upstream: "ollama\n2026-09-08T09:00:00 forged allowed 200", Model: "m", CostSource: "denied", Status: 403}, ""))
	ledger, err := s.Ledger(ctx, cred, 10)
	require.NoError(t, err)
	require.Len(t, ledger, 1)
	require.NotContains(t, ledger[0].Upstream, "\n")
}
