package app

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

const (
	fileSummaryMaxCheckRows = 512
	fileSummaryMaxToolRows  = 512
)

var errInvalidFilePreviewSummary = errors.New("invalid file preview summary")

// WriteFilePreviewGitHubSummary appends a bounded content-free Markdown view of
// the same safe result returned by file Test. It is not evidence or history.
func WriteFilePreviewGitHubSummary(w io.Writer, result FilePreviewResult) error {
	return writeFilePreviewGitHubSummary(w, result, 0)
}

// WriteFilePreviewGitHubSection reuses the validated projection under a folder's
// single heading/footer. Only an ordinal, never a path, identifies the section.
func WriteFilePreviewGitHubSection(w io.Writer, result FilePreviewResult, ordinal int) error {
	if ordinal < 1 {
		return errInvalidFilePreviewSummary
	}
	return writeFilePreviewGitHubSummary(w, result, ordinal)
}

func writeFilePreviewGitHubSummary(w io.Writer, result FilePreviewResult, ordinal int) error {
	if w == nil || !validFilePreviewSummary(result) {
		return errInvalidFilePreviewSummary
	}
	var out strings.Builder
	level := "###"
	if ordinal == 0 {
		fmt.Fprintf(&out, "## kmx test: %s\n\n", strings.ToUpper(string(result.Verdict)))
	} else {
		fmt.Fprintf(&out, "### File %d: %s\n\n", ordinal, strings.ToUpper(string(result.Verdict)))
		level = "####"
	}
	fmt.Fprintln(&out, "| Field | Value |")
	fmt.Fprintln(&out, "|---|---|")
	fmt.Fprintf(&out, "| State | `%s` |\n", result.State)
	fmt.Fprintf(&out, "| Revision | `%s` |\n", summaryValue(result.File.Revision))
	if result.SnapshotRevision != "" {
		fmt.Fprintf(&out, "| Snapshot | `%s` |\n", result.SnapshotRevision)
	}
	if result.Tests != nil {
		fmt.Fprintf(&out, "| Tests source | `%s` |\n| Tests revision | `%s` |\n", result.Tests.State, summaryValue(result.Tests.Revision))
	}
	fmt.Fprintf(&out, "| Model | `%s` |\n", summaryRoute(result.ModelRoute))
	judge := "not used"
	if result.JudgeRoute != nil {
		judge = summaryRoute(*result.JudgeRoute)
	}
	fmt.Fprintf(&out, "| Judge | `%s` |\n", judge)
	if result.Selection.Scope == "" {
		fmt.Fprintf(&out, "| Scope | `%s` |\n", summaryValue(result.Selection.Scope))
	} else {
		fmt.Fprintf(&out, "| Scope | `%s` (%d selected / %d authored) |\n", result.Selection.Scope, len(result.Selection.CaseIDs), result.Selection.TotalCases)
	}
	fmt.Fprintf(&out, "| Duration | %s |\n", summaryDuration(result.DurationMs))
	fmt.Fprintf(&out, "| Reason | `%s`: %s |\n", result.ReasonCode, summaryReason(result.ReasonCode))

	if len(result.Examples) > 0 {
		fmt.Fprintf(&out, "\n%s Tests\n\n", level)
		fmt.Fprintln(&out, "| Test | Verdict | Duration | Reason |")
		fmt.Fprintln(&out, "|---|---|---:|---|")
		for _, example := range result.Examples {
			fmt.Fprintf(&out, "| `%s` | `%s` | %s | `%s`: %s |\n", example.ID, example.Verdict, summaryDuration(example.DurationMs), example.ReasonCode, summaryReason(example.ReasonCode))
		}

		fmt.Fprintf(&out, "\n%s Checks\n\n", level)
		fmt.Fprintln(&out, "| Test | Check | Type | Verdict | Reason |")
		fmt.Fprintln(&out, "|---|---|---|---|---|")
		written, total := 0, 0
		for _, example := range result.Examples {
			for _, assertion := range example.Assertions {
				total++
				if written == fileSummaryMaxCheckRows {
					continue
				}
				fmt.Fprintf(&out, "| `%s` | `%s` | `%s` | `%s` | `%s`: %s |\n", example.ID, assertion.ID, assertion.Type, assertion.Verdict, assertion.ReasonCode, summaryReason(assertion.ReasonCode))
				written++
			}
		}
		if omitted := total - written; omitted > 0 {
			fmt.Fprintf(&out, "\n%d additional check row(s) are available in the JSON report.\n", omitted)
		}
	}

	settings := make(map[string]localtools.ToolState, len(result.ToolSettings))
	for _, setting := range result.ToolSettings {
		settings[setting.Name] = setting.State
	}
	toolRows := 0
	for _, example := range result.Examples {
		toolRows += len(example.Tools)
	}
	if toolRows > 0 {
		fmt.Fprintf(&out, "\n%s Tools\n\n", level)
		fmt.Fprintln(&out, "| Test | Tool | Setting | Mode | Source | Calls |")
		fmt.Fprintln(&out, "|---|---|---|---|---|---:|")
		written := 0
		unavailable := false
		for _, example := range result.Examples {
			for _, tool := range example.Tools {
				if written == fileSummaryMaxToolRows {
					continue
				}
				state := settings[tool.Name]
				if state == "" {
					state = localtools.Enabled
				}
				fmt.Fprintf(&out, "| `%s` | `%s` | `%s` | `%s` | `%s` | %d |\n", example.ID, tool.Name, state, tool.Mode, tool.Source, tool.CallCount)
				unavailable = unavailable || fileToolUnavailable(tool, result.ToolSettings)
				written++
			}
		}
		if omitted := toolRows - written; omitted > 0 {
			fmt.Fprintf(&out, "\n%d additional tool row(s) are available in the JSON report.\n", omitted)
		}
		if unavailable {
			fmt.Fprintln(&out, "\nUnavailable tools have no recording or live source for this test. Calls count requests, not successful live execution.")
		}
	}

	if ordinal == 0 {
		fmt.Fprintln(&out, "\n> Local preview report only. It is not suite evidence, publication authority, or deployment authority.")
	}
	_, err := io.WriteString(w, out.String())
	return err
}

