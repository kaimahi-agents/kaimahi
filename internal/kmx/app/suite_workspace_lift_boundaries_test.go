package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestWorkspaceLiftCompletionCapacityReservesBothStages(t *testing.T) {
	a, opt, _, _, _, _ := workspaceLiftFixture(t)
	_ = a
	p, err := PrepareWorkspaceLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	for i := 0; i < 63; i++ {
		if err := os.WriteFile(filepath.Join(p.store.partition, fmt.Sprintf("lift-other-%03d.json", i)), []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	first, err := p.liftRecord(t.Context(), "publication", opt.Repository)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := p.liftRecord(t.Context(), "placement", "other")
	if err == nil {
		second.Close()
		t.Fatal("both stages reserved the last completion slot")
	}
}

func TestWorkspaceLiftKubeconfigChangesAfterDisclosedPlan(t *testing.T) {
	a, opt, state, _, _, writes := workspaceLiftFixture(t)
	old := a.captureCommand
	configs := 0
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		if !slices.Contains(cmd.Args, "config") {
			return old(ctx, cmd)
		}
		configs++
		var output bytes.Buffer
		out := cmd.Stdout
		cmd.Stdout = &output
		err := old(ctx, cmd)
		cmd.Stdout = out
		if configs > 1 {
			output.WriteString(" ")
		}
		_, e := cmd.Stdout.Write(output.Bytes())
		if err != nil {
			return err
		}
		return e
	}
	p, err := PrepareWorkspaceLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	r, err := a.LiftWorkspace(t.Context(), p)
	if err == nil || !strings.Contains(err.Error(), "kubeconfig changed") || r.Eval.Status != "not-attempted" || len(state.creates) != 0 || *writes != 0 {
		t.Fatal("redirected plan accepted", r, err)
	}
}

func TestWorkspaceLiftStoreFinalizationErrorInSingleJSON(t *testing.T) {
	a, opt, _, _, _, _ := workspaceLiftFixture(t)
	opt.JSON = true
	p, err := PrepareWorkspaceLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	old := a.captureCommand
	closed := false
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		closeAfter := false
		if slices.Contains(cmd.Args, "-f") && slices.Contains(cmd.Args, "create") && !slices.Contains(cmd.Args, "--dry-run=server") {
			raw, _ := io.ReadAll(cmd.Stdin)
			cmd.Stdin = bytes.NewReader(raw)
			var doc map[string]any
			_ = json.Unmarshal(raw, &doc)
			closeAfter = doc["kind"] == "Agent" && orkaObjectName(doc) == "greeter"
		}
		err := old(ctx, cmd)
		if closeAfter && err == nil {
			if e := p.store.lock.Close(); e != nil {
				t.Fatal(e)
			}
			closed = true
		}
		return err
	}
	var stdout bytes.Buffer
	a.Out = &stdout
	a.Err = io.Discard
	r, err := a.LiftWorkspace(t.Context(), p)
	if err == nil || !closed || r.Lift.Status != "complete" || !strings.Contains(r.Error, "finalization failed") || r.Placement == nil || r.Placement.State != "ready" {
		t.Fatal("successful effects/finalization error lost", r, err)
	}
	var decoded WorkspaceLiftResult
	decoder := json.NewDecoder(&stdout)
	if decoder.Decode(&decoded) != nil || decoded.Error != r.Error || decoder.Decode(&decoded) != io.EOF {
		t.Fatal("late error outside JSON", stdout.String())
	}
}

func TestWorkspaceLiftCachedPassMustMatchSelectedFreshSubject(t *testing.T) {
	a, opt, state, reads, _, _ := workspaceLiftFixture(t)
	ep, err := PrepareSuiteWorkspaceEvaluation(t.Context(), func() SuiteWorkspaceEvaluationOptions { o := opt.Evaluation; o.ManagedPass = true; return o }())
	if err != nil {
		t.Fatal(err)
	}
	if err := a.EvaluateSuiteWorkspace(t.Context(), ep); err != nil {
		t.Fatal(err)
	}
	oldReceipt, _, err := ep.store.latest(t.Context(), ep.suite)
	if err != nil {
		t.Fatal(err)
	}
	if err := ep.Close(); err != nil {
		t.Fatal(err)
	}
	// Same S/context/namespace, different selected inference. Historical completed
	// candidate proof is not evidence for this freshly selected subject.
	var provider map[string]any
	_ = json.Unmarshal(mustJSON(t, reads.objects["providers.core.orka.ai/inference"]), &provider)
	nativeMetadata(provider)["name"] = "other-inference"
	reads.objects["providers.core.orka.ai/other-inference"] = provider
	opt.Evaluation.Inference = "provider:other-inference"
	r := runWorkspaceLift(t, a, opt)
	if r.Eval.Status != "complete" || len(state.creates) != 8 {
		t.Fatal("selected-subject drift reused cached pass", r, state.creates)
	}
	prepared, err := PrepareWorkspaceLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Close()
	receipt, _, err := prepared.store.latest(t.Context(), prepared.review.suite)
	if err != nil || receipt.RunID == oldReceipt.RunID || receipt.Subject.Binding.Provider.Name != "other-inference" {
		t.Fatal("did not use a fresh candidate run", receipt, err)
	}
}

