package app

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Public dispatch must not turn the backend's failed post-link ACK into a
// usable update plan, including a request for the digest already on the pair.
func TestPublicUpdatePostLinkAcknowledgmentBlocksNextPrepare(t *testing.T) {
	a, reads, old, next, _, api := releaseUpdateFixture(t)
	old.Update, next.Update = true, true
	fired := failReleaseAckDirectorySync(t, a)
	p, err := a.PrepareReleaseLift(t.Context(), next)
	if err != nil {
		t.Fatal(err)
	}
	result, err := a.ApplyReleaseLift(t.Context(), p, p.Summary().PlanDigest)
	if !*fired || !errors.Is(err, errReleaseLiftProvenance) || result.State != "partial" || result.ProvenanceState != "unknown" || result.placement != nil {
		t.Fatal("public dispatch hid failed acknowledgment", err)
	}
	assertVisibleAckCandidate(t, result.LiftID)
	patches, writes := api.patches, reads.records.writes
	for _, opt := range []ReleaseLiftOptions{old, next} {
		if _, err := a.PrepareReleaseLift(t.Context(), opt); !errors.Is(err, errReleaseState) || !errors.Is(err, ErrReleaseLift) {
			t.Fatal("public next prepare accepted uncommitted ownership", err)
		}
	}
	if api.patches != patches || reads.records.writes != writes {
		t.Fatal("refusal changed shared/native state")
	}
}

func TestWorkspaceUpdateRequiresCommitBeforeSpend(t *testing.T) {
	for _, failure := range []string{"failed create acknowledgment", "unmarked baseline"} {
		t.Run(failure, func(t *testing.T) {
			a, opt, state, reads, _, writes := workspaceLiftFixture(t)
			var fired *bool
			if failure == "failed create acknowledgment" {
				fired = failReleaseAckDirectorySync(t, a)
			}
			p, err := PrepareWorkspaceLift(t.Context(), opt)
			if err != nil {
				t.Fatal(err)
			}
			_, err = a.LiftWorkspace(t.Context(), p)
			p.Close()
			if fired != nil {
				if !*fired || err == nil {
					t.Fatal("workspace fixture missed real failed ACK", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			candidates, err := filepath.Glob(filepath.Join(os.Getenv("KMX_HOME"), "release-lifts", "*", "4.json"))
			if err != nil || len(candidates) != 1 {
				t.Fatal("expected one complete visible candidate", err, candidates)
			}
			if failure == "unmarked baseline" {
				// Simulate a pre-marker baseline; no production code may repair it.
				if err := os.Remove(filepath.Join(filepath.Dir(candidates[0]), releaseCommitMarker)); err != nil {
					t.Fatal(err)
				}
			}
			attachWorkspaceUpdateReads(t, a)
			// The source has no evidence for this new digest. Eligibility must refuse
			// before evaluation, publication, native effects or private enrollment.
			path := filepath.Join(opt.Workspace, "agents/greeter/instructions.md")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, append(raw, []byte("\nA new revision.\n")...), 0600); err != nil {
				t.Fatal(err)
			}
			opt.Destination.Update = true
			tasks, records, pushes := len(state.creates), reads.records.writes, *writes
			p, err = PrepareWorkspaceLift(t.Context(), opt)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			r, err := a.LiftWorkspace(t.Context(), p)
			if !errors.Is(err, errReleaseState) || r.Eval.Status != "not-attempted" || len(state.creates) != tasks || reads.records.writes != records || *writes != pushes {
				t.Fatal("uncommitted baseline spent or enrolled", r.Eval.Status, err)
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(candidates[0]), releaseCommitMarker)); !os.IsNotExist(err) {
				t.Fatal("refusal repaired marker", err)
			}
		})
	}
}