func validFilePreviewSummary(result FilePreviewResult) bool {
	if result.SchemaVersion != 1 || result.Kind != "kmx.agent-file-preview" || result.Operation != "test" || !summaryState(result.State) || !summaryVerdict(result.Verdict) || !summaryReasonCode(result.ReasonCode) || !summaryMilliseconds(result.DurationMs) {
		return false
	}
	if result.State == "complete" && result.Verdict == runtime.EvaluationPass && result.ReasonCode != "checks_passed" || result.State == "complete" && result.Verdict == runtime.EvaluationFail && result.ReasonCode != "assertion_failed" || result.State == "complete" && result.Verdict == runtime.EvaluationUnknown && result.ReasonCode != "judge_unknown" {
		return false
	}
	if result.State != "complete" && (result.Verdict != runtime.EvaluationUnknown || result.ReasonCode == "checks_passed" || result.ReasonCode == "assertion_failed" || result.ReasonCode == "judge_unknown" || result.ReasonCode == "completion_ready") {
		return false
	}
	if result.FullPass != (result.State == "complete" && result.Verdict == runtime.EvaluationPass && result.Selection.Scope == "full") || result.AdHoc != nil {
		return false
	}
	if result.File.Revision != "" && (!strings.HasPrefix(result.File.Revision, "sha256:") || !verificationDigest(strings.TrimPrefix(result.File.Revision, "sha256:"))) {
		return false
	}
	if !validPairPreviewMetadata(result) {
		return false
	}
	savedRevision := result.SavedRevision != ""
	if savedRevision && (result.State != "complete" || result.SnapshotRevision == "" && result.SavedRevision == result.File.Revision || !strings.HasPrefix(result.SavedRevision, "sha256:") || !verificationDigest(strings.TrimPrefix(result.SavedRevision, "sha256:"))) {
		return false
	}
	if !summaryRouteValid(result.ModelRoute, true) || result.State == "complete" && result.ModelRoute.Provider == "" || result.JudgeRoute != nil && !summaryRouteValid(*result.JudgeRoute, false) {
		return false
	}
	redactedReason := result.ReasonCode == "unsafe_tool_content" || result.ReasonCode == "credential_content"
	redacted := redactedFilePreviewSummary(result)
	if redactedReason != redacted {
		return false
	}
	if !redacted && (result.Selection.Scope != "full" && result.Selection.Scope != "subset" || result.Selection.TotalCases < 0 || result.Selection.TotalCases > agentfile.MaxExamples || len(result.Selection.CaseIDs) > agentfile.MaxExamples || len(result.Examples) > len(result.Selection.CaseIDs) || result.Selection.TotalCases < len(result.Selection.CaseIDs)) {
		return false
	}
	if result.State == "complete" && (len(result.Examples) != len(result.Selection.CaseIDs) || result.Selection.Scope == "full" && len(result.Selection.CaseIDs) != result.Selection.TotalCases) {
		return false
	}
	seenCases := map[string]bool{}
	for i, id := range result.Selection.CaseIDs {
		if !evaluationEvidenceID.MatchString(id) || seenCases[id] || i < len(result.Examples) && result.Examples[i].ID != id {
			return false
		}
		seenCases[id] = true
	}
	settings := map[string]bool{}
	if len(result.ToolSettings) > 32 {
		return false
	}
	for _, setting := range result.ToolSettings {
		if !localtools.ValidToolName(setting.Name) || settings[setting.Name] || setting.State != localtools.Enabled && setting.State != localtools.Disabled {
			return false
		}
		settings[setting.Name] = true
	}
	if len(result.Examples) > agentfile.MaxExamples {
		return false
	}
	recordingsSaved := 0
	for _, example := range result.Examples {
		if !evaluationEvidenceID.MatchString(example.ID) || !summaryState(example.State) || !summaryVerdict(example.Verdict) || !summaryReasonCode(example.ReasonCode) || !summaryMilliseconds(example.DurationMs) || len(example.Assertions) > runtime.EvaluationMaxAssertions {
			return false
		}
		if example.State == "complete" && example.ReasonCode != previewResultReasonCode(example.Verdict) || example.State != "complete" && example.DurationMs != 0 {
			return false
		}
		if example.State != "complete" && (example.Verdict != runtime.EvaluationUnknown || example.ReasonCode != "not_run") || example.State == "complete" && example.Verdict != summaryAssertionsVerdict(example.Assertions) {
			return false
		}
		seenTools := map[string]bool{}
		if len(example.Tools) > 32 {
			return false
		}
		if example.State == "complete" && len(settings) > 0 && len(example.Tools) != len(settings) {
			return false
		}
		calls := 0
		for _, tool := range example.Tools {
			if !localtools.ValidToolName(tool.Name) || seenTools[tool.Name] || len(settings) > 0 && !settings[tool.Name] || tool.Mode != "live" && tool.Mode != "recorded" || tool.Source != "live" && tool.Source != "recorded" && tool.Source != "unavailable" || tool.Mode == "live" && tool.Source == "recorded" || tool.CallCount < 0 || tool.CallCount > localtools.MaxCalls {
				return false
			}
			if tool.RecordingSaved {
				if !savedRevision || tool.Mode != "recorded" || tool.Source != "live" || tool.CallCount == 0 {
					return false
				}
				recordingsSaved++
			}
			seenTools[tool.Name] = true
			calls += tool.CallCount
		}
		if calls != example.ToolCalls || calls > localtools.MaxCalls {
			return false
		}
		seenAssertions := map[string]bool{}
		for _, assertion := range example.Assertions {
			if !evaluationEvidenceID.MatchString(assertion.ID) || seenAssertions[assertion.ID] || !summaryAssertionType(assertion.Type) || !summaryVerdict(assertion.Verdict) || !summaryReasonCode(assertion.ReasonCode) {
				return false
			}
			seenAssertions[assertion.ID] = true
			if assertion.Verdict == runtime.EvaluationPass && assertion.ReasonCode != "assertion_satisfied" || assertion.Verdict == runtime.EvaluationFail && assertion.ReasonCode != "assertion_failed" || assertion.Verdict == runtime.EvaluationUnknown && assertion.ReasonCode != "judge_unknown" && assertion.ReasonCode != "not_run" {
				return false
			}
		}
		if example.State == "complete" && result.DurationMs < example.DurationMs {
			return false
		}
	}
	if savedRevision != (recordingsSaved > 0) {
		return false
	}
	if summaryUsesJudge(result) && result.JudgeRoute == nil {
		return false
	}
	return true
}

