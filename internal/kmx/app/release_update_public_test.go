package app

import (
	"errors"
	"strings"
	"testing"
)

func TestPublicReleaseLiftUpdateAndOriginalEvidenceRollback(t *testing.T) {
	a, reads, old, next, _, api := releaseUpdateFixture(t)
	old.Update, next.Update = true, true
	for _, opt := range []ReleaseLiftOptions{next, old} {
		patches, writes := api.patches, reads.records.writes
		p, err := a.PrepareReleaseLift(t.Context(), opt)
		if err != nil {
			t.Fatal("public update planning refused", err)
		}
		if api.patches != patches || reads.records.writes != writes {
			t.Fatal("update planning wrote shared/native state")
		}
		if p.Summary().Resources[0].Action != "update" {
			t.Fatal("public plan lost update action")
		}
		r, err := a.ApplyReleaseLift(t.Context(), p, p.Summary().PlanDigest)
		if err != nil || r.State != "ready" || r.LiftID == "" || api.patches != patches+2 {
			t.Fatal("public update/rollback incomplete", err)
		}
		if err := a.VerifyReleaseLiftPlacement(t.Context(), r); err != nil {
			t.Fatal("public update returned unusable proof", err)
		}
		if _, err := a.ApplyReleaseLift(t.Context(), p, p.Summary().PlanDigest); err == nil {
			t.Fatal("public update plan replayed")
		}
	}
	before := reads.records.writes
	p, err := a.PrepareReleaseLift(t.Context(), old)
	if err != nil {
		t.Fatal(err)
	}
	if p.Summary().Resources[0].Action != "verify" {
		t.Fatal("same-digest plan advertised mutation")
	}
	if r, err := a.ApplyReleaseLift(t.Context(), p, p.Summary().PlanDigest); err != nil || r.State != "ready" || r.LiftID != "" || api.patches != 4 || reads.records.writes != before {
		t.Fatal("public same-digest update was not no-write", err)
	}
}

func TestPlainReleaseLiftAdviceDistinguishesEligibleOldAndUnreadable(t *testing.T) {
	for _, state := range []string{"eligible", "old", "unreadable"} {
		t.Run(state, func(t *testing.T) {
			a, reads, _, next, _, api := releaseUpdateFixture(t)
			if state == "old" {
				t.Setenv("KMX_HOME", privateSuiteDir(t))
			}
			if state == "unreadable" {
				reads.records.fail = "get"
			}
			_, err := a.PrepareReleaseLift(t.Context(), next)
			if err == nil || api.patches != 0 {
				t.Fatal("plain lift did not refuse", err)
			}
			if state == "eligible" && !strings.Contains(err.Error(), "--update") {
				t.Fatal("eligible placement lost update advice", err)
			}
			if state == "old" && !strings.Contains(err.Error(), "explicitly recreate") {
				t.Fatal("old placement lost no-enrollment advice", err)
			}
			if state == "unreadable" && (strings.Contains(err.Error(), "recreate") || !errors.Is(err, errReleaseStateUnavailable)) {
				t.Fatal("read failure misclassified as unsupported", err)
			}
		})
	}
}

func TestPublicReleaseLiftUpdateNeverEnrollsUnrecorded(t *testing.T) {
	a, reads, _, next, _, api := releaseUpdateFixture(t)
	next.Update = true
	t.Setenv("KMX_HOME", privateSuiteDir(t))
	before := reads.records.writes
	if _, err := a.PrepareReleaseLift(t.Context(), next); (!errors.Is(err, errReleaseUpdateUnsupported) || !errors.Is(err, ErrReleaseLift)) || api.patches != 0 || reads.records.writes != before {
		t.Fatal("public update backfilled old observations", err)
	}
}
