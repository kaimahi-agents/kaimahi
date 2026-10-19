package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Keep the real hard link, writes and validation. Only the directory descriptor
// is closed: publication links 4.json, then its actual directory Sync fails.
func failReleaseAckDirectorySync(t *testing.T, a *App) *bool {
	t.Helper()
	fired := false
	a.releaseRecordPrepared = func(w *releaseLiftRecordWriter) {
		if len(w.outputs) != 5 {
			t.Fatal("fixture needs ownership ACK slot")
		}
		output := w.outputs[4]
		output.beforeLink = func() error { fired = true; return output.directory.Close() }
	}
	return &fired
}

func TestReleaseUpdatePostLinkAckFailureBlocksNextPrepare(t *testing.T) {
	a, reads, old, next, _, api := releaseUpdateFixture(t)
	fired := failReleaseAckDirectorySync(t, a)
	p, err := a.prepareReleaseUpdate(t.Context(), next)
	if err != nil {
		t.Fatal(err)
	}
	r, err := a.applyReleaseUpdate(t.Context(), p, p.lift.summary.PlanDigest)
	if !*fired || !errors.Is(err, errReleaseLiftProvenance) || r.State != "partial" || r.ProvenanceState != "unknown" || r.placement != nil {
		t.Fatal("post-link ACK failure not reported honestly", r.State, err)
	}
	assertVisibleAckCandidate(t, r.LiftID)
	state, err := a.readReleaseState(t.Context(), releaseStateSubject(p.lift.native.Subject))
	if err != nil || state == nil || state.State.Operation != "" {
		t.Fatal("fixture did not reach released shared-state boundary", err)
	}
	writes, patches := reads.records.writes, api.patches
	if _, err := a.prepareReleaseUpdate(t.Context(), old); !errors.Is(err, errReleaseState) {
		t.Fatal("post-link failed ACK authorized next update", err)
	}
	if reads.records.writes != writes || api.patches != patches {
		t.Fatal("refusal changed shared/native state")
	}
}

func TestReleaseCreatePostLinkAckFailureBlocksNextPrepare(t *testing.T) {
	a, reads, opt, dir := releaseApplyFixture(t, "both")
	// Update preparation requires a complete bounded dependent inventory even for
	// a same-digest request. Supply the actual created canonical Agent document.
	prior := a.captureCommand
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		if slices.Contains(cmd.Args, "--raw") && strings.Contains(strings.Join(cmd.Args, " "), "/agents?limit=256") {
			raw, err := os.ReadFile(filepath.Join(dir, "agents.core.orka.ai.json"))
			if err != nil {
				return err
			}
			var doc map[string]any
			if err := json.Unmarshal(raw, &doc); err != nil {
				return err
			}
			return json.NewEncoder(cmd.Stdout).Encode(map[string]any{"apiVersion": "core.orka.ai/v1alpha1", "kind": "AgentList", "metadata": map[string]any{}, "items": []any{doc}})
		}
		return prior(ctx, cmd)
	}
	fired := failReleaseAckDirectorySync(t, a)
	p, err := a.PrepareReleaseLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	r, err := a.ApplyReleaseLift(t.Context(), p, p.Summary().PlanDigest)
	if !*fired || !errors.Is(err, errReleaseLiftProvenance) || r.State != "partial" || r.ProvenanceState != "unknown" || r.placement != nil {
		t.Fatal("post-link create ACK failure not reported honestly", r.State, err)
	}
	assertVisibleAckCandidate(t, r.LiftID)
	state, err := a.readReleaseState(t.Context(), releaseStateSubject(p.native.Subject))
	if err != nil || state == nil || state.State.Operation != "" {
		t.Fatal("fixture did not reach released shared-state boundary", err)
	}
	writes := reads.records.writes
	if _, err := a.prepareReleaseUpdate(t.Context(), opt); !errors.Is(err, errReleaseState) {
		t.Fatal("post-link failed fresh-create ACK authorized next update", err)
	}
	if reads.records.writes != writes {
		t.Fatal("refusal rewrote shared state")
	}
}