func redactedFilePreviewSummary(result FilePreviewResult) bool {
	return result.State == "refused" && (result.ReasonCode == "unsafe_tool_content" || result.ReasonCode == "credential_content") &&
		result.SnapshotRevision == "" && result.Tests == nil && result.RecordingTarget == nil && result.SavedSnapshotRevision == "" && result.SavedRevision == "" &&
		result.Runner == "" && result.File == (FilePreviewIdentity{Path: "[agent file]"}) && result.ModelRoute == (FilePreviewRoute{}) && result.JudgeRoute == nil && result.Diagnostic == nil &&
		result.Selection.Scope == "" && len(result.Selection.CaseIDs) == 0 && result.Selection.TotalCases == 0 && len(result.Examples) == 0 && result.AdHoc == nil &&
		len(result.ToolSettings) == 0 && result.Retention.Status == "not_requested" && len(result.Retention.Diagnostics) == 0
}

func summaryAssertionsVerdict(assertions []FilePreviewAssertion) runtime.EvaluationVerdict {
	verdict := runtime.EvaluationPass
	for _, assertion := range assertions {
		if assertion.Verdict == runtime.EvaluationFail {
			return runtime.EvaluationFail
		}
		if assertion.Verdict == runtime.EvaluationUnknown {
			verdict = runtime.EvaluationUnknown
		}
	}
	return verdict
}

