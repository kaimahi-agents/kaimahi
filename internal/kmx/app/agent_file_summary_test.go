package app

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func summaryFixture() FilePreviewResult {
	return FilePreviewResult{
		SchemaVersion: 1, Kind: "kmx.agent-file-preview", Operation: "test",
		File:       FilePreviewIdentity{Path: "PRIVATE.agent.md", Revision: "sha256:" + strings.Repeat("a", 64)},
		ModelRoute: FilePreviewRoute{Provider: "compatible", Model: "model-v1"}, JudgeRoute: &FilePreviewRoute{Provider: "fake", Model: "fake"},
		Selection: FilePreviewSelection{Scope: "full", CaseIDs: []string{"greeting"}, TotalCases: 1},
		State:     "complete", Verdict: runtime.EvaluationFail, FullPass: false, ReasonCode: "assertion_failed", Reason: "PRIVATE REASON", DurationMs: 2345,
		Examples: []FilePreviewExample{{ID: "greeting", FilePreviewCompletion: FilePreviewCompletion{
			State: "complete", ReasonCode: "assertion_failed", Reason: "PRIVATE COMPLETION", DurationMs: 1234, AnswerExcerpt: "PRIVATE ANSWER", ToolCalls: 1,
			Tools: []agentpreview.ToolSummary{{Name: "lookup", Mode: "recorded", Source: "recorded", CallCount: 1}},
		}, Verdict: runtime.EvaluationFail, Assertions: []FilePreviewAssertion{{ID: "greeting-text", Type: "contains", Verdict: runtime.EvaluationFail, ReasonCode: "assertion_failed", Reason: "PRIVATE ASSERTION"}}}},
		Retention:    FilePreviewRetention{Status: "retained", Diagnostics: []FilePreviewRetainedDiagnostic{{ID: "f-" + strings.Repeat("b", 32), CaseID: "greeting"}}},
		ToolSettings: []localtools.ToolSetting{{Name: "lookup", State: localtools.Enabled}},
	}
}

func TestFilePreviewGitHubSummaryIsContentFreeAndComplete(t *testing.T) {
	var output bytes.Buffer
	if err := WriteFilePreviewGitHubSummary(&output, summaryFixture()); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{
		"## kmx test: FAIL", "sha256:" + strings.Repeat("a", 64), "compatible/model-v1", "fake/fake",
		"`full` (1 selected / 1 authored)", "2.345 s", "`greeting`", "1.234 s", "`greeting-text`", "`contains`",
		"`lookup`", "`Enabled`", "`recorded`", "| 1 |", "not suite evidence",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q:\n%s", want, text)
		}
	}
	for _, forbidden := range []string{"PRIVATE", "f-" + strings.Repeat("b", 32), "agent.md", "ANSWER", "OPERAND", "input", "endpoint"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("summary disclosed %q:\n%s", forbidden, text)
		}
	}
}

