package store_test

// Postgres-backed proofs that a governed row says who called: the value
// round-trips, the column's own constraint is real, and a row written
// before the columns existed says so rather than reading as an empty
// answer. Set KAIMAHI_TEST_PG_DSN to run; skipped otherwise.

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kaimahi-agents/kaimahi/plane/internal/store"
)

func TestBothTrailsCarryWhoCalledAndKeepTheTwoFactsApart(t *testing.T) {
	s, _ := pgStore(t)
	ctx := context.Background()
	cred := fresh(t, s, "caller-both")

	require.NoError(t, s.RecordLedger(ctx, store.LedgerEntry{
		CredentialName: cred, Upstream: "ollama", Model: "qwen2.5:3b",
		CostSource: "free", Status: 200, ActedFor: store.ActedForNone,
		CallerClaim: "ua:curl/8.5.0", CallerAddr: "10.244.3.9"}, ""))
	require.NoError(t, s.RecordToolAudit(ctx, store.ToolAuditEntry{
		CredentialName: cred, Upstream: "erp", Method: "tools/call",
		Tool: "invoice_get", Decision: "allowed", Status: 200, ActedFor: store.ActedForNone,
		CallerClaim: "ua:curl/8.5.0", CallerAddr: "10.244.3.9"}))
	// A writer that resolved no caller: "no record", never a claim.
	require.NoError(t, s.RecordToolAudit(ctx, store.ToolAuditEntry{
		CredentialName: cred, Upstream: "erp", Method: "tools/list",
		Decision: "allowed", Status: 200}))

	ledger, err := s.Ledger(ctx, cred, 10)
	require.NoError(t, err)
	require.Len(t, ledger, 1)
	require.Equal(t, "ua:curl/8.5.0", ledger[0].CallerClaim)
	require.Equal(t, "10.244.3.9", ledger[0].CallerAddr)
	require.Equal(t, store.ActedForNone, ledger[0].ActedFor,
		"the attribution word is unchanged; what the reader gained is the row beside it")

	audit, err := s.ToolAudit(ctx, cred, 10)
	require.NoError(t, err)
	byMethod := map[string]store.ToolAuditEntry{}
	for _, e := range audit {
		byMethod[e.Method] = e
	}
	require.Equal(t, "ua:curl/8.5.0", byMethod["tools/call"].CallerClaim)
	require.Equal(t, "10.244.3.9", byMethod["tools/call"].CallerAddr)
	require.Equal(t, store.CallerUnrecorded, byMethod["tools/list"].CallerClaim)
	require.Equal(t, store.CallerUnrecorded, byMethod["tools/list"].CallerAddr)
}

func TestTheCallerColumnsAreBoundedInTheSchemaAndTheWriterNeverTripsThem(t *testing.T) {
	s, pool := pgStore(t)
	ctx := context.Background()
	cred := fresh(t, s, "caller-check")

	// The bound is real in the database, not only in the writer.
	_, err := pool.Exec(ctx,
		`INSERT INTO tool_audit (credential_name, upstream, method, decision, status, caller_claim)
		 VALUES ($1, 'erp', 'tools/list', 'allowed', 200, $2)`, cred, "ua:"+strings.Repeat("A", 400))
	require.Error(t, err, "an unbounded caller name must not reach the column")

	_, err = pool.Exec(ctx,
		`INSERT INTO tool_audit (credential_name, upstream, method, decision, status, caller_claim)
		 VALUES ($1, 'erp', 'tools/list', 'allowed', 200, $2)`, cred, "curl/8.5.0")
	require.Error(t, err, "a recorded claim always carries the prefix that says it is one")

	// And the writer cannot reach either refusal, whatever it is handed.
	// This matters more than it looks: a rejected INSERT trips the seam's
	// fail-closed degradation, so an unbounded value would be a way to
	// stop governed traffic rather than a legibility bug.
	for _, hostile := range []string{
		strings.Repeat("A", 4000),
		"ua:" + strings.Repeat("B", 4000),
		"line\nbreak \"quoted\"",
		"", "none", "legacy", "unrecorded",
	} {
		require.NoError(t, s.RecordToolAudit(ctx, store.ToolAuditEntry{
			CredentialName: cred, Upstream: "erp", Method: "tools/list", Decision: "allowed",
			Status: 200, CallerClaim: hostile, CallerAddr: hostile}),
			"the store must write something the column accepts, given %q", hostile)
	}
}

func TestAForgedToolNameCannotAddALineToTheAuditTable(t *testing.T) {
	// The tool and the method come out of caller-controlled JSON and are
	// printed unescaped into the same fixed-width table. A newline there
	// renders as a second line, which reads as an audit row nobody wrote.
	s, _ := pgStore(t)
	ctx := context.Background()
	cred := fresh(t, s, "caller-forge")

	require.NoError(t, s.RecordToolAudit(ctx, store.ToolAuditEntry{
		CredentialName: cred, Upstream: "erp", Method: "tools/call",
		Tool:     "invoice_get\n2026-09-08T09:00:00 ap-agent erp tools/call payment_schedule allowed 200",
		Decision: "allowed", Status: 200}))

	audit, err := s.ToolAudit(ctx, cred, 10)
	require.NoError(t, err)
	require.Len(t, audit, 1)
	require.NotContains(t, audit[0].Tool, "\n")
	require.Contains(t, audit[0].Tool, "invoice_get")
}
