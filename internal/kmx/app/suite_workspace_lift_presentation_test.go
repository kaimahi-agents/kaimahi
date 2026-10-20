package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestWorkspaceLiftPresentationCannotAuthorizeWrongOrCancelledInput(t *testing.T) {
	for _, mode := range []string{"wrong", "empty", "cancel", "cancel-context", "source-drift", "binding-drift"} {
		t.Run(mode, func(t *testing.T) {
			a, opt, state, reads, _, registryWrites := workspaceLiftFixture(t)
			// An explicit UI answer must not fall back to this valid inherited value.
			a.Cfg.Confirm = "kind-test"
			called := 0
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var progress []WorkspaceLiftProgress
			opt.Progress = func(event WorkspaceLiftProgress) { progress = append(progress, event) }
			opt.Confirm = func(ctx context.Context, request WorkspaceLiftConfirmation) (string, error) {
				called++
				if request.Challenge != "kind-test" || request.Plan.SuiteDigest == "" || request.Plan.Repository != opt.Repository || request.EvalNamespace != opt.Evaluation.ToNamespace || request.DestinationNamespace != opt.Destination.ToNamespace || request.Plan.EvalServer == "" || !strings.Contains(request.Plan.Disclosure, "cleanup") {
					t.Fatalf("incomplete review: %+v", request)
				}
				switch mode {
				case "wrong":
					return "other-target", nil
				case "empty":
					return "", nil
				case "cancel":
					return "", context.Canceled
				case "cancel-context":
					cancel()
					return request.Challenge, nil
				case "source-drift":
					if err := os.WriteFile(filepath.Join(opt.Workspace, "agents/greeter/instructions.md"), []byte("Changed after review."), 0600); err != nil {
						t.Fatal(err)
					}
				case "binding-drift":
					reads.objects["providers.core.orka.ai/inference"]["metadata"].(map[string]any)["resourceVersion"] = "changed"
				}
				return request.Challenge, nil
			}
			prepared, err := PrepareWorkspaceLift(t.Context(), opt)
			if err != nil {
				t.Fatal(err)
			}
			result, err := a.LiftWorkspace(ctx, prepared)
			if err == nil || called != 1 || len(state.creates) != 0 || len(state.deletes) != 0 || *registryWrites != 0 || result.Placement != nil {
				t.Fatalf("refusal failed: mode=%s called=%d creates=%v deletes=%v result=%+v err=%v", mode, called, state.creates, state.deletes, result, err)
			}
			if mode == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cancellation: %v", err)
			}
			if a.Cfg.Confirm != "kind-test" {
				t.Fatal("callback mutated caller configuration")
			}
			for _, event := range progress {
				if event.Stage != "Plan" {
					t.Fatalf("execution stage emitted before consent: %+v", event)
				}
			}
		})
	}
}

