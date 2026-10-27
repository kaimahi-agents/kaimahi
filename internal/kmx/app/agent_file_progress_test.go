package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// Emits private runner rows to exercise App's observer boundary, not execution.
type progressPreview struct{ scriptedPreview }

func (f *progressPreview) Run(ctx context.Context, req agentpreview.Request) (agentpreview.Outcome, error) {
	out, err := f.scriptedPreview.Run(ctx, req)
	if req.ExampleFinished != nil {
		for _, row := range out.Examples {
			req.ExampleFinished(row)
		}
	}
	return out, err
}

func TestFileCaseProgressSafeProjectionAndFinalParity(t *testing.T) {
	for _, tc := range []struct {
		verdict runtime.EvaluationVerdict
		reason  string
	}{
		{runtime.EvaluationPass, ""}, {runtime.EvaluationFail, "Assertion not satisfied."}, {runtime.EvaluationUnknown, "Assertion outcome is inconclusive."},
	} {
		t.Run(string(tc.verdict), func(t *testing.T) {
			runner := &progressPreview{scriptedPreview{verdict: tc.verdict}}
			application := &App{FileRunner: runner}
			opt := FilePreviewOptions{File: previewFile(t), Operation: "test", ShowContent: true}
			baseline, baselineErr := application.PreviewAgentFile(t.Context(), opt)
			var rows []FilePreviewCaseProgress
			opt.CaseFinished = func(row FilePreviewCaseProgress) { rows = append(rows, row) }
			result, err := application.PreviewAgentFile(t.Context(), opt)
			baseline.DurationMs = 0
			for i := range baseline.Examples {
				baseline.Examples[i].DurationMs = 0
			}
			result.DurationMs = 0
			for i := range result.Examples {
				result.Examples[i].DurationMs = 0
			}
			before, _ := json.Marshal(baseline)
			after, _ := json.Marshal(result)
			if !bytes.Equal(before, after) || (baselineErr == nil) != (err == nil) {
				t.Fatal("observer changed final projection")
			}
			if len(rows) != 2 {
				t.Fatalf("progress: %+v", rows)
			}
			for i, row := range rows {
				if row.CaseID != result.Examples[i].ID || row.Verdict != tc.verdict || row.Verdict != result.Examples[i].Verdict || row.Reason != tc.reason {
					t.Fatalf("unsafe/mismatched progress: %+v", row)
				}
			}
		})
	}
}

func TestFileCaseProgressFirstFailingReasonInAuthoredOrder(t *testing.T) {
	file := previewFile(t)
	source, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	source = []byte(strings.ReplaceAll(string(source), "        value: PRIVATE OPERAND", "        value: PRIVATE OPERAND\n      - id: other\n        type: contains\n        value: PRIVATE SECOND OPERAND"))
	if err := os.WriteFile(file, source, 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		first, second, aggregate runtime.EvaluationVerdict
		reason                   string
	}{
		{runtime.EvaluationPass, runtime.EvaluationFail, runtime.EvaluationFail, "Assertion not satisfied."},
		{runtime.EvaluationUnknown, runtime.EvaluationFail, runtime.EvaluationFail, "Assertion not satisfied."},
		{runtime.EvaluationFail, runtime.EvaluationUnknown, runtime.EvaluationFail, "Assertion not satisfied."},
		{runtime.EvaluationPass, runtime.EvaluationUnknown, runtime.EvaluationUnknown, "Assertion outcome is inconclusive."},
	} {
		t.Run(string(tc.first)+"/"+string(tc.second), func(t *testing.T) {
			runner := &progressPreview{scriptedPreview{verdict: runtime.EvaluationPass, mutate: func(out *agentpreview.Outcome) {
				for i := range out.Examples {
					out.Examples[i].Assertions[0].Verdict = tc.first
					out.Examples[i].Assertions[1].Verdict = tc.second
				}
			}}}
			var rows []FilePreviewCaseProgress
			result, _ := (&App{FileRunner: runner}).PreviewAgentFile(t.Context(), FilePreviewOptions{File: file, Operation: "test", CaseFinished: func(row FilePreviewCaseProgress) { rows = append(rows, row) }})
			if len(rows) != 2 {
				t.Fatalf("progress: %+v", rows)
			}
			for i, row := range rows {
				if row.Verdict != tc.aggregate || row.Reason != tc.reason || row.Verdict != result.Examples[i].Verdict {
					t.Fatalf("check order/aggregate lost: %+v", row)
				}
			}
		})
	}
}