func TestFilePreviewGitHubSummaryRefusesUntrustedProjection(t *testing.T) {
	base := summaryFixture()
	cases := map[string]func(*FilePreviewResult){
		"schema":        func(r *FilePreviewResult) { r.SchemaVersion = 2 },
		"operation":     func(r *FilePreviewResult) { r.Operation = "run" },
		"revision":      func(r *FilePreviewResult) { r.File.Revision = "PRIVATE" },
		"provider":      func(r *FilePreviewResult) { r.ModelRoute.Provider = "PRIVATE" },
		"model":         func(r *FilePreviewResult) { r.ModelRoute.Model = "PRIVATE|MODEL" },
		"scope":         func(r *FilePreviewResult) { r.Selection.Scope = "PRIVATE" },
		"case":          func(r *FilePreviewResult) { r.Examples[0].ID = "PRIVATE|CASE" },
		"assertion":     func(r *FilePreviewResult) { r.Examples[0].Assertions[0].ID = "PRIVATE|CHECK" },
		"reason":        func(r *FilePreviewResult) { r.Examples[0].Assertions[0].ReasonCode = "PRIVATE" },
		"reason parity": func(r *FilePreviewResult) { r.Examples[0].Assertions[0].Verdict = runtime.EvaluationPass },
		"duration":      func(r *FilePreviewResult) { r.DurationMs = -1 },
		"tool":          func(r *FilePreviewResult) { r.Examples[0].Tools[0].Name = "PRIVATE|TOOL" },
		"tool count":    func(r *FilePreviewResult) { r.Examples[0].ToolCalls = 0 },
		"save without acknowledgment": func(r *FilePreviewResult) {
			r.SavedRevision = "sha256:" + strings.Repeat("b", 64)
		},
		"acknowledgment without save": func(r *FilePreviewResult) {
			r.Examples[0].Tools[0].RecordingSaved = true
		},
		"full pass": func(r *FilePreviewResult) { r.FullPass = true },
		"case duplicate": func(r *FilePreviewResult) {
			r.Selection.CaseIDs = []string{"greeting", "greeting"}
			r.Examples = append(r.Examples, r.Examples[0])
			r.Selection.TotalCases = 2
		},
		"case verdict": func(r *FilePreviewResult) { r.Examples[0].Verdict = runtime.EvaluationPass },
		"case reason":  func(r *FilePreviewResult) { r.Examples[0].ReasonCode = "completion_ready" },
		"check duplicate": func(r *FilePreviewResult) {
			r.Examples[0].Assertions = append(r.Examples[0].Assertions, r.Examples[0].Assertions[0])
		},
		"missing judge":       func(r *FilePreviewResult) { r.JudgeRoute = nil; r.Examples[0].Assertions[0].Type = "llmJudge" },
		"missing model":       func(r *FilePreviewResult) { r.ModelRoute = FilePreviewRoute{} },
		"nonterminal verdict": func(r *FilePreviewResult) { r.State = "error" },
		"duration relation":   func(r *FilePreviewResult) { r.DurationMs = 1 },
		"missing tool row":    func(r *FilePreviewResult) { r.Examples[0].Tools = nil; r.Examples[0].ToolCalls = 0 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			result := base
			result.Examples = append([]FilePreviewExample(nil), base.Examples...)
			result.Examples[0].Assertions = append([]FilePreviewAssertion(nil), base.Examples[0].Assertions...)
			result.Examples[0].Tools = append([]agentpreview.ToolSummary(nil), base.Examples[0].Tools...)
			result.Selection.CaseIDs = append([]string(nil), base.Selection.CaseIDs...)
			mutate(&result)
			var output bytes.Buffer
			if err := WriteFilePreviewGitHubSummary(&output, result); err == nil || output.Len() != 0 {
				t.Fatalf("untrusted projection accepted: %v / %q", err, output.String())
			}
		})
	}
}

func TestFilePreviewGitHubSummaryAllowsSavedRecording(t *testing.T) {
	result := summaryFixture()
	result.SavedRevision = "sha256:" + strings.Repeat("b", 64)
	tool := &result.Examples[0].Tools[0]
	tool.Mode, tool.Source, tool.RecordingSaved = "recorded", "live", true
	var output bytes.Buffer
	if err := WriteFilePreviewGitHubSummary(&output, result); err != nil || !strings.Contains(output.String(), "`live`") || strings.Contains(output.String(), result.SavedRevision) {
		t.Fatalf("saved recording summary: %v\n%s", err, output.String())
	}
	for name, mutate := range map[string]func(*FilePreviewResult){
		"recorded source": func(r *FilePreviewResult) { r.Examples[0].Tools[0].Source = "recorded" },
		"live mode":       func(r *FilePreviewResult) { r.Examples[0].Tools[0].Mode = "live" },
		"no call":         func(r *FilePreviewResult) { r.Examples[0].Tools[0].CallCount, r.Examples[0].ToolCalls = 0, 0 },
		"same revision":   func(r *FilePreviewResult) { r.SavedRevision = r.File.Revision },
	} {
		t.Run(name, func(t *testing.T) {
			bad := result
			bad.Examples = append([]FilePreviewExample(nil), result.Examples...)
			bad.Examples[0].Tools = append([]agentpreview.ToolSummary(nil), result.Examples[0].Tools...)
			mutate(&bad)
			var rejected bytes.Buffer
			if err := WriteFilePreviewGitHubSummary(&rejected, bad); err == nil || rejected.Len() != 0 {
				t.Fatalf("invalid saved recording accepted: %v / %q", err, rejected.String())
			}
		})
	}
}