func TestWorkspaceLiftPresentationValueCopiesAndVerifiedSkips(t *testing.T) {
	a, opt, state, _, _, _ := workspaceLiftFixture(t)
	var diagnostics bytes.Buffer
	a.Err = &diagnostics
	confirmations := 0
	var events []WorkspaceLiftProgress
	opt.Confirm = func(ctx context.Context, request WorkspaceLiftConfirmation) (string, error) {
		confirmations++
		answer := request.Challenge
		request.Plan.Repository = "must-not-be-used.invalid/repo"
		request.Plan.SuiteDigest = "changed-copy"
		request.Challenge = "changed-copy"
		return answer, nil
	}
	opt.Progress = func(event WorkspaceLiftProgress) {
		events = append(events, event)
		event.Status = "changed-copy"
	}
	for run := 0; run < 2; run++ {
		events = nil
		prepared, err := PrepareWorkspaceLift(t.Context(), opt)
		if err != nil {
			t.Fatal(err)
		}
		before := len(state.creates)
		result, err := a.LiftWorkspace(t.Context(), prepared)
		if err != nil {
			t.Fatal(err)
		}
		if result.Plan.Repository != opt.Repository || result.Plan.SuiteDigest == "changed-copy" {
			t.Fatal("presentation mutated retained plan")
		}
		want := []string{"Plan/running", "Plan/awaiting-confirmation", "Plan/complete"}
		if run == 0 {
			want = append(want, "Eval/running", "Eval/complete", "Push/running", "Push/complete", "Lift/running", "Lift/complete")
		} else {
			want = append(want, "Eval/skipped", "Push/skipped", "Lift/skipped")
			if len(state.creates) != before || result.Placement != nil {
				t.Fatal("verified skip created placement authority or resources")
			}
		}
		var got []string
		for _, event := range events {
			got = append(got, event.Stage+"/"+event.Status)
			if event.Stage == "Eval" && event.Status != "running" && (event.Passed != result.Eval.Passed || event.Total != result.Eval.Total) {
				t.Fatal("progress invented passing counts")
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("progress=%v want=%v", got, want)
		}
		count := len(events)
		if _, err := a.LiftWorkspace(t.Context(), prepared); err == nil || len(events) != count {
			t.Fatal("single-use plan repeated presentation or execution")
		}
	}
	if confirmations != 2 {
		t.Fatalf("confirmations=%d", confirmations)
	}
	if strings.Contains(diagnostics.String(), "changed-copy") {
		t.Fatal("mutated presentation copy reached CLI output")
	}
}

func TestWorkspaceLiftPlanOnlyDoesNotInvokePresentation(t *testing.T) {
	a, opt, _, _, _, _ := workspaceLiftFixture(t)
	opt.PlanOnly = true
	opt.Confirm = func(context.Context, WorkspaceLiftConfirmation) (string, error) {
		t.Fatal("plan-only invoked confirmation")
		return "", nil
	}
	opt.Progress = func(WorkspaceLiftProgress) { t.Fatal("offline plan claimed runtime progress") }
	prepared, err := PrepareWorkspaceLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.LiftWorkspace(t.Context(), prepared); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceLiftUpdatePresentationRefusesWrongOrCancelledInput(t *testing.T) {
	for _, mode := range []string{"wrong", "empty", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			a, opt, state, reads, _, writes := workspaceLiftFixture(t)
			prepared, err := PrepareWorkspaceLift(t.Context(), opt)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := a.LiftWorkspace(t.Context(), prepared); err != nil {
				t.Fatal(err)
			}
			api := attachReleaseUpdateAPI(t, a, os.Getenv("KMX_RECONCILE_DIR"))
			path := filepath.Join(opt.Workspace, "agents/greeter/instructions.md")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, append(raw, []byte("\nA new version.\n")...), 0600); err != nil {
				t.Fatal(err)
			}
			opt.Destination.Update = true
			confirmations := 0
			opt.Confirm = func(context.Context, WorkspaceLiftConfirmation) (string, error) {
				confirmations++
				switch mode {
				case "wrong":
					return "other-target", nil
				case "cancel":
					return "", context.Canceled
				}
				return "", nil
			}
			var events []WorkspaceLiftProgress
			opt.Progress = func(event WorkspaceLiftProgress) { events = append(events, event) }
			beforeCreates, beforeRecords := len(state.creates), reads.records.writes
			prepared, err = PrepareWorkspaceLift(t.Context(), opt)
			if err != nil {
				t.Fatal(err)
			}
			result, err := a.LiftWorkspace(t.Context(), prepared)
			if err == nil || confirmations != 1 || len(state.creates) != beforeCreates || reads.records.writes != beforeRecords || *writes != 0 || api.patches != 0 || result.Placement != nil {
				t.Fatal("update consent refusal allowed effects", mode, result, err)
			}
			if mode == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal("update presentation lost cancellation", err)
			}
			for _, event := range events {
				if event.Stage != "Plan" {
					t.Fatal("refused update claimed execution progress", event)
				}
			}
		})
	}
}