func TestFileCaseProgressIdentifiesFirstFailedTextCheck(t *testing.T) {
	agent, err := agentfile.Load(previewFile(t))
	if err != nil {
		t.Fatal(err)
	}
	checks := agent.Examples[0].Assertions
	judge := checks[0]
	judge.ID, judge.Type = "judge", "llmJudge"
	agent.Examples[0].Assertions = append([]runtime.EvaluationAssertion{judge}, checks...)
	selected := agent.Examples[:1]
	route := modelaccess.Route{Provider: "fake", Model: "fake"}
	runner := &scriptedPreview{verdict: runtime.EvaluationFail}
	out, err := runner.Run(t.Context(), agentpreview.Request{Agent: agent, Examples: selected, Route: route})
	if err != nil {
		t.Fatal(err)
	}
	var rows []FilePreviewCaseProgress
	observe := previewCaseObserver(t.Context(), selected, agent, route, func(row FilePreviewCaseProgress) { rows = append(rows, row) }, false)
	observe(out.Examples[0])
	encoded, _ := json.Marshal(rows)
	var got []map[string]any
	if err := json.Unmarshal(encoded, &got); err != nil || len(got) != 1 || got[0]["failedAssertionId"] != "text" {
		t.Fatalf("live reason cannot identify the first failed text check: %s", encoded)
	}
	if strings.Contains(string(encoded), "PRIVATE") {
		t.Fatalf("progress leaked source or answer: %s", encoded)
	}
}

func TestFileCaseProgressRejectsMalformedRows(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*agentpreview.Outcome)
	}{
		{"case", func(o *agentpreview.Outcome) { o.Examples[0].ID = "wrong" }},
		{"revision", func(o *agentpreview.Outcome) { o.Examples[0].Completion.FileRevision = "wrong" }},
		{"route", func(o *agentpreview.Outcome) { o.Examples[0].Completion.Model = "wrong" }},
		{"unfinished", func(o *agentpreview.Outcome) { o.Examples[0].Completion.State = agentpreview.NotRun }},
		{"assertion", func(o *agentpreview.Outcome) { o.Examples[0].Assertions[0].ID = "wrong" }},
		{"definition", func(o *agentpreview.Outcome) { o.Examples[0].Assertions[0].DefinitionDigest = "wrong" }},
		{"verdict", func(o *agentpreview.Outcome) { o.Examples[0].Assertions[0].Verdict = "wrong" }},
		{"count", func(o *agentpreview.Outcome) { o.Examples[0].Assertions = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			application := &App{FileRunner: &progressPreview{scriptedPreview{verdict: runtime.EvaluationPass, mutate: tc.mutate}}}
			rows := 0
			result, err := application.PreviewAgentFile(t.Context(), FilePreviewOptions{File: previewFile(t), Operation: "test", CaseFinished: func(FilePreviewCaseProgress) { rows++ }})
			if rows != 0 || err == nil || result.FullPass || result.ReasonCode != "invalid_completion" {
				t.Fatalf("malformed row escaped: count=%d result=%+v err=%v", rows, result, err)
			}
		})
	}
}

func TestFileCaseProgressIsProvisionalOnJournalFailure(t *testing.T) {
	runner := &progressPreview{scriptedPreview{verdict: runtime.EvaluationPass, err: agentpreview.ErrJournalFailed}}
	rows := 0
	result, err := (&App{FileRunner: runner}).PreviewAgentFile(t.Context(), FilePreviewOptions{File: previewFile(t), Operation: "test", CaseFinished: func(FilePreviewCaseProgress) { rows++ }})
	var failure *FilePreviewError
	if rows != 2 || result.FullPass || result.ReasonCode != "journal_failed" || !errors.As(err, &failure) {
		t.Fatalf("progress overruled journal failure: rows=%d result=%+v err=%v", rows, result, err)
	}
}