func TestFilePreviewGitHubSummaryBoundsRows(t *testing.T) {
	result := summaryFixture()
	result.ToolSettings = nil
	result.Selection.CaseIDs = nil
	result.Examples = nil
	for i := 0; i < 9; i++ {
		id := fmt.Sprintf("case-%02d", i)
		example := FilePreviewExample{ID: id, FilePreviewCompletion: FilePreviewCompletion{State: "complete", ReasonCode: "checks_passed"}, Verdict: runtime.EvaluationPass}
		for j := 0; j < runtime.EvaluationMaxAssertions; j++ {
			example.Assertions = append(example.Assertions, FilePreviewAssertion{ID: fmt.Sprintf("c%02d-%02d", i, j), Type: "contains", Verdict: runtime.EvaluationPass, ReasonCode: "assertion_satisfied"})
		}
		result.Selection.CaseIDs = append(result.Selection.CaseIDs, id)
		result.Examples = append(result.Examples, example)
	}
	result.Selection.TotalCases = len(result.Examples)
	var output bytes.Buffer
	omitted := len(result.Examples)*runtime.EvaluationMaxAssertions - fileSummaryMaxCheckRows
	if err := WriteFilePreviewGitHubSummary(&output, result); err != nil || !strings.Contains(output.String(), fmt.Sprintf("%d additional check row(s)", omitted)) {
		t.Fatalf("row bound missing: %v\n%s", err, output.String())
	}
}

func TestFilePreviewGitHubSummaryAllowsSafeEarlyRefusal(t *testing.T) {
	result := FilePreviewResult{
		SchemaVersion: 1, Kind: "kmx.agent-file-preview", Operation: "test",
		File:      FilePreviewIdentity{Path: "PRIVATE.agent.md", Revision: "sha256:" + strings.Repeat("a", 64)},
		Selection: FilePreviewSelection{Scope: "full", CaseIDs: []string{}, TotalCases: 2},
		State:     "refused", Verdict: runtime.EvaluationUnknown, ReasonCode: "model_unavailable", Reason: "PRIVATE", DurationMs: 1,
		Examples: []FilePreviewExample{}, Retention: FilePreviewRetention{Status: "not_requested"},
	}
	var output bytes.Buffer
	if err := WriteFilePreviewGitHubSummary(&output, result); err != nil || !strings.Contains(output.String(), "model_unavailable") || strings.Contains(output.String(), "PRIVATE") {
		t.Fatalf("safe refusal summary: %v\n%s", err, output.String())
	}
}

func TestFilePreviewGitHubSummaryAllowsClosedRedactedRefusal(t *testing.T) {
	for _, code := range []string{"unsafe_tool_content", "credential_content"} {
		t.Run(code, func(t *testing.T) {
			result := FilePreviewResult{
				SchemaVersion: 1, Kind: "kmx.agent-file-preview", Operation: "test",
				File: FilePreviewIdentity{Path: "[agent file]"}, Selection: FilePreviewSelection{CaseIDs: []string{}},
				State: "refused", Verdict: runtime.EvaluationUnknown, ReasonCode: code, Reason: "PRIVATE", Examples: []FilePreviewExample{},
				Retention: FilePreviewRetention{Status: "not_requested"},
			}
			var output bytes.Buffer
			if err := WriteFilePreviewGitHubSummary(&output, result); err != nil || !strings.Contains(output.String(), "`unavailable`") || !strings.Contains(output.String(), code) || strings.Contains(output.String(), "PRIVATE") || strings.Contains(output.String(), "selected / ") {
				t.Fatalf("redacted refusal summary: %v\n%s", err, output.String())
			}
		})
	}
}
