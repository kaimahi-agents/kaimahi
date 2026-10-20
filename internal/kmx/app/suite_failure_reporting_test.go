package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func TestSuiteFailureReporting(t *testing.T) {
	frame, obs := failureDetailFixture("no action required")
	frame.Metadata.Assertions = make([]suitePassingAssertion, len(obs.Assertions))
	obs.TaskUID = "uid"
	for _, show := range []bool{false, true} {
		var details []suiteFailureDetail
		var out bytes.Buffer
		ctx := withSuiteFailureReporting(t.Context(), &details, &out, show)
		summary := new(suiteRunSummary)
		_, err := evaluateSuiteRunsReporting(ctx, []suitePassingInventoryCase{frame}, time.Second, io.Discard, func(ctx context.Context, f suitePassingInventoryCase) (suitePassingObservation, error) {
			captureSuiteFailureContext(ctx, f, &obs, "no action required", errSuiteNativeCaseFailed)
			return obs, errSuiteNativeCaseFailed
		}, summary)
		if !errors.Is(err, errSuiteNativeCaseFailed) || len(details) != 1 || !strings.Contains(out.String(), "no-promise (notContains)") || !strings.Contains(out.String(), "20,30 / 70") {
			t.Fatalf("missing report: %s %v %+v", out.String(), err, details)
		}
		if strings.Contains(out.String(), "no action required") != show {
			t.Fatal("content opt-in not honored")
		}
		raw, _ := json.Marshal(details[0].summary())
		if strings.Contains(string(raw), "no action required") {
			t.Fatal("payload entered summary projection")
		}
	}
	// Delivery failure must not erase collected detail or execution accounting.
	var details []suiteFailureDetail
	ctx := withSuiteFailureReporting(t.Context(), &details, suiteSummaryObserver(func([]byte) (int, error) { return 0, io.ErrClosedPipe }), false)
	summary := new(suiteRunSummary)
	_, err := evaluateSuiteRunsReporting(ctx, []suitePassingInventoryCase{frame}, time.Second, io.Discard, func(ctx context.Context, f suitePassingInventoryCase) (suitePassingObservation, error) {
		captureSuiteFailureContext(ctx, f, &obs, "no action required", errSuiteNativeCaseFailed)
		return obs, errSuiteNativeCaseFailed
	}, summary)
	if !errors.Is(err, io.ErrClosedPipe) || len(details) != 1 || !summary.ReportingFailed || summary.Executed != 1 {
		t.Fatal("reporting failure lost diagnostic or counts", err, summary)
	}
}

func TestSuiteFailureIncompleteRows(t *testing.T) {
	frame, obs := failureDetailFixture("answer")
	for i := range obs.Assertions {
		obs.Assertions[i].Verdict = agentruntime.EvaluationPass
	}
	frame.Metadata.Assertions = make([]suitePassingAssertion, len(obs.Assertions)+1)
	var details []suiteFailureDetail
	ctx := withSuiteFailureReporting(t.Context(), &details, io.Discard, false)
	_, err := evaluateSuiteRunsReporting(ctx, []suitePassingInventoryCase{frame}, time.Second, io.Discard, func(context.Context, suitePassingInventoryCase) (suitePassingObservation, error) { return obs, nil }, nil)
	if err == nil || len(details) != 1 || details[0].Verdict != "unknown" || details[0].Answer != "" {
		t.Fatal("incomplete rows were accepted", details, err)
	}
}

func TestSuiteFailureLateCancellationWithholdsContent(t *testing.T) {
	frame, obs := failureDetailFixture("no action required")
	frame.Metadata.Assertions = make([]suitePassingAssertion, len(obs.Assertions))
	obs.TaskUID = "uid"
	var details []suiteFailureDetail
	bounded, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx := withSuiteFailureReporting(bounded, &details, io.Discard, true)
	_, err := evaluateSuiteRunsReporting(ctx, []suitePassingInventoryCase{frame}, time.Minute, io.Discard, func(runCtx context.Context, f suitePassingInventoryCase) (suitePassingObservation, error) {
		captureSuiteFailureContext(runCtx, f, &obs, "no action required", errSuiteNativeCaseFailed)
		cancel()
		return obs, errSuiteNativeCaseFailed
	}, nil)
	if !errors.Is(err, context.Canceled) || len(details) != 1 || details[0].Answer != "" || len(details[0].Assertions) != 0 {
		t.Fatal("late cancellation disclosed content", details, err)
	}
}

func TestSuiteFailureLiftParity(t *testing.T) {
	var expected []suiteFailureDetail
	for _, mode := range []string{"eval-json", "lift", "lift-verbose-json"} {
		t.Run(mode, func(t *testing.T) {
			a, opt, _, _, f, writes := workspaceLiftFixture(t, func(root string) { repeatedCandidateCase(t, root) })
			f.answers[nativeCanary+" first"] = "fictional no action required"
			var out, diagnostics bytes.Buffer
			a.Out, a.Err = &out, &diagnostics
			var details []suiteFailureDetail
			if strings.HasPrefix(mode, "eval") {
				eval := opt.Evaluation
				eval.ManagedPass, eval.JSON = true, true
				p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), eval)
				if err != nil {
					t.Fatal(err)
				}
				defer p.Close()
				err = a.EvaluateSuiteWorkspace(t.Context(), p)
				if err == nil {
					t.Fatal("expected failure")
				}
				details = p.failures
			} else {
				opt.Verbose, opt.JSON = strings.Contains(mode, "verbose"), strings.Contains(mode, "json")
				p, err := PrepareWorkspaceLift(t.Context(), opt)
				if err != nil {
					t.Fatal(err)
				}
				defer p.Close()
				r, err := a.LiftWorkspace(t.Context(), p)
				if err == nil || r.Push.Status != "not-attempted" || r.Lift.Status != "not-attempted" || *writes != 0 {
					t.Fatalf("failed evaluation advanced: %+v %v", r, err)
				}
				details = p.failures
			}
			if len(details) != 3 || !strings.Contains(diagnostics.String(), "expectContains.0") || strings.Contains(diagnostics.String(), "fictional no action required") {
				t.Fatalf("default detail missing or content leaked: failures=%d output=%s", len(details), diagnostics.String())
			}
			if strings.Contains(mode, "json") {
				decoder := json.NewDecoder(&out)
				var report map[string]any
				if err := decoder.Decode(&report); err != nil {
					t.Fatal(err)
				}
				if err := decoder.Decode(&report); err != io.EOF {
					t.Fatal("not exactly one JSON document", err)
				}
				raw, _ := json.Marshal(report)
				if strings.Contains(string(raw), "fictional no action required") {
					t.Fatal("answer entered outcome JSON")
				}
			}
			if strings.HasPrefix(mode, "eval") {
				expected = details
			} else if !reflect.DeepEqual(expected, details) {
				t.Fatalf("standalone/Lift detail differs: %+v vs %+v", expected, details)
			}
		})
	}
}
