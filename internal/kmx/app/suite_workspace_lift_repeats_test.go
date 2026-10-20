package app

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestWorkspaceLiftEvaluationPreflightCounts(t *testing.T) {
	a, opt, state, _, _, writes := workspaceLiftFixture(t, func(root string) { repeatedCandidateCase(t, root) })
	a.Out, a.Err = io.Discard, io.Discard
	state.change = func(_ string, cmd *exec.Cmd) error {
		if slices.Contains(cmd.Args, "serviceaccount") && slices.Contains(cmd.Args, orkaResultAccount) {
			return errors.New(nativeCanary)
		}
		return nil
	}
	p, err := PrepareWorkspaceLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	r, err := a.LiftWorkspace(t.Context(), p)
	if err == nil || r.Eval.Total != 3 || r.Eval.SuiteRunExecutionSummary == nil || r.Eval.Executed != 0 || r.Eval.NotExecuted != 3 || r.Eval.Aborted {
		t.Fatal("pre-run refusal lost planned run count", r, err)
	}
	if len(state.creates) != 0 || *writes != 0 || r.Push.Status != "not-attempted" || r.Lift.Status != "not-attempted" {
		t.Fatal("preflight refusal advanced", r)
	}
	if strings.Contains(r.Eval.Detail, "cleanup completed") || strings.Contains(r.Eval.Error, nativeCanary) {
		t.Fatal("unsafe preflight report", r)
	}
}

func TestWorkspaceLiftCachedThresholdDoesNotInventPassingRuns(t *testing.T) {
	a, opt, state, _, f, _ := workspaceLiftFixture(t, func(root string) {
		path := filepath.Join(root, "eval/example.yaml")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, path, string(raw)+"\nruns: 2\nminPasses: 1\n")
	})
	a.Out, a.Err = io.Discard, io.Discard
	tasks := 0
	state.afterCreate = func(key string) {
		if strings.HasPrefix(key, "Task/") {
			tasks++
			f.answers[nativeCanary+" first"] = nativeCanary + " answer"
			if tasks == 1 {
				f.answers[nativeCanary+" first"] = "wrong"
			}
		}
	}
	for run := 0; run < 2; run++ {
		p, err := PrepareWorkspaceLift(t.Context(), opt)
		if err != nil {
			t.Fatal(err)
		}
		result, err := a.LiftWorkspace(t.Context(), p)
		if err != nil {
			p.Close()
			t.Fatal(err)
		}
		if err := p.Close(); err != nil {
			t.Fatal(err)
		}
		failures, err := openSuiteFailureStore(opt.Workspace, false)
		if err != nil {
			t.Fatal(err)
		}
		rows, scanErr := failures.scan()
		closeErr := failures.Close()
		if scanErr != nil || closeErr != nil || len(rows) != 1 || rows[0].record.Detail.Run != 1 || rows[0].record.Detail.Verdict != "fail" {
			t.Fatal("threshold pass or cached skip lost/invented failure history", scanErr, closeErr)
		}
		raw, err := json.Marshal(result.Plan)
		if err != nil {
			t.Fatal(err)
		}
		var plan map[string]any
		if err := json.Unmarshal(raw, &plan); err != nil {
			t.Fatal(err)
		}
		if plan["caseRuns"] != float64(3) || plan["cases"] != nil {
			t.Fatalf("ambiguous execution count unit: %s", raw)
		}
		if result.Eval.Passed != 2 || result.Eval.Total != 3 {
			t.Fatalf("run %d invented passed runs: %+v", run, result.Eval)
		}
		if run == 0 && (result.Eval.SuiteRunExecutionSummary == nil || result.Eval.Executed != 3 || result.Eval.NotExecuted != 0 || result.Eval.Aborted) {
			t.Fatal("missing fresh execution counts", result.Eval)
		}
		if run == 1 {
			raw, err := json.Marshal(result.Eval)
			if err != nil || strings.Contains(string(raw), "executed") || strings.Contains(string(raw), "notExecuted") || strings.Contains(string(raw), "aborted") {
				t.Fatal("cached GET-only skip claimed current execution", string(raw), err)
			}
		}
	}
	if tasks != 3 {
		t.Fatalf("cached threshold re-executed: %d tasks", tasks)
	}
}
