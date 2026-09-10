package store_test

// Postgres-backed proofs for two things: WHO an agent acted for, and
// credentials that expire. Set
// KAIMAHI_TEST_PG_DSN to run (CI's go-plane job provides a service
// container); skipped otherwise, like the rest of the store's
// Postgres tests.

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/kaimahi-agents/kaimahi/plane/internal/store"
)

// The three attribution outcomes are DIFFERENT answers and must stay
// distinguishable: no run open is "there is no person" ('none'); one
// run open is that run's actor; two runs open at once is "the plane
// cannot say" ('unknown') — never "nobody was there".
func TestAttributionSaysNoneUnknownOrThePersonAndNeverConfusesThem(t *testing.T) {
	s, _ := pgStore(t)
	ctx := context.Background()
	cred := fresh(t, s, "attrib")

	// Nothing open: a complete answer, not a gap.
	att, err := s.ActorFor(ctx, cred)
	require.NoError(t, err)
	require.Equal(t, store.ActedForNone, att.ActedFor)
	require.Empty(t, att.RunID, "no run means no provenance id, and acted_for already said so")

	// One run, triggered by a person.
	runA, err := s.OpenRun(ctx, cred, "slack:U0CIPERSON", "inbound:slack-events", "Ev1", "", time.Minute)
	require.NoError(t, err)
	att, err = s.ActorFor(ctx, cred)
	require.NoError(t, err)
	require.Equal(t, "slack:U0CIPERSON", att.ActedFor)
	require.Equal(t, runA, att.RunID)

	// Two runs at once: the plane refuses to guess which person a call
	// belongs to.
	runB, err := s.OpenRun(ctx, cred, "slack:U0OTHER", "inbound:slack-events", "Ev2", "", time.Minute)
	require.NoError(t, err)
	att, err = s.ActorFor(ctx, cred)
	require.NoError(t, err)
	require.Equal(t, store.ActedForUnknown, att.ActedFor, "overlapping runs are a LOST attribution, not an absent one")
	require.Empty(t, att.RunID)

	// Close one and the other is nameable again.
	require.NoError(t, s.CloseRun(ctx, runB))
	att, err = s.ActorFor(ctx, cred)
	require.NoError(t, err)
	require.Equal(t, "slack:U0CIPERSON", att.ActedFor)

	// Close the last and we are back to "there is no person".
	require.NoError(t, s.CloseRun(ctx, runA))
	att, err = s.ActorFor(ctx, cred)
	require.NoError(t, err)
	require.Equal(t, store.ActedForNone, att.ActedFor)
}

// A run a crashed replica never closed must not poison every later call
// for that credential: past its expiry it stops counting.
func TestAttributionIgnoresARunThatWasNeverClosed(t *testing.T) {
	s, _ := pgStore(t)
	ctx := context.Background()
	cred := fresh(t, s, "attrib-stale")

	_, err := s.OpenRun(ctx, cred, "slack:U0GHOST", "inbound:demo", "Ev9", "", -time.Second)
	require.NoError(t, err)
	att, err := s.ActorFor(ctx, cred)
	require.NoError(t, err)
	require.Equal(t, store.ActedForNone, att.ActedFor,
		"an expired open run stops counting, exactly as a spend reservation does")
}

// A webhook the plane authenticated but that names no human is 'none' —
// a complete answer — and it must not read as a lost attribution.
func TestARunWithNoPersonIsValidAndDistinguishableFromALostOne(t *testing.T) {
	s, _ := pgStore(t)
	ctx := context.Background()
	cred := fresh(t, s, "attrib-nobody")

	run, err := s.OpenRun(ctx, cred, store.ActedForNone, "inbound:demo-bearer", "d-1", "", time.Minute)
	require.NoError(t, err)
	att, err := s.ActorFor(ctx, cred)
	require.NoError(t, err)
	require.Equal(t, store.ActedForNone, att.ActedFor)
	require.Equal(t, run, att.RunID, "a run with no person is still a run, and still provenance")

	require.NoError(t, s.RecordLedger(ctx, store.LedgerEntry{
		CredentialName: cred, Upstream: "ollama", Model: "qwen2.5:3b",
		CostSource: "free", Status: 200, ActedFor: att.ActedFor, RunID: att.RunID}, ""))
	rows, err := s.Ledger(ctx, cred, 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, store.ActedForNone, rows[0].ActedFor)
	require.Equal(t, run, rows[0].RunID)
}

