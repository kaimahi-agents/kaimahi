package app

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

const (
	before = "2026-09-06T10:00:00Z"
	after  = "2026-09-06T10:10:00Z"
)

func written(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// The finding: a credential is written, kagent's cached verdict still says
// Accepted from before the write, and the operator is told it worked. Five
// minutes of "it worked" is worse than an error, because they move on.
func TestAVerdictReachedBeforeTheCredentialIsUnknownNotAccepted(t *testing.T) {
	v := classifySeamVerdict(&serverCondition{
		Type: "Accepted", Status: "True", LastTransitionTime: before,
	}, 0, 0, written("2026-09-06T10:05:00Z"))

	if v.State != verdictUnknown {
		t.Fatalf("a stale pass was reported as %q, not unknown", v.State)
	}
	for _, want := range []string{"before the credential was written", "has not looked since", "5m0s"} {
		if !strings.Contains(v.Reason, want) {
			t.Errorf("the reason does not say %q:\n%s", want, v.Reason)
		}
	}
	// `unknown` must not be mistaken for a rejection either: nothing is
	// known to be wrong, and telling an operator their credential failed
	// would send them to fix something that may be fine.
	if strings.Contains(v.Line(time.Now()), "rejected") {
		t.Errorf("cannot-tell was rendered as a rejection: %s", v.Line(time.Now()))
	}
}

// The other side of the window, and the reading the operator is entitled to
// once kagent has actually looked.
func TestAVerdictReachedAfterTheCredentialIsUsed(t *testing.T) {
	since := written("2026-09-06T10:05:00Z")

	pass := classifySeamVerdict(&serverCondition{
		Type: "Accepted", Status: "True", LastTransitionTime: after,
	}, 0, 0, since)
	if pass.State != verdictAccepted {
		t.Errorf("a fresh pass was reported as %q: %s", pass.State, pass.Reason)
	}

	fail := classifySeamVerdict(&serverCondition{
		Type: "Accepted", Status: "False", Message: "Unauthorized", LastTransitionTime: after,
	}, 0, 0, since)
	if fail.State != verdictRejected {
		t.Fatalf("a fresh rejection was reported as %q", fail.State)
	}
	// kagent's own words survive: an expired token and an unreachable host
	// are different problems and only the upstream can tell them apart.
	if !strings.Contains(fail.Message, "Unauthorized") {
		t.Errorf("the rejection lost what the upstream said: %q", fail.Message)
	}
}

// A caller that changed nothing has no write to compare against and must not
// invent one — this is what `kmx status` asks, and it has to keep working.
func TestWithNoWriteToCompareAgainstTheVerdictIsReportedAsItStands(t *testing.T) {
	v := classifySeamVerdict(&serverCondition{
		Type: "Accepted", Status: "True", LastTransitionTime: before,
	}, 0, 0, time.Time{})
	if v.State != verdictAccepted {
		t.Fatalf("status was denied an answer kagent had actually given: %q (%s)", v.State, v.Reason)
	}
	if v.At.IsZero() {
		t.Error("the verdict lost its time, which is what lets a reader judge it")
	}
}

// Absent and mid-reconcile are both `unknown`, and neither is `none`: kagent
// has not said no, it has not said anything.
func TestNoVerdictIsUnknownRatherThanAFailure(t *testing.T) {
	absent := classifySeamVerdict(nil, 0, 0, time.Time{})
	if absent.State != verdictUnknown || !strings.Contains(absent.Reason, "no verdict") {
		t.Errorf("an absent condition: %+v", absent)
	}

	reconciling := classifySeamVerdict(&serverCondition{
		Type: "Accepted", Status: "True", LastTransitionTime: after,
	}, 4, 3, time.Time{})
	if reconciling.State != verdictUnknown || !strings.Contains(reconciling.Reason, "still reconciling") {
		t.Errorf("a verdict from an older generation: %+v", reconciling)
	}
}

// A condition with no time cannot be told apart from one reached before the
// write, so it cannot answer a question about the write.
func TestAnUndatedVerdictCannotAnswerAQuestionAboutAWrite(t *testing.T) {
	v := classifySeamVerdict(&serverCondition{Type: "Accepted", Status: "True"},
		0, 0, written("2026-09-06T10:05:00Z"))
	if v.State != verdictUnknown || !strings.Contains(v.Reason, "no timestamp") {
		t.Errorf("an undated verdict was trusted: %+v", v)
	}
}

func TestPickConditionFindsTheOneAsked(t *testing.T) {
	conditions := []serverCondition{
		{Type: "Ready", Status: "True"},
		{Type: "Accepted", Status: "False", Message: "nope"},
	}
	if got := pickCondition(conditions, "Accepted"); got == nil || got.Message != "nope" {
		t.Errorf("pickCondition: %+v", got)
	}
	if got := pickCondition(conditions, "Missing"); got != nil {
		t.Errorf("pickCondition invented a condition: %+v", got)
	}
}

// The wait after a credential is written must not be satisfied by a verdict
// from before it. `kubectl wait --for=...Accepted=True` was, and returned
// instantly on a cached pass, which is how a credential that could not be
// used reported as working.
func TestTheWaitIsNotSatisfiedByAVerdictFromBeforeTheWrite(t *testing.T) {
	since := written("2026-09-06T10:05:00Z")
	clock := since
	reads, rechecks, slept := 0, 0, time.Duration(0)

	v, err := waitForVerdict(
		func() (seamVerdict, error) {
			reads++
			// Always the same stale pass: kagent never looks again.
			return classifySeamVerdict(&serverCondition{
				Type: "Accepted", Status: "True", LastTransitionTime: before,
			}, 0, 0, since), nil
		},
		func() time.Time { return clock },
		func() { rechecks++ },
		func(d time.Duration) { slept += d; clock = clock.Add(d) },
	)
	if err != nil {
		t.Fatal(err)
	}
	if v.State != verdictUnknown {
		t.Fatalf("a stale pass satisfied the wait as %q", v.State)
	}
	if !strings.Contains(v.Reason, "is not known") || !strings.Contains(v.Reason, "not known to be broken") {
		t.Errorf("the timeout does not report cannot-tell honestly:\n%s", v.Reason)
	}
	// The timeout must say WHY it cannot tell, or it reads as kmx giving up.
	// A condition records a transition, not a check, so an unchanged pass is
	// indistinguishable from a stale one however long anyone waits.
	if !strings.Contains(v.Reason, "CHANGED") {
		t.Errorf("the timeout does not say why waiting longer would not help:\n%s", v.Reason)
	}
	if reads < 2 {
		t.Errorf("the wait did not actually poll (%d reads)", reads)
	}
	// kagent does not retry a cached verdict on its own, and it is asked
	// once rather than on every turn of the loop.
	if rechecks != 1 {
		t.Errorf("kagent was asked to look again %d times, want exactly 1", rechecks)
	}
	if slept < seamRecheckWait {
		t.Errorf("the wait gave up after %s, before the projection could refresh", slept)
	}
}

// And it returns as soon as kagent does look, with what kagent found.
func TestTheWaitReturnsTheVerdictKagentReachesAfterTheWrite(t *testing.T) {
	since := written("2026-09-06T10:05:00Z")
	clock := since
	reads := 0

	v, err := waitForVerdict(
		func() (seamVerdict, error) {
			reads++
			c := &serverCondition{Type: "Accepted", Status: "True", LastTransitionTime: before}
			if reads >= 3 {
				c = &serverCondition{Type: "Accepted", Status: "False", Message: "Unauthorized",
					LastTransitionTime: after}
			}
			return classifySeamVerdict(c, 0, 0, since), nil
		},
		func() time.Time { return clock },
		func() {},
		func(d time.Duration) { clock = clock.Add(d) },
	)
	if err != nil {
		t.Fatal(err)
	}
	if v.State != verdictRejected || !strings.Contains(v.Message, "Unauthorized") {
		t.Fatalf("the rejection kagent actually reached was not reported: %+v", v)
	}
}

// The lag against a real cluster, rather than a fixture that returns what the
// test expects.
//
// Skipped unless pointed at a live seam. The variables are the seam to read
// and the moment its credential was written, so the classifier is asked the
// same question `kmx tools govern` asks it: is this verdict about the
// credential that is there now?
func TestAgainstARealSeam(t *testing.T) {
	server := os.Getenv("KAIMAHI_SEAM")
	since := os.Getenv("KAIMAHI_SEAM_CREDENTIAL_WRITTEN_AT")
	if server == "" || since == "" {
		t.Skip("no live seam; set KAIMAHI_SEAM and KAIMAHI_SEAM_CREDENTIAL_WRITTEN_AT")
	}
	writtenAt, err := time.Parse(time.RFC3339, since)
	if err != nil {
		t.Fatalf("KAIMAHI_SEAM_CREDENTIAL_WRITTEN_AT: %v", err)
	}
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	a := New(cfg)
	a.Run.Echo = false

	v, err := a.readSeamVerdict("kagent", server, writtenAt)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("credential written at %s", writtenAt.Format(time.RFC3339))
	t.Logf("kmx reports: %s", v.Line(time.Now()))
	if v.State == verdictAccepted {
		t.Errorf("a verdict reached at %s was reported as an answer about a credential written at %s",
			v.At.Format(time.RFC3339), writtenAt.Format(time.RFC3339))
	}
}
