package inbound

// The inbound door is where a PERSON is most visible — a Slack mention
// means a human triggered the run — and where the attribution gap was
// widest. These are the proofs that the person reaches the run (and
// through it the ledger and the tool audit), that a source naming
// nobody is valid and says so, and that an expired credential is
// refused at the door.

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/kaimahi-agents/kaimahi/plane/internal/config"
	"github.com/kaimahi-agents/kaimahi/plane/internal/store"
)

func TestASlackMentionNamesThePersonOnTheRunAndTheTrail(t *testing.T) {
	fs := newFakeStore()
	f := newSlackFixture(t, fs)

	require.Equal(t, http.StatusAccepted,
		f.post("slack-chan", mentionInAllowed, f.slackSigned(mentionInAllowed)).Code)
	f.waitOutcome(t, "slack-chan")

	fs.mu.Lock()
	defer fs.mu.Unlock()
	require.Len(t, fs.runs, 1, "an admitted mention opens exactly one run")
	run := fs.runs[0]
	// Reused rather than reinvented: the same shape `decided_by` uses
	// for an approver.
	require.Equal(t, "slack:U2", run.ActedFor)
	require.Equal(t, "inbound:slack-chan", run.Source)
	require.Equal(t, "Ev0000000001", run.DeliveryID)
	// The run is against the credential that SPENDS (the agent's), not
	// the hook's — that is the one the proxy and the gateway see.
	require.Equal(t, "hello-world", run.CredentialName)
	require.Equal(t, []string{"run-Ev0000000001"}, fs.closedRuns,
		"the run is closed when the turn ends, so it stops attributing later calls")

	for _, e := range fs.audits {
		if e.Hook == "slack-chan" && (e.Decision == "admitted" || e.Decision == "completed") {
			require.Equal(t, "slack:U2", e.ActedFor, "the inbound trail names them too")
		}
	}
}

func TestAHookThatNamesNobodyIsValidAndSaysSo(t *testing.T) {
	fs := newFakeStore()
	f := newFixture(t, fs, &fakeMeter{})

	require.Equal(t, http.StatusAccepted, f.post("demo", `{"text":"hi"}`, bearer("d-nobody")).Code)
	f.waitOutcome(t, "demo")

	require.Equal(t, store.ActedForNone, fs.last("demo").ActedFor)
	fs.mu.Lock()
	defer fs.mu.Unlock()
	require.Len(t, fs.runs, 1)
	require.Equal(t, store.ActedForNone, fs.runs[0].ActedFor,
		"a signed or bearer webhook has a sender the plane authenticated but no human to name — 'none' is a complete answer")
}

// A refusal the plane makes before it has verified anything cannot claim
// there was nobody: it says it cannot tell.
func TestARefusalBeforeAuthenticationSaysUnknownNotNone(t *testing.T) {
	fs := newFakeStore()
	f := newFixture(t, fs, &fakeMeter{})

	require.Equal(t, http.StatusUnauthorized, f.post("demo", `{"text":"hi"}`, nil).Code)
	require.Equal(t, store.ActedForUnknown, f.fs.last("demo").ActedFor,
		"an unverified blob names nobody, but the plane has not EARNED the claim that no person sent it")
}

func TestExpiredHookCredentialIsRefusedAtTheDoor(t *testing.T) {
	fs := newFakeStore()
	past := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	fs.addCredential(callerToken, store.Credential{Name: "inbound-demo", ExpiresAt: &past})
	f := newFixture(t, fs, &fakeMeter{})

	rec := f.post("demo", `{"text":"hi"}`, bearer("d-expired"))
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), `expired credential "inbound-demo"`)
	require.Contains(t, rec.Body.String(), "kmx credential renew inbound-demo --ttl 720h")
	require.Equal(t, "denied", f.fs.last("demo").Decision)
	require.Equal(t, 0, f.a2a.count(), "nothing runs on an expired credential")
}

func TestExpiredTargetCredentialRefusesBeforeAGrantIsBurned(t *testing.T) {
	fs := newFakeStore()
	past := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	fs.byName["hello-world"] = store.Credential{Name: "hello-world", ExpiresAt: &past}
	f := newFixture(t, fs, &fakeMeter{})

	before := fs.grants["inbound-demo/demo"]
	rec := f.post("demo", `{"text":"hi"}`, bearer("d-target"))
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "target expired credential \"hello-world\"")
	require.Equal(t, before, fs.grants["inbound-demo/demo"],
		"refusing at the door costs no grant use — the alternative is admitting an event that could never spend")
	require.Equal(t, 0, f.a2a.count())
}

// An event whose run cannot be recorded is not honoured: the same rule
// as an event whose audit row cannot be written.
func TestAnEventWhoseRunCannotBeOpenedIsNotInvoked(t *testing.T) {
	fs := newFakeStore()
	fs.openRunErr = errors.New("pg down")
	f := newFixture(t, fs, &fakeMeter{})

	require.Equal(t, http.StatusAccepted, f.post("demo", `{"text":"hi"}`, bearer("d-norun")).Code)
	out := f.waitOutcome(t, "demo")
	require.Equal(t, "failed", out.Decision)
	require.Contains(t, out.Detail, "attribution unavailable")
	require.Equal(t, 0, f.a2a.count(), "the agent is never reached")
}

var _ = config.AuthSlack