// The identity reaches BOTH trails, and a writer that resolved nothing
// gets 'unknown' rather than a false claim that nobody was there.
func TestTheLedgerAndTheToolAuditBothCarryWhoTheCallWasFor(t *testing.T) {
	s, _ := pgStore(t)
	ctx := context.Background()
	cred := fresh(t, s, "attrib-both")

	run, err := s.OpenRun(ctx, cred, "slack:U0CIPERSON", "inbound:slack-events", "Ev7", "", time.Minute)
	require.NoError(t, err)
	att, err := s.ActorFor(ctx, cred)
	require.NoError(t, err)

	require.NoError(t, s.RecordLedger(ctx, store.LedgerEntry{
		CredentialName: cred, Upstream: "ollama", Model: "qwen2.5:3b",
		CostSource: "free", Status: 200, ActedFor: att.ActedFor, RunID: att.RunID}, ""))
	require.NoError(t, s.RecordToolAudit(ctx, store.ToolAuditEntry{
		CredentialName: cred, Upstream: "kagent-tools", Method: "tools/call",
		Tool: "k8s_get_resources", Decision: "allowed", Status: 200,
		ActedFor: att.ActedFor, RunID: att.RunID}))
	// A writer that stamped nothing: recorded as unresolved, never as
	// "nobody".
	require.NoError(t, s.RecordToolAudit(ctx, store.ToolAuditEntry{
		CredentialName: cred, Upstream: "kagent-tools", Method: "tools/list",
		Decision: "allowed", Status: 200}))

	ledger, err := s.Ledger(ctx, cred, 10)
	require.NoError(t, err)
	require.Len(t, ledger, 1)
	require.Equal(t, "slack:U0CIPERSON", ledger[0].ActedFor)
	require.Equal(t, run, ledger[0].RunID)

	audit, err := s.ToolAudit(ctx, cred, 10)
	require.NoError(t, err)
	require.Len(t, audit, 2)
	byMethod := map[string]store.ToolAuditEntry{}
	for _, e := range audit {
		byMethod[e.Method] = e
	}
	require.Equal(t, "slack:U0CIPERSON", byMethod["tools/call"].ActedFor)
	require.Equal(t, run, byMethod["tools/call"].RunID)
	require.Equal(t, store.ActedForUnknown, byMethod["tools/list"].ActedFor)
	require.Empty(t, byMethod["tools/list"].RunID)

	// And the row points back at the delivery that caused it.
	got, err := s.RunByID(ctx, run)
	require.NoError(t, err)
	require.Equal(t, "inbound:slack-events", got.Source)
	require.Equal(t, "Ev7", got.DeliveryID)
}

// An expired credential is still a credential: it resolves, it says
// when it expired, it is listed, and renewing it brings it back without
// touching the token.
func TestExpiredCredentialResolvesRenewsAndIsListed(t *testing.T) {
	s, _ := pgStore(t)
	ctx := context.Background()
	name := fmt.Sprintf("expired-%d", time.Now().UnixNano())
	h := sha256.Sum256([]byte(name))
	past := time.Now().Add(-time.Hour).UTC()
	require.NoError(t, s.CreateCredential(ctx, name, h[:], past))

	cred, err := s.CredentialByTokenHash(ctx, h[:])
	require.NoError(t, err)
	require.True(t, cred.Expired(time.Now()), "the lookup must still RESOLVE it — an operator told 'unknown token' hunts the wrong problem")
	require.Contains(t, store.ExpiredMessage(cred), "kmx credential renew "+name+" --ttl 720h",
		"the refusal has to name the fix, not just the fault")

	listed, err := s.ListCredentials(ctx)
	require.NoError(t, err)
	var found *store.Credential
	for i := range listed {
		if listed[i].Name == name {
			found = &listed[i]
		}
	}
	require.NotNil(t, found, "an expired credential must stay visible; invisible is how it stays unfixed")
	require.True(t, found.Expired(time.Now()))

	require.NoError(t, s.RenewCredential(ctx, name, time.Now().Add(24*time.Hour)))
	cred, err = s.CredentialByTokenHash(ctx, h[:])
	require.NoError(t, err, "renewal must not move the token: the same hash still resolves")
	require.False(t, cred.Expired(time.Now()))
	require.True(t, cred.ExpiringSoon(time.Now(), store.ExpiryWarning),
		"a day left is inside the warning window an operator watches")

	require.ErrorIs(t, s.RenewCredential(ctx, name+"-nope", time.Now().Add(time.Hour)), store.ErrNotFound)
}

// A credential issued before expiry existed carries NULL and still
// works. The class is closed, not emptied: nothing new can join it.
func TestLegacyCredentialWithNoExpiryStillWorks(t *testing.T) {
	s, pool := pgStore(t)
	ctx := context.Background()
	name := fmt.Sprintf("legacy-%d", time.Now().UnixNano())
	h := sha256.Sum256([]byte(name))
	require.NoError(t, s.CreateCredential(ctx, name, h[:], time.Now().Add(time.Hour)))
	// Exactly what migration 00010 leaves behind for a row that
	// predates it.
	_, err := pool.Exec(ctx, `UPDATE credential SET expires_at = NULL WHERE name = $1`, name)
	require.NoError(t, err)

	cred, err := s.CredentialByTokenHash(ctx, h[:])
	require.NoError(t, err)
	require.Nil(t, cred.ExpiresAt)
	require.False(t, cred.Expired(time.Now()), "a legacy credential still authenticates — expiring the estate at migration time is an outage, not a control")
	require.False(t, cred.ExpiringSoon(time.Now(), store.ExpiryWarning))

	deadlines, err := s.CredentialDeadlines(ctx, time.Now())
	require.NoError(t, err)
	var legacy bool
	for _, d := range deadlines {
		if d.Credential == name {
			legacy = d.Legacy
		}
	}
	require.True(t, legacy, "the no-expiry class must be COUNTABLE, not hidden behind a large number")
}

// The schema itself refuses an actor outside the closed vocabulary, so
// no future writer can smuggle a name, an email or free text into a
// table that is in every pg_dump.
func TestTheActorVocabularyIsClosedInTheSchema(t *testing.T) {
	s, pool := pgStore(t)
	ctx := context.Background()
	cred := fresh(t, s, "attrib-check")

	_, err := pool.Exec(ctx,
		`INSERT INTO ledger_entry (credential_name, upstream, model, cost_source, status, acted_for)
		 VALUES ($1, 'ollama', 'qwen2.5:3b', 'free', 200, $2)`, cred, "alice@example.com")
	require.Error(t, err, "an address is not an identifier from the vocabulary")

	_, err = pool.Exec(ctx,
		`INSERT INTO agent_run (credential_name, acted_for, source, expires_at)
		 VALUES ($1, 'unknown', 'inbound:demo', now() + interval '1 minute')`, cred)
	require.Error(t, err, "a run always knows which of the two it is; 'unknown' is a resolution, never a claim")

	_ = s
}
