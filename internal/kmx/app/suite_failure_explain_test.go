package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
)

func TestSuiteFailureExplain(t *testing.T) {
	root, d := failureStoreFixture(t)
	if err := os.WriteFile(filepath.Join(root, "workspace.yaml"), []byte("schemaVersion: 1\nname: fictional\nmember: agents/fictional/member.yaml\nbuildProfile: build-profiles/default.json\n"), 0644); err != nil {
		t.Fatal(err)
	}
	id, err := saveSuiteFailure(t.Context(), root, d)
	if err != nil {
		t.Fatal(err)
	}
	for _, show := range []bool{false, true} {
		var out bytes.Buffer
		a := &App{Out: &out, Err: io.Discard}
		if err := a.ExplainSuiteEvaluation(t.Context(), SuiteEvaluationExplainOptions{Workspace: root, ID: id, ShowContent: show}); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), id) || !strings.Contains(out.String(), d.SuiteDigest) || strings.Contains(out.String(), "fictional answer") != show {
			t.Fatal("offline detail/content selection", out.String())
		}
	}
	var list bytes.Buffer
	a := &App{Out: &list, Err: io.Discard}
	if err := a.ExplainSuiteEvaluation(t.Context(), SuiteEvaluationExplainOptions{Workspace: root}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(list.String(), id) || strings.Contains(list.String(), "fictional answer") {
		t.Fatal("unsafe list")
	}
	if err := a.ExplainSuiteEvaluation(t.Context(), SuiteEvaluationExplainOptions{Workspace: root, ID: "../../passing", Clear: true}); err == nil {
		t.Fatal("path ID accepted")
	}
	if err := a.ExplainSuiteEvaluation(t.Context(), SuiteEvaluationExplainOptions{Clear: true}); err == nil {
		t.Fatal("unscoped clear accepted")
	}
	if err := a.ExplainSuiteEvaluation(t.Context(), SuiteEvaluationExplainOptions{Workspace: root, ID: id, Clear: true, ShowContent: true}); err == nil {
		t.Fatal("conflicting options accepted")
	}
	if err := a.ExplainSuiteEvaluation(t.Context(), SuiteEvaluationExplainOptions{Workspace: root, ID: id, Clear: true}); err != nil {
		t.Fatal(err)
	}
	if err := a.ExplainSuiteEvaluation(t.Context(), SuiteEvaluationExplainOptions{Workspace: root, ID: id}); !errors.Is(err, errSuiteFailureMissing) {
		t.Fatal("cleared record remains", err)
	}
	if err := a.ExplainSuiteEvaluation(t.Context(), SuiteEvaluationExplainOptions{Workspace: root, Clear: true}); err != nil {
		t.Fatal(err)
	}
}

func TestSuiteFailureExplainDelivery(t *testing.T) {
	root, d := failureStoreFixture(t)
	if err := os.WriteFile(filepath.Join(root, "workspace.yaml"), []byte("schemaVersion: 1\nname: fictional\nmember: agents/fictional/member.yaml\nbuildProfile: build-profiles/default.json\n"), 0644); err != nil {
		t.Fatal(err)
	}
	id, err := saveSuiteFailure(t.Context(), root, d)
	if err != nil {
		t.Fatal(err)
	}
	a := &App{Out: suiteSummaryObserver(func([]byte) (int, error) { return 0, errors.New("private-writer-value") })}
	for _, clear := range []bool{false, true} {
		err := a.ExplainSuiteEvaluation(t.Context(), SuiteEvaluationExplainOptions{Workspace: root, ID: id, Clear: clear})
		if err == nil || strings.Contains(err.Error(), "private-writer-value") {
			t.Fatal("unsafe report error", err)
		}
	}
	if _, err := saveSuiteFailure(t.Context(), root, d); err != nil {
		t.Fatal(err)
	}
}