func summaryUsesJudge(result FilePreviewResult) bool {
	for _, example := range result.Examples {
		for _, assertion := range example.Assertions {
			if assertion.Type == "llmJudge" {
				return true
			}
		}
	}
	return false
}

func summaryRouteValid(route FilePreviewRoute, optional bool) bool {
	if route.Provider == "" || route.Model == "" {
		return optional && route.Provider == "" && route.Model == ""
	}
	switch route.Provider {
	case "fake", "ollama", "foundry-local", "compatible", "copilot":
	default:
		return false
	}
	return agentfile.ValidModel(route.Model)
}

func summaryState(state string) bool {
	return state == "refused" || state == "error" || state == "cancelled" || state == "complete" || state == "not_run"
}

func summaryVerdict(verdict runtime.EvaluationVerdict) bool {
	return verdict == runtime.EvaluationPass || verdict == runtime.EvaluationFail || verdict == runtime.EvaluationUnknown
}

func summaryReasonCode(code string) bool { return code != "" && previewReason(code) != "" }

func summaryAssertionType(kind string) bool {
	switch kind {
	case "contains", "notContains", "regex", "llmJudge", "toolCalled", "toolNotCalled":
		return true
	default:
		return false
	}
}

func summaryMilliseconds(value int64) bool {
	return value >= 0 && value <= (10*time.Minute).Milliseconds()
}

func summaryValue(value string) string {
	if value == "" {
		return "unavailable"
	}
	return value
}

func summaryRoute(route FilePreviewRoute) string {
	if route.Provider == "" {
		return "unavailable"
	}
	return route.Provider + "/" + route.Model
}

func summaryDuration(milliseconds int64) string {
	if milliseconds < 1000 {
		return fmt.Sprintf("%d ms", milliseconds)
	}
	return fmt.Sprintf("%.3f s", float64(milliseconds)/1000)
}

func summaryReason(code string) string {
	if reason := previewReason(code); reason != "" {
		return reason
	}
	return "Unavailable."
}
