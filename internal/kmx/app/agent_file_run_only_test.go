package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func runOnlyOptions(t *testing.T, opt FilePreviewOptions) FilePreviewOptions {
	t.Helper()
	opt.SkipAssertions = true
	return opt
}

func TestFileRunSkipsChecks(t *testing.T) {
	home := filepath.Join(t.TempDir(), "state")
	t.Setenv("KMX_HOME", home)
	path := previewFile(t)
	a := &App{FileRunner: localpreview.NewRunner(localpreview.RunnerOptions{JournalDir: func() (string, error) { dir := filepath.Join(t.TempDir(), "journal"); return dir, nil }})}
	var progress []FilePreviewCaseProgress
	result, err := a.PreviewAgentFile(t.Context(), runOnlyOptions(t, FilePreviewOptions{File: path, Operation: "run", IncludeAnswer: true, RetentionFile: path, CaseFinished: func(p FilePreviewCaseProgress) { progress = append(progress, p) }}))
	if err != nil || result.State != "complete" || result.Verdict != runtime.EvaluationUnknown || result.FullPass || result.ReasonCode != "completion_ready" || result.Retention.Status != "not_requested" {
		t.Fatalf("Run checks: %+v / %v", result, err)
	}
	if len(progress) != 2 {
		t.Fatalf("progress missing: %+v", progress)
	}
	for _, p := range progress {
		if p.Verdict != runtime.EvaluationUnknown || p.FailedAssertionID != "" {
			t.Fatalf("Run progress evaluated checks: %+v", p)
		}
	}
	for _, row := range result.Examples {
		if len(row.Assertions) != 0 || row.Verdict != runtime.EvaluationUnknown || row.Answer == nil || row.ReasonCode != "completion_ready" {
			t.Fatalf("Run row: %+v", row)
		}
	}
	var text bytes.Buffer
	if err := WriteFilePreview(&text, result, "text"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text.String(), "ANSWERED first") || strings.Contains(text.String(), "UNKNOWN first") || strings.Contains(text.String(), "PASS first") || strings.Contains(text.String(), "FAIL first") {
		t.Fatalf("Run row label: %s", text.String())
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("Run wrote retention state: %v", err)
	}
}

func TestFileRunSkipsJudge(t *testing.T) {
	parsed, err := agentfile.Load(previewFile(t))
	if err != nil {
		t.Fatal(err)
	}
	score := 50
	parsed.Examples[0].Assertions = []runtime.EvaluationAssertion{{ID: "foreign", Type: "llmJudge", Rubric: "greets", MinScore: &score, JudgeModel: "foreign", JudgeVersion: "2"}}
	before, _ := json.Marshal(parsed)
	a := &App{FileRunner: localpreview.NewRunner(localpreview.RunnerOptions{JournalDir: func() (string, error) { return filepath.Join(t.TempDir(), "journal"), nil }})}
	unused := modelaccess.Route{Provider: "unsupported"}
	result, err := a.PreviewAgentSnapshot(t.Context(), parsed, runOnlyOptions(t, FilePreviewOptions{File: "agent.md", Operation: "run", JudgeRoute: &unused}))
	if err != nil || result.JudgeRoute != nil || result.State != "complete" {
		t.Fatalf("unused judge refused: %+v / %v", result, err)
	}
	after, _ := json.Marshal(parsed)
	if string(before) != string(after) {
		t.Fatal("source assertions mutated")
	}
}

func TestFileTestStillEvaluatesChecks(t *testing.T) {
	a := &App{FileRunner: localpreview.NewRunner(localpreview.RunnerOptions{JournalDir: func() (string, error) { return filepath.Join(t.TempDir(), "journal"), nil }})}
	result, err := a.PreviewAgentFile(t.Context(), FilePreviewOptions{File: previewFile(t), Operation: "test"})
	if err == nil || result.Verdict != runtime.EvaluationFail || len(result.Examples[0].Assertions) != 1 {
		t.Fatalf("Test skipped checks: %+v / %v", result, err)
	}
}

func TestFileRunRejectsTestSkipBeforeEffects(t *testing.T) {
	runner := &scriptedPreview{verdict: runtime.EvaluationPass}
	a := &App{FileRunner: runner}
	result, err := a.PreviewAgentFile(t.Context(), runOnlyOptions(t, FilePreviewOptions{File: previewFile(t), Operation: "test", PrepareRecordingSave: func(context.Context, string) (RecordingSaver, error) {
		t.Fatal("invalid Test prepared capture")
		return nil, nil
	}, RouteSelected: func(FilePreviewRoute) error { t.Fatal("invalid Test selected route"); return nil }}))
	if err == nil || result.ReasonCode != "invalid_options" || runner.calls != 0 {
		t.Fatalf("invalid Test executed: %+v / %v / %d", result, err, runner.calls)
	}
}

func TestFileRunRejectsInvalidCompletion(t *testing.T) {
	for _, name := range []string{"assertions", "revision", "not-run", "journal"} {
		t.Run(name, func(t *testing.T) {
			runner := &scriptedPreview{verdict: runtime.EvaluationPass, mutate: func(out *agentpreview.Outcome) {
				if name != "assertions" {
					for i := range out.Examples {
						out.Examples[i].Assertions = nil
					}
				}
				switch name {
				case "revision":
					out.Examples[0].Completion.FileRevision = "wrong"
				case "not-run":
					out.Examples[0].Completion.State = agentpreview.NotRun
				case "journal":
					out.Journal = ""
				}
			}}
			result, err := (&App{FileRunner: runner}).PreviewAgentFile(t.Context(), runOnlyOptions(t, FilePreviewOptions{File: previewFile(t), Operation: "run"}))
			if err == nil || result.State == "complete" || result.ReasonCode != "invalid_completion" {
				t.Fatalf("invalid Run accepted: %+v / %v", result, err)
			}
		})
	}
}