func TestSuiteFailureExplainAbsentIsInert(t *testing.T) {
	root, _ := failureStoreFixture(t)
	if err := os.WriteFile(filepath.Join(root, "workspace.yaml"), []byte("schemaVersion: 1\nname: fictional\nmember: agents/fictional/member.yaml\nbuildProfile: build-profiles/default.json\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, clear := range []bool{false, true} {
		var out bytes.Buffer
		a := &App{Out: &out}
		if err := a.ExplainSuiteEvaluation(t.Context(), SuiteEvaluationExplainOptions{Workspace: root, Clear: clear}); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(os.Getenv("KMX_HOME")); !os.IsNotExist(err) {
			t.Fatal("empty inspection/clear created private state", err)
		}
	}
}

func TestSuiteFailureRetainedBeforeOutputAndCancel(t *testing.T) {
	root, d := failureStoreFixture(t)
	var details []suiteFailureDetail
	bounded, cancel := context.WithCancel(t.Context())
	ctx := withSuiteFailureReporting(bounded, &details, suiteSummaryObserver(func([]byte) (int, error) { return 0, io.ErrClosedPipe }), false)
	ctx = withSuiteFailureRetention(ctx, root, &workspace.NativeSuite{Digest: d.SuiteDigest, Runtime: workspace.RuntimeProfile{ID: d.Runtime}, MemberID: d.MemberID})
	cancel()
	if err := reportSuiteFailure(ctx, &d); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal("writer error changed", err)
	}
	s, err := openSuiteFailureStore(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rows, err := s.scan()
	if err != nil || len(rows) != 1 || len(details) != 1 || details[0].ID == "" || rows[0].record.Detail.ID != details[0].ID {
		t.Fatal("failure not retained before output/cancel", err)
	}
}

func TestSuiteFailureRetentionUnavailableLift(t *testing.T) {
	for _, mode := range []string{"store", "store-and-output"} {
		t.Run(mode, func(t *testing.T) {
			a, opt, state, _, f, writes := workspaceLiftFixture(t, func(root string) { repeatedCandidateCase(t, root) })
			f.answers[nativeCanary+" first"] = "fictional failed answer"
			var out bytes.Buffer
			a.Out = io.Discard
			a.Err = &out
			if mode == "store-and-output" {
				a.Err = suiteSummaryObserver(func(raw []byte) (int, error) {
					if strings.Contains(string(raw), "expectContains.0 (contains)") {
						return 0, io.ErrClosedPipe
					}
					return out.Write(raw)
				})
			}
			base := filepath.Join(os.Getenv("KMX_HOME"), "eval-failures")
			if err := os.MkdirAll(base, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(base, 0755); err != nil {
				t.Fatal(err)
			}
			tasks := 0
			state.afterCreate = func(key string) {
				if strings.HasPrefix(key, "Task/") {
					tasks++
				}
			}
			p, err := PrepareWorkspaceLift(t.Context(), opt)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			result, err := a.LiftWorkspace(t.Context(), p)
			want := 3
			if mode == "store-and-output" {
				want = 1
				if !errors.Is(err, io.ErrClosedPipe) {
					t.Fatal("writer error identity lost", err)
				}
			}
			if err == nil || errors.Is(err, errSuiteFailureStore) || tasks != want || len(p.failures) != want || result.Eval.Executed != want || result.Push.Status != "not-attempted" || result.Lift.Status != "not-attempted" || *writes != 0 {
				t.Fatal("optional retention altered failure lifecycle", tasks, err)
			}
			for _, detail := range p.failures {
				if !detail.RetentionUnavailable || detail.ID != "" {
					t.Fatal("failed retention not disclosed")
				}
			}
			if strings.Count(out.String(), "warning: local evaluation failure retention unavailable") != 1 {
				t.Fatal("warning not bounded")
			}
		})
	}
}

func TestSuiteFailureRetentionWarningWriteStopsLift(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			a, opt, state, _, f, writes := workspaceLiftFixture(t, func(root string) { repeatedCandidateCase(t, root) })
			opt.JSON = format == "json"
			f.answers[nativeCanary+" first"] = "fictional failed answer"
			var out bytes.Buffer
			a.Out = io.Discard
			a.Err = suiteSummaryObserver(func(raw []byte) (int, error) {
				if strings.Contains(string(raw), "warning: local evaluation failure retention") {
					return 0, io.ErrClosedPipe
				}
				return out.Write(raw)
			})
			base := filepath.Join(os.Getenv("KMX_HOME"), "eval-failures")
			if err := os.MkdirAll(base, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(base, 0755); err != nil {
				t.Fatal(err)
			}
			tasks := 0
			state.afterCreate = func(key string) {
				if strings.HasPrefix(key, "Task/") {
					tasks++
					if tasks > 1 {
						f.answers[nativeCanary+" first"] = nativeCanary + " answer"
					}
				}
			}
			p, err := PrepareWorkspaceLift(t.Context(), opt)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			result, err := a.LiftWorkspace(t.Context(), p)
			if !errors.Is(err, io.ErrClosedPipe) || tasks != 1 || len(p.failures) != 1 || result.Eval.SuiteRunExecutionSummary == nil || result.Eval.Executed != 1 || result.Eval.NotExecuted != 2 || !result.Eval.ReportingFailed || result.Push.Status != "not-attempted" || result.Lift.Status != "not-attempted" || *writes != 0 {
				t.Fatal("warning delivery failure allowed later effects", tasks, err)
			}
			if !p.failures[0].RetentionUnavailable || !strings.Contains(out.String(), "expectContains.0 (contains)") {
				t.Fatal("collected detail was lost")
			}
			store, err := openSuitePassStore(t.Context(), p.review.root, p.store.cache, p.plan.SuiteDigest)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if _, _, err := store.latest(t.Context(), p.review.suite); !errors.Is(err, errSuitePassMissing) {
				t.Fatal("warning failure registered proof", err)
			}
		})
	}
}