func TestReleaseOwnershipRequiresCommittedCandidate(t *testing.T) {
	for _, failure := range []string{"unmarked", "missing candidate", "malformed marker", "different bytes", "public marker", "oversized marker"} {
		t.Run(failure, func(t *testing.T) {
			a, _, old, _, _, _ := releaseUpdateFixture(t)
			prepared, err := a.prepareReleaseUpdate(t.Context(), old)
			if err != nil {
				t.Fatal(err)
			}
			id := prepared.origin.ID
			parent := filepath.Join(os.Getenv("KMX_HOME"), "release-lifts", id)
			candidate, marker := filepath.Join(parent, "4.json"), filepath.Join(parent, "committed.json")
			raw, err := os.ReadFile(candidate)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			switch failure {
			case "unmarked":
			case "missing candidate":
				if err := os.WriteFile(marker, raw, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(candidate); err != nil {
					t.Fatal(err)
				}
			case "malformed marker":
				if err := os.WriteFile(marker, []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			case "different bytes":
				if err := os.WriteFile(marker, append(raw, '\n'), 0600); err != nil {
					t.Fatal(err)
				}
			case "public marker":
				if err := os.WriteFile(marker, raw, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(marker, 0644); err != nil {
					t.Fatal(err)
				}
			case "oversized marker":
				if err := os.WriteFile(marker, bytes.Repeat([]byte("x"), maxReleaseLiftRecordBytes+1), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := readReleaseOwnership(t.Context(), id); !errors.Is(err, errReleaseState) {
				t.Fatal("uncommitted or mismatched candidate accepted", err)
			}
			if _, err := a.prepareReleaseUpdate(t.Context(), old); !errors.Is(err, errReleaseState) {
				t.Fatal("next prepare accepted uncommitted authority", err)
			}
		})
	}
}

func TestReleaseUpdateWriterCloseFailureDoesNotCommit(t *testing.T) {
	a, _, old, next, _, _ := releaseUpdateFixture(t)
	fired := false
	a.releaseRecordPrepared = func(w *releaseLiftRecordWriter) {
		w.outputs[4].beforeLink = func() error { fired = true; return w.outputs[0].directory.Close() }
	}
	p, err := a.prepareReleaseUpdate(t.Context(), next)
	if err != nil {
		t.Fatal(err)
	}
	result, err := a.applyReleaseUpdate(t.Context(), p, p.lift.summary.PlanDigest)
	if !fired || !errors.Is(err, errReleaseLiftProvenance) || result.State != "partial" || result.ProvenanceState != "unknown" {
		t.Fatal("writer Close failure lost uncertainty", result.State, result.ProvenanceState, err)
	}
	assertVisibleAckCandidate(t, result.LiftID)
	if _, err := a.prepareReleaseUpdate(t.Context(), old); !errors.Is(err, errReleaseState) {
		t.Fatal("writer Close failure committed ownership", err)
	}
}

func TestReleaseAckCommitBoundary(t *testing.T) {
	for _, boundary := range []string{"success", "link refused", "committed response lost", "post-commit cleanup"} {
		t.Run(boundary, func(t *testing.T) {
			a, _, old, next, _, _ := releaseUpdateFixture(t)
			fired := false
			a.releaseRecordPrepared = func(w *releaseLiftRecordWriter) {
				w.commitLink = func(root *os.Root, oldName, newName string) error {
					fired = true
					assertVisibleAckCandidate(t, w.id)
					if _, err := readReleaseOwnership(t.Context(), w.id); !errors.Is(err, errReleaseState) {
						t.Fatal("candidate visible before commit was eligible", err)
					}
					if extra, err := openReleaseLiftStorage(t.Context()); err == nil {
						extra.Close()
						t.Fatal("capacity lock released before commit")
					}
					if boundary == "link refused" {
						return os.ErrPermission
					}
					if err := root.Link(oldName, newName); err != nil {
						return err
					}
					if boundary == "committed response lost" {
						return os.ErrExist
					}
					if boundary == "post-commit cleanup" {
						if err := root.Close(); err != nil {
							t.Fatal(err)
						}
						if err := w.lock.Close(); err != nil {
							t.Fatal(err)
						}
					}
					return nil
				}
			}
			p, err := a.prepareReleaseUpdate(t.Context(), next)
			if err != nil {
				t.Fatal(err)
			}
			result, err := a.applyReleaseUpdate(t.Context(), p, p.lift.summary.PlanDigest)
			if !fired {
				t.Fatal("final marker boundary not exercised")
			}
			if boundary == "link refused" {
				if !errors.Is(err, errReleaseLiftProvenance) || result.State != "partial" || result.ProvenanceState != "unknown" || result.placement != nil {
					t.Fatal("commit refusal lost uncertainty", result.State, result.ProvenanceState, err)
				}
				if _, err := a.prepareReleaseUpdate(t.Context(), old); !errors.Is(err, errReleaseState) {
					t.Fatal("refused marker authorized rollback", err)
				}
			} else {
				if err != nil || result.State != "ready" || result.ProvenanceState != "recorded" {
					t.Fatal("committed ACK retroactively became unknown", result.State, result.ProvenanceState, err)
				}
				if _, err := a.prepareReleaseUpdate(t.Context(), old); err != nil {
					t.Fatal("committed ACK did not authorize next prepare", err)
				}
			}
		})
	}
}

func TestReleaseOwnershipCopiedCommitStillMatches(t *testing.T) {
	a, _, old, _, _, _ := releaseUpdateFixture(t)
	p, err := a.prepareReleaseUpdate(t.Context(), old)
	if err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(os.Getenv("KMX_HOME"), "release-lifts", p.origin.ID)
	raw, err := os.ReadFile(filepath.Join(parent, "4.json"))
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(parent, "committed.json")
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.prepareReleaseUpdate(t.Context(), old); err != nil {
		t.Fatal("byte-exact committed backup required hard-link identity", err)
	}
}

func TestReleaseAckCommitErrorsRetainProvenanceCause(t *testing.T) {
	for _, failure := range []string{"store identity", "cancellation"} {
		t.Run(failure, func(t *testing.T) {
			summary, receipt, annotation := recordTestInput(t)
			summary.Runtime = "orka"
			writer, err := preflightReleaseLiftRecord(t.Context(), recordTestID, summary, receipt, annotation)
			if err != nil {
				t.Fatal(err)
			}
			defer writer.Close()
			// Exercise the real commit boundary after a successfully synced candidate;
			// these bytes do not claim ownership or enter an update preparation.
			if err := writer.outputs[4].publishBytes(t.Context(), []byte("{}")); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var cause error
			if failure == "store identity" {
				if err := writer.root.Close(); err != nil {
					t.Fatal(err)
				}
				cause = errReleaseLiftRecord
			} else {
				cancel()
				cause = context.Canceled
			}
			err = writer.commitAcknowledgment(ctx)
			if !errors.Is(err, errReleaseLiftProvenance) || !errors.Is(err, cause) {
				t.Fatal("commit failure lost provenance classification or original cause", err)
			}
			if _, err := os.Stat(recordTestPath("committed.json")); !os.IsNotExist(err) {
				t.Fatal("refused commit left a marker", err)
			}
		})
	}
}

func assertVisibleAckCandidate(t *testing.T, id string) {
	t.Helper()
	path := filepath.Join(os.Getenv("KMX_HOME"), "release-lifts", id, "4.json")
	raw, err := os.ReadFile(path)
	if err != nil || !json.Valid(raw) {
		t.Fatal("failure was not after a complete ACK hard link", err)
	}
}
