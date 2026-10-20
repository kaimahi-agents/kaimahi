package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
)

func attachWorkspaceUpdateReads(t *testing.T, a *App) {
	t.Helper()
	prior := a.captureCommand
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		if slices.Contains(cmd.Args, "--raw") && strings.Contains(strings.Join(cmd.Args, " "), "/agents?limit=256") {
			raw, err := os.ReadFile(filepath.Join(os.Getenv("KMX_RECONCILE_DIR"), "agents.core.orka.ai.json"))
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
}

func TestWorkspaceLiftUpdateRefusesUnrecordedBeforeSpend(t *testing.T) {
	a, opt, state, _, _, writes := workspaceLiftFixture(t)
	opt.Destination.Update = true
	p, err := PrepareWorkspaceLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if _, err := a.LiftWorkspace(t.Context(), p); err == nil || len(state.creates) != 0 || *writes != 0 {
		t.Fatal("unrecorded update spent evaluation/publication or created", err, len(state.creates), *writes)
	}
}

func TestWorkspaceLiftUpdateNoopReprovesBaselineWithoutSpend(t *testing.T) {
	a, opt, state, reads, _, writes := workspaceLiftFixture(t)
	p, err := PrepareWorkspaceLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.LiftWorkspace(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	p.Close()
	beforeTasks, beforeWrites := len(state.creates), reads.records.writes
	opt.Destination.Update = true
	nativeMetadata(reads.objects["providers.core.orka.ai/inference"])["resourceVersion"] = "new-status-version"
	attachWorkspaceUpdateReads(t, a)
	p, err = PrepareWorkspaceLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	r, err := a.LiftWorkspace(t.Context(), p)
	if err != nil || r.Lift.Status != "skipped" || len(state.creates) != beforeTasks || reads.records.writes != beforeWrites || *writes != 0 {
		t.Fatal("RV-only update no-op used stale completion or spent", r.Lift.Status, err, len(state.creates), reads.records.writes)
	}
}

func TestWorkspaceUpdateNoopRechecksFrozenSource(t *testing.T) {
	a, opt, _, reads, _, _ := workspaceLiftFixture(t)
	p, err := PrepareWorkspaceLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.LiftWorkspace(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	p.Close()
	attachWorkspaceUpdateReads(t, a)
	opt.Destination.Update = true
	p, err = PrepareWorkspaceLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	prior := a.captureCommand
	changed := false
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		if !changed && slices.Contains(cmd.Args, "get") && slices.Contains(cmd.Args, "configmap") {
			changed = true
			path := filepath.Join(opt.Workspace, "agents/greeter/instructions.md")
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if err := os.WriteFile(path, append(raw, []byte("\nChanged during update inspection.\n")...), 0600); err != nil {
				return err
			}
		}
		return prior(ctx, cmd)
	}
	writes := reads.records.writes
	result, err := a.LiftWorkspace(t.Context(), p)
	if !changed || err == nil || result.Lift.Status == "skipped" || reads.records.writes != writes {
		t.Fatal("no-op credited a changed workspace", result.Lift.Status, err)
	}
}

func TestWorkspaceUpdateSharedCapacityBeforeSpend(t *testing.T) {
	a, opt, tasks, reads, _, pushes := workspaceLiftFixture(t)
	p, err := PrepareWorkspaceLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.LiftWorkspace(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	p.Close()
	api := attachReleaseUpdateAPI(t, a, os.Getenv("KMX_RECONCILE_DIR"))
	attachWorkspaceUpdateReads(t, a)
	native, err := a.prepareSuiteNative(t.Context(), p.review.suite, opt.Destination.ToNamespace, "inference")
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := a.inspectReleaseUpdateBaseline(t.Context(), native, opt.Destination.Inference)
	if err != nil {
		t.Fatal(err)
	}
	// Model protected historical partials at the external shared-state boundary.
	// Retain the real current pair/receipt/dependencies and publish its matching
	// private acknowledgment through the actual writer, not hand-made markers.
	receipt, err := baseline.retainedReleaseEvidence(t.Context(), p.plan.SuiteDigest)
	if err != nil {
		t.Fatal(err)
	}
	// Obtain both IDs from separate normal provenance operations. Do not encode
	// or mutate a literal ID: the prospective operation must differ from the
	// current revision while preserving the production record/receipt path.
	lift := &PreparedReleaseLift{native: native, receipt: receipt, summary: baseline.latest.Summary, options: opt.Destination}
	docs, err := lift.releaseDocuments()
	if err != nil {
		t.Fatal(err)
	}
	probe, pendingID, err := a.prepareReleaseLiftProvenance(t.Context(), lift, docs)
	if err != nil {
		t.Fatal(err)
	}
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	writer, currentID, err := a.prepareReleaseLiftProvenance(t.Context(), lift, docs)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if currentID == pendingID || currentID == baseline.latest.ID || pendingID == baseline.latest.ID {
		t.Fatal("normal provenance operations did not produce distinct IDs")
	}
	state := baseline.before.State
	current := state.History[len(state.History)-1]
	current.LiftID = currentID
	state.History = []releaseStateRevision{current}
	var raw []byte
	found := false
	for i := 1; i < 100; i++ {
		raw, err = releaseStateBytes(state)
		if err != nil {
			t.Fatal("fixture exceeded valid shared capacity", err)
		}
		next := state
		next.Operation = pendingID
		next.Pending = &releaseStateRevision{LiftID: next.Operation, SuiteDigest: current.SuiteDigest, EvidenceDigest: current.EvidenceDigest, State: "pending", Before: state.Baseline, Objects: state.Baseline}
		if reserveReleaseUpdateCapacity(next) != nil {
			found = true
			break
		}
		partial := current
		partial.LiftID = fmt.Sprintf("%032x", i)
		partial.State = "partial"
		state.History = append([]releaseStateRevision{partial}, state.History...)
	}
	if !found {
		t.Fatal("no valid near-capacity shared fixture")
	}
	snap := *baseline.before
	snap.State, snap.data = state, string(raw)
	name := releaseStateName(state.Suite, state.Member, state.Runtime)
	reads.records.objects[name]["data"].(map[string]any)["release.json"] = snap.data
	writer.updateProof = baseline.latest.UpdateProof
	result := ReleaseLiftResult{State: baseline.latest.State, Resources: baseline.latest.Resources}
	if err := writer.acknowledge(t.Context(), &snap, result); err != nil {
		writer.Close()
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.inspectReleaseUpdateBaseline(t.Context(), native, opt.Destination.Inference); err != nil {
		t.Fatal("capacity fixture must retain valid acknowledged authority", err)
	}
	path := filepath.Join(opt.Workspace, "agents/greeter/instructions.md")
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(source, []byte("\nA version with no passing cache.\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	opt.Destination.Update = true
	p, err = PrepareWorkspaceLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	beforeTasks, beforeRecords, beforePushes, beforePatches := len(tasks.creates), reads.records.writes, *pushes, api.patches
	resultWorkspace, err := a.LiftWorkspace(t.Context(), p)
	if err == nil || resultWorkspace.Eval.Status != "not-attempted" || resultWorkspace.Push.Status != "not-attempted" || len(tasks.creates) != beforeTasks || reads.records.writes != beforeRecords || *pushes != beforePushes || api.patches != beforePatches {
		t.Fatal("shared capacity refused only after spend", resultWorkspace.Eval.Status, resultWorkspace.Push.Status, err)
	}
}

func TestWorkspacePlainLiftOccupiedAdviceAfterUpdateHistory(t *testing.T) {
	for _, cache := range []string{"missing", "stale"} {
		t.Run(cache, func(t *testing.T) {
			a, opt, _, _, _, _ := workspaceLiftFixture(t)
			attachWorkspaceUpdateReads(t, a)
			p, err := PrepareWorkspaceLift(t.Context(), opt)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := a.LiftWorkspace(t.Context(), p); err != nil {
				t.Fatal(err)
			}
			p.Close()
			paths, _ := filepath.Glob(filepath.Join(p.store.partition, "lift-placement-*.json"))
			if len(paths) != 1 {
				t.Fatal("missing placement observation")
			}
			if cache == "missing" {
				if err := os.Remove(paths[0]); err != nil {
					t.Fatal(err)
				}
			} else {
				raw, _ := os.ReadFile(paths[0])
				var record workspaceLiftRecord
				_ = json.Unmarshal(raw, &record)
				record.Placement.Agent.Generation++
				raw, _ = json.Marshal(record)
				if err := os.WriteFile(paths[0], raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			p, err = PrepareWorkspaceLift(t.Context(), opt)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			if _, err := a.LiftWorkspace(t.Context(), p); err == nil || !strings.Contains(err.Error(), "--update") {
				t.Fatal("plain rerun lost eligible update advice", cache, err)
			}
		})
	}
}

func TestWorkspaceUpdateRefusesDriftAndRBACBeforeSpend(t *testing.T) {
	for _, failure := range []string{"drift", "rbac"} {
		t.Run(failure, func(t *testing.T) {
			a, opt, state, reads, _, writes := workspaceLiftFixture(t)
			p, err := PrepareWorkspaceLift(t.Context(), opt)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := a.LiftWorkspace(t.Context(), p); err != nil {
				t.Fatal(err)
			}
			p.Close()
			attachWorkspaceUpdateReads(t, a)
			path := filepath.Join(opt.Workspace, "agents/greeter/instructions.md")
			data, _ := os.ReadFile(path)
			if err := os.WriteFile(path, append(data, []byte("\nNew version.\n")...), 0600); err != nil {
				t.Fatal(err)
			}
			if failure == "drift" {
				raw, _ := os.ReadFile(filepath.Join(os.Getenv("KMX_RECONCILE_DIR"), "agents.core.orka.ai.json"))
				var doc map[string]any
				_ = json.Unmarshal(raw, &doc)
				doc["spec"].(map[string]any)["systemPrompt"] = map[string]any{"inline": "foreign edit"}
				seedReconcile(t, os.Getenv("KMX_RECONCILE_DIR"), doc)
			} else {
				prior := a.captureCommand
				a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
					if slices.Contains(cmd.Args, "auth") {
						_, err := io.WriteString(cmd.Stdout, "no\n")
						return err
					}
					return prior(ctx, cmd)
				}
			}
			opt.Destination.Update = true
			before, recordWrites := len(state.creates), reads.records.writes
			p, err = PrepareWorkspaceLift(t.Context(), opt)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			if _, err := a.LiftWorkspace(t.Context(), p); err == nil || len(state.creates) != before || reads.records.writes != recordWrites || *writes != 0 {
				t.Fatal("invalid update spent before preflight", failure, err)
			}
		})
	}
}

func TestWorkspaceUpdateRollbackNeverUsesStalePlacementSkip(t *testing.T) {
	a, opt, state, reads, _, _ := workspaceLiftFixture(t)
	partitions := map[string]string{}
	run := func() WorkspaceLiftResult {
		p, err := PrepareWorkspaceLift(t.Context(), opt)
		if err != nil {
			t.Fatal(err)
		}
		defer p.Close()
		partitions[p.plan.SuiteDigest] = p.store.partition
		r, err := a.LiftWorkspace(t.Context(), p)
		if err != nil {
			t.Fatal("workspace lifecycle failed", r, err)
		}
		return r
	}
	first := run()
	api := attachReleaseUpdateAPI(t, a, os.Getenv("KMX_RECONCILE_DIR"))
	path := filepath.Join(opt.Workspace, "agents/greeter/instructions.md")
	old, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	next := append(append([]byte(nil), old...), []byte("\nKeep the requested format.\n")...)
	if err := os.WriteFile(path, next, 0600); err != nil {
		t.Fatal(err)
	}
	graph, err := workspace.Prepare(t.Context(), opt.Workspace, filepath.Join(t.TempDir(), "registry-v2"))
	if err != nil {
		t.Fatal(err)
	}
	reads.registryLayout = graph.Path
	opt.Destination.Update = true
	opt.EvalName = "current-evaluation-alias"
	var disclosure bytes.Buffer
	a.Err = &disclosure
	second := run()
	if !strings.Contains(second.Plan.Disclosure, "quiet window") || !strings.Contains(disclosure.String(), "update retained Orka pair") {
		t.Fatal("live update consent omitted mutation/quiet-window disclosure", second.Plan.Disclosure)
	}
	if second.Lift.Status != "complete" || api.patches != 2 || second.Plan.SuiteDigest == first.Plan.SuiteDigest {
		t.Fatal("new version not applied")
	}
	spent := len(state.creates)
	// Losing a redundant managed-pass cache must not force re-evaluation of
	// an original passing digest whose canonical evidence is still retained.
	cached, _ := filepath.Glob(filepath.Join(partitions[first.Plan.SuiteDigest], "[0-9]*.json"))
	if len(cached) == 0 {
		t.Fatal("missing managed passing cache fixture")
	}
	for _, entry := range cached {
		if err := os.Remove(entry); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path, old, 0600); err != nil {
		t.Fatal(err)
	}
	rollback := run()
	if strings.Contains(rollback.Eval.Detail, opt.EvalName) || !strings.Contains(rollback.Eval.Detail, "kind-test/orka-system") || !strings.Contains(rollback.Lift.Detail, "kind-test/orka-system") {
		t.Fatal("historical evidence credited current eval alias", rollback.Eval.Detail, rollback.Lift.Detail)
	}
	if rollback.Lift.Status != "complete" || rollback.Eval.Status != "skipped" || rollback.Push.Status != "skipped" || api.patches != 4 || len(state.creates) != spent {
		t.Fatal("rollback re-evaluated or used stale completed placement", rollback)
	}
	if err := os.WriteFile(path, next, 0600); err != nil {
		t.Fatal(err)
	}
	forward := run()
	if forward.Lift.Status != "complete" || api.patches != 6 || len(state.creates) != spent {
		t.Fatal("forward update reused pre-rollback skip", forward)
	}
	noop := run()
	if noop.Lift.Status != "skipped" || api.patches != 6 || len(state.creates) != spent {
		t.Fatal("same-digest workspace update wrote", noop)
	}
}

func TestWorkspaceUpdateNoopKeepsExplicitEvidenceGate(t *testing.T) {
	a, opt, _, _, _, _ := workspaceLiftFixture(t)
	p, err := PrepareWorkspaceLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.LiftWorkspace(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	p.Close()
	attachWorkspaceUpdateReads(t, a)
	strict := false
	opt.Destination.Update = true
	opt.Promote = &strict
	p, err = PrepareWorkspaceLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if _, err := a.LiftWorkspace(t.Context(), p); err == nil {
		t.Fatal("no-op bypassed explicit strict source-evidence gate")
	}
}

func TestWorkspaceLiftUpdateColdPlanDisclosesConditionalUpdate(t *testing.T) {
	_, opt, _, _, _, _ := workspaceLiftFixture(t)
	opt.PlanOnly, opt.Destination.Update = true, true
	p, err := PrepareWorkspaceLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	var diag bytes.Buffer
	r, err := (&App{Out: io.Discard, Err: &diag}).LiftWorkspace(t.Context(), p)
	if err != nil || !strings.Contains(r.Plan.Disclosure, "update") || r.Lift.Status != "not-attempted" {
		t.Fatal("cold update plan claimed placement or omitted mode", r.Plan.Disclosure, err)
	}
}