func TestSuiteFailureAPIKeyRetention(t *testing.T) {
	for _, name := range []string{"Api-Key", "api_key", "APIKEY"} {
		t.Run(name, func(t *testing.T) {
			root, meta := failureStoreFixture(t)
			header := name + ": opaque-private-value"
			payload, err := json.Marshal(map[string]any{"score": 20, "reasoning": header})
			if err != nil {
				t.Fatal(err)
			}
			score, reason, match, ok := parseEvaluationJudgeResponse([]byte(judgeEnvelope(string(payload))), "fixture/model", "pinned-v1", "")
			if !ok || score != 20 || match != "requested" {
				t.Fatal("privacy changed accepted judge evidence")
			}
			frame, obs := failureDetailFixture(header)
			frame.Case.ID = header
			frame.Case.Assertions[0].Value = header
			d := *captureSuiteFailure(frame, obs, header, map[string]suiteJudgeDiagnostic{"quality": {Reasons: []string{reason, reason}}}, nil)
			d.OperationID, d.SuiteDigest, d.Runtime, d.MemberID = meta.OperationID, meta.SuiteDigest, meta.Runtime, meta.MemberID
			d.Complete, d.Attributed = true, true
			id, err := saveSuiteFailure(t.Context(), root, d)
			if err != nil {
				t.Fatal(err)
			}
			store, err := openSuiteFailureStore(root, false)
			if err != nil {
				t.Fatal(err)
			}
			rows, scanErr := store.scan()
			closeErr := store.Close()
			if scanErr != nil || closeErr != nil || len(rows) != 1 {
				t.Fatal("readback failed", scanErr, closeErr)
			}
			raw, err := json.Marshal(rows[0].record)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "opaque-private-value") || reason != "" || d.AnswerStatus != "withheld" || d.CaseID != "withheld" || d.Assertions[0].Expected != "" || !d.Assertions[1].RationaleUnavailable {
				t.Fatal("standalone API-key header persisted")
			}
			if err := os.WriteFile(filepath.Join(root, "workspace.yaml"), []byte("schemaVersion: 1\nname: fictional\nmember: agents/fictional/member.yaml\nbuildProfile: build-profiles/default.json\n"), 0644); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			a := &App{Out: &output}
			if err := a.ExplainSuiteEvaluation(t.Context(), SuiteEvaluationExplainOptions{Workspace: root, ID: id, ShowContent: true}); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(output.String(), "opaque-private-value") || !strings.Contains(output.String(), "rationale: unavailable or withheld") {
				t.Fatal("offline inspection exposed API key")
			}
		})
	}
}

func TestSuiteFailureRetentionUnavailable(t *testing.T) {
	root, d := failureStoreFixture(t)
	var details []suiteFailureDetail
	var out bytes.Buffer
	ctx := withSuiteFailureReporting(context.Background(), &details, &out, false)
	ctx = withSuiteFailureRetention(ctx, root, &workspace.NativeSuite{Digest: d.SuiteDigest, Runtime: workspace.RuntimeProfile{ID: d.Runtime}, MemberID: d.MemberID})
	base := filepath.Join(os.Getenv("KMX_HOME"), "eval-failures")
	if err := os.MkdirAll(base, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(base, 0755); err != nil {
		t.Fatal(err)
	}
	d.ID = "f-" + strings.Repeat("d", 32)
	for i := 0; i < 2; i++ {
		if err := reportSuiteFailure(ctx, &d); err != nil {
			t.Fatal("retention changed evaluation error", err)
		}
	}
	if len(details) != 2 || details[0].ID != "" || !details[0].RetentionUnavailable || strings.Count(out.String(), "warning: local evaluation failure retention unavailable") != 1 {
		t.Fatal("retention error not bounded/disclosed")
	}
}
