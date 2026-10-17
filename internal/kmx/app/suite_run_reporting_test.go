package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// Accept setup diagnostics, then fail the first actual run result.
type failingSuiteRunLineWriter struct {
	bytes.Buffer
	cause    error
	runLines int
}

func (w *failingSuiteRunLineWriter) Write(p []byte) (int, error) {
	if strings.HasPrefix(string(p), "first run ") {
		w.runLines++
		return 0, w.cause
	}
	return w.Buffer.Write(p)
}

func TestSuiteRunReportingFailureKeepsObservationCounts(t *testing.T) {
	for _, mode := range []string{"pass", "assertion fail", "execution fail"} {
		t.Run(mode, func(t *testing.T) {
			frames := repeatDriverFrames(t, "runs: 3\nminPasses: 2\n")
			writerErr := errors.New(nativeCanary)
			out := &failingSuiteRunLineWriter{cause: writerErr}
			var summary suiteRunSummary
			calls := 0
			observations, err := evaluateSuiteRunsReporting(t.Context(), frames, time.Minute, out, func(_ context.Context, frame suitePassingInventoryCase) (suitePassingObservation, error) {
				calls++
				rows := agentruntime.EvaluateAssertions(frame.Case, agentruntime.EvaluationEvidence{Answer: "answer", AnswerAvailable: true})
				observed := suitePassingObservation{EvaluationReceipt: agentruntime.EvaluationReceipt{TaskUID: "task-uid", AnswerSHA256: agentruntime.EvaluationAnswerDigest("answer"), Verdict: agentruntime.EvaluationPass, Assertions: rows}}
				if mode == "execution fail" {
					observed.AnswerSHA256, observed.Assertions, observed.Verdict = "", nil, agentruntime.EvaluationFail
					return observed, errSuiteNativeCaseFailed
				}
				if mode == "assertion fail" {
					observed.Assertions[0].Verdict = agentruntime.EvaluationFail
					observed.Verdict = agentruntime.EvaluationFail
					return observed, errSuiteNativeCaseFailed
				}
				return observed, nil
			}, &summary)
			if err != writerErr || observations != nil || calls != 1 || out.runLines != 1 {
				t.Fatalf("reporting failure changed abort/return contract: calls=%d observations=%v err=%v", calls, observations, err)
			}
			passed := 0
			if mode == "pass" {
				passed = 1
			}
			if summary.Executed != 1 || summary.Passed != passed || summary.Total != 3 || summary.NotExecuted != 2 || summary.Aborted != (mode == "execution fail") {
				t.Fatalf("reporting failure corrupted observation summary: %+v", summary)
			}
			if !strings.Contains(summary.String(), "reporting error") || strings.Contains(summary.String(), nativeCanary) {
				t.Fatal("missing safe reporting-failure summary", summary.String())
			}
		})
	}
}

func TestRootEvalJSONRunLineFailure(t *testing.T) {
	for _, mode := range []string{"pass", "Failed"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("KMX_HOME", privateSuiteDir(t))
			a, _, f, state := publicNativeEvaluationFixture(t)
			opt := nativeEvaluationOptions(t)
			opt.ResultPort, opt.ManagedPass, opt.JSON = f.opt.ResultPort, true, true
			repeatedCandidateCase(t, opt.Workspace)
			p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			f.answers[nativeCanary+" first"] = nativeCanary + " answer"
			tasks := new(int)
			if mode == "Failed" {
				tasks = candidateAbortScenario(t, state, mode)
			} else {
				state.afterCreate = func(key string) {
					if strings.HasPrefix(key, "Task/") {
						*tasks++
					}
				}
			}
			writerErr := errors.New(nativeCanary)
			diagnostics := &failingSuiteRunLineWriter{cause: writerErr}
			var stdout bytes.Buffer
			a.Out, a.Err = &stdout, diagnostics
			err = a.EvaluateSuiteWorkspace(t.Context(), p)
			var report struct {
				Status, Detail, Error, Cleanup       string
				Executed, Passed, Total, NotExecuted int
				Aborted, ReportingFailed             bool
			}
			if e := json.Unmarshal(stdout.Bytes(), &report); e != nil {
				t.Fatalf("stdout is not one JSON report: %s (%v)", &stdout, e)
			}
			passed := 0
			if mode == "pass" {
				passed = 1
			}
			if err == nil || !errors.Is(err, writerErr) || *tasks != 1 || diagnostics.runLines != 1 || !strings.Contains(diagnostics.String(), "Run preflight:") {
				t.Fatalf("diagnostic failure did not stop after first run: Tasks=%d report=%+v err=%v", *tasks, report, err)
			}
			if report.Executed != 1 || report.Passed != passed || report.Total != 3 || report.NotExecuted != 2 || report.Aborted != (mode == "Failed") || !report.ReportingFailed {
				t.Fatalf("dishonest JSON reporting failure: %+v err=%v", report, err)
			}
			if report.Status != "failed" || report.Error == "" || !strings.Contains(report.Detail, "reporting error") || !strings.Contains(report.Detail, "no passing evidence published") {
				t.Fatal("reporting failure claimed success or omitted safe failure details", report)
			}
			if mode == "pass" && strings.Contains(report.Detail+err.Error(), "execution error") || mode == "Failed" && !strings.Contains(report.Detail, "aborted on execution error") {
				t.Fatal("reporting failure changed execution classification", report, err)
			}
			if report.Cleanup != "candidate cleanup completed" || len(state.objects) != 0 {
				t.Fatal("cleanup not verified", report, err)
			}
			if _, e := os.Stat(opt.Out); !os.IsNotExist(e) {
				t.Fatal("reporting failure exported evidence", e)
			}
			if _, _, e := p.store.latest(t.Context(), p.suite); !errors.Is(e, errSuitePassMissing) {
				t.Fatal("reporting failure registered managed proof", e)
			}
			if records, e := filepath.Glob(filepath.Join(p.store.partition, "*.json")); e != nil || len(records) != 0 {
				t.Fatal("reporting failure persisted proof", records, e)
			}
			if strings.Contains(stdout.String()+diagnostics.String()+err.Error(), nativeCanary) {
				t.Fatal("raw diagnostic writer cause leaked")
			}
		})
	}
}