func TestWorkspaceLiftEditedVersionFailsEvalBeforeInspectingOccupiedDestination(t *testing.T) {
	a, opt, state, reads, f, writes := workspaceLiftFixture(t)
	a.Out, a.Err = io.Discard, io.Discard
	runWorkspaceLift(t, a, opt)
	before := len(state.creates)
	// Isolate the historical no-record occupied-target path; retained new
	// records refuse before spend and are covered separately.
	clear(reads.records.objects)
	writeFile(t, filepath.Join(opt.Workspace, "agents", "greeter", "instructions.md"), "Edited instructions for the new version.\n")
	f.answers[nativeCanary+" first"] = "wrong"
	p, err := PrepareWorkspaceLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	r, err := a.LiftWorkspace(t.Context(), p)
	if !errors.Is(err, errSuiteNativeCaseFailed) || r.Eval.Status != "failed" || r.Push.Status != "not-attempted" || r.Lift.Status != "not-attempted" {
		t.Fatalf("old occupied destination prevented proving the edit: %+v %v", r, err)
	}
	if len(state.creates) != before+3 || *writes != 0 || len(persistentOrkaCalls(t, os.Getenv("KMX_RECONCILE_DIR"))) != 2 {
		t.Fatal("failed edit did not execute one fresh case or changed publication/destination")
	}
}

func TestWorkspaceLiftRequiredProvenanceCannotTrustPublicationCompletion(t *testing.T) {
	a, opt, state, _, _, writes := workspaceLiftFixture(t)
	a.Out, a.Err = io.Discard, io.Discard
	runWorkspaceLift(t, a, opt)
	before := len(state.creates)
	opt.Provenance = SuitePushProvenance{Enabled: true, Require: true}
	p, err := PrepareWorkspaceLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	r, err := a.LiftWorkspace(t.Context(), p)
	if err == nil || !strings.Contains(err.Error(), "provenance") || r.Push.Status != "not-attempted" || r.Lift.Status != "not-attempted" || len(state.creates) != before || *writes != 0 || len(persistentOrkaCalls(t, os.Getenv("KMX_RECONCILE_DIR"))) != 2 {
		t.Fatalf("required provenance trusted old publication without read-only proof: %+v %v", r, err)
	}
}

func TestWorkspaceLiftSourceEditDuringDestinationInspectionStopsPublication(t *testing.T) {
	a, opt, state, _, _, writes := workspaceLiftFixture(t)
	a.Out, a.Err = io.Discard, io.Discard
	old := a.captureCommand
	edited := false
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		err := old(ctx, cmd)
		if !edited && len(state.creates) == 4 && slices.Contains(cmd.Args, "get") && slices.Contains(cmd.Args, "providers.core.orka.ai") && slices.Contains(cmd.Args, "greeter") {
			writeFile(t, filepath.Join(opt.Workspace, "agents", "greeter", "instructions.md"), "Changed after eval, before publication.\n")
			edited = true
		}
		return err
	}
	p, err := PrepareWorkspaceLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	r, err := a.LiftWorkspace(t.Context(), p)
	records, _ := filepath.Glob(filepath.Join(p.store.partition, "lift-publication-*.json"))
	if !edited || err == nil || r.Eval.Status != "complete" || r.Push.Status != "not-attempted" || len(records) != 0 || *writes != 0 || len(persistentOrkaCalls(t, os.Getenv("KMX_RECONCILE_DIR"))) != 0 {
		t.Fatalf("inspection-time edit advanced publication: %+v %v records=%v", r, err, records)
	}
}

func TestWorkspaceLiftOccupiedUnrecordedDestinationRefuses(t *testing.T) {
	a, opt, state, _, _, writes := workspaceLiftFixture(t)
	runWorkspaceLift(t, a, opt)
	// Deliberately discard only the completion in this fixture. Live ownership is
	// not adoption/recovery authority, even with the same package and exact UIDs.
	files, err := filepath.Glob(filepath.Join(os.Getenv("KMX_HOME"), "passing", "*", "*", "lift-placement-*.json"))
	if err != nil || len(files) != 1 {
		t.Fatal(files, err)
	}
	if err := os.Remove(files[0]); err != nil {
		t.Fatal(err)
	}
	before := len(state.creates)
	p, err := PrepareWorkspaceLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	r, err := a.LiftWorkspace(t.Context(), p)
	if err == nil || len(state.creates) != before || *writes != 0 || r.Lift.Status != "not-attempted" || len(persistentOrkaCalls(t, os.Getenv("KMX_RECONCILE_DIR"))) != 2 {
		t.Fatal("occupied destination replayed", r, err)
	}
}
