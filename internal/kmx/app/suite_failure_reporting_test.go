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
	for _, missing := range []string{"", " \t", "Authorization: opaque-private-value"} {
		detail := captureSuiteFailure(frame, obs, "no action required", map[string]suiteJudgeDiagnostic{"quality": {Reasons: []string{"public rationale", missing}}}, nil)
		for _, show := range []bool{false, true} {
			var out bytes.Buffer
			if err := writeSuiteFailureDetail(&out, *detail, show); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(out.String(), "rationale: unavailable or withheld") != show || strings.Contains(out.String(), `rationale: ""`) || strings.Contains(out.String(), "public rationale") != show {
				t.Fatal("missing rationale was not identified without losing safe rationale")
			}
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

func TestSuiteFailureLiftStopsOnDiagnosticWriteError(t *testing.T) {
	for _, mode := range []string{"text", "json"} {
		t.Run(mode, func(t *testing.T) {
			a, opt, state, _, f, writes := workspaceLiftFixture(t, func(root string) { repeatedCandidateCase(t, root) })
			opt.JSON = mode == "json"
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
			var out bytes.Buffer
			a.Out = &out
			a.Err = suiteSummaryObserver(func(raw []byte) (int, error) {
				if strings.Contains(string(raw), "expectContains.0 (contains)") {
					return 0, io.ErrClosedPipe
				}
				return len(raw), nil
			})
			p, err := PrepareWorkspaceLift(t.Context(), opt)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			result, err := a.LiftWorkspace(t.Context(), p)
			if !errors.Is(err, io.ErrClosedPipe) || tasks != 1 || *writes != 0 || result.Push.Status != "not-attempted" || result.Lift.Status != "not-attempted" {
				t.Fatalf("diagnostics failed after later effects: tasks=%d registryWrites=%d result=%+v err=%v", tasks, *writes, result, err)
			}
			if result.Eval.SuiteRunExecutionSummary == nil || result.Eval.Executed != 1 || result.Eval.NotExecuted != 2 || !result.Eval.ReportingFailed || len(p.failures) != 1 {
				t.Fatal("delivery failure erased execution accounting", result.Eval)
			}
			store, err := openSuitePassStore(t.Context(), p.review.root, p.store.cache, p.plan.SuiteDigest)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if _, _, err := store.latest(t.Context(), p.review.suite); !errors.Is(err, errSuitePassMissing) {
				t.Fatal("diagnostic delivery failure registered passing proof", err)
			}
			retained, err := openSuiteFailureStore(opt.Workspace, false)
			if err != nil {
				t.Fatal(err)
			}
			rows, scanErr := retained.scan()
			closeErr := retained.Close()
			if scanErr != nil || closeErr != nil || len(rows) != 1 || rows[0].record.Detail.ID != p.failures[0].ID {
				t.Fatal("writer failure lost retained diagnostic", scanErr, closeErr)
			}
		})
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
			store, err := openSuiteFailureStore(opt.Workspace, false)
			if err != nil {
				t.Fatal(err)
			}
			entries, scanErr := store.scan()
			closeErr := store.Close()
			if scanErr != nil || closeErr != nil || len(entries) != len(details) {
				t.Fatal("stored detail missing", scanErr, closeErr)
			}
			ids := map[string]bool{}
			for i, entry := range entries {
				if !reflect.DeepEqual(entry.record.Detail, details[i]) {
					t.Fatal("stored and live diagnostics differ")
				}
				if !suiteFailureID.MatchString(details[i].ID) || ids[details[i].ID] || details[i].OperationID != details[0].OperationID {
					t.Fatal("run identity not unique or operation identity changed")
				}
				ids[details[i].ID] = true
			}
			normalized := append([]suiteFailureDetail(nil), details...)
			for i := range normalized {
				normalized[i].ID, normalized[i].OperationID = "", ""
			}
			if strings.HasPrefix(mode, "eval") {
				expected = normalized
			} else if !reflect.DeepEqual(expected, normalized) {
				t.Fatal("standalone/Lift recorded detail differs")
			}
		})
	}
}
