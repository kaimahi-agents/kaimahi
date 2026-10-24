package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func TestFileUnpinnedJudgeModelSelectionPreservesSnapshot(t *testing.T) {
	opt := directOptions(t, "unit", true)
	source, err := os.ReadFile(opt.File)
	if err != nil {
		t.Fatal(err)
	}
	source = bytes.ReplaceAll(source, []byte("        judgeModel: unit\n"), nil)
	parsed, err := agentfile.Parse(opt.File, source)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(parsed)
	for _, model := range []string{"model-A", "model-B"} {
		opt.Model = model
		opt.Route = &modelaccess.Route{Provider: "compatible", Model: model, Endpoint: "http://127.0.0.1/v1", Auth: "none"}
		runner := &scriptedPreview{verdict: runtime.EvaluationPass}
		result, err := (&App{FileRunner: runner}).PreviewAgentSnapshot(t.Context(), parsed, opt)
		if err != nil || !result.FullPass {
			t.Fatalf("result=%+v / %v", result, err)
		}
		for i, example := range runner.request.Examples {
			assertion := example.Assertions[0]
			if assertion.JudgeModel != model || assertion.JudgeVersion != "v1" || runtime.ValidateEvaluationAssertions(example.Assertions) != nil {
				t.Fatalf("unbound runner definition: %+v", assertion)
			}
			if runner.request.Agent.Examples[i].Assertions[0].JudgeModel != model {
				t.Fatal("agent/selection definitions differ")
			}
		}
		// Runner-owned nested copies must not alias the caller snapshot.
		*runner.request.Agent.Examples[0].Assertions[0].MinScore = 1
		runner.request.Agent.Examples[0].AssertionLocations[0].Line = 1
		runner.request.Examples[0].Assertions[0].Rubric = "changed"
		after, _ := json.Marshal(parsed)
		if !bytes.Equal(before, after) {
			t.Fatal("preview mutated nested snapshot")
		}
	}
	afterSource, _ := os.ReadFile(opt.File)
	if !strings.Contains(string(afterSource), "judgeModel: unit") {
		t.Fatal("preview edited source")
	}
	opt.JudgeRoute = &modelaccess.Route{Provider: "compatible", Model: "separate-judge", Endpoint: "http://127.0.0.1/v1", Auth: "none"}
	runner := &scriptedPreview{verdict: runtime.EvaluationPass}
	if _, err := (&App{FileRunner: runner}).PreviewAgentSnapshot(t.Context(), parsed, opt); err != nil || runner.request.Examples[0].Assertions[0].JudgeModel != "separate-judge" {
		t.Fatalf("judge override not bound: %v", err)
	}
	pinned := mustPreviewAgent(t, opt.File)
	result, err := (&App{FileRunner: runner}).PreviewAgentSnapshot(t.Context(), pinned, opt)
	if err == nil || result.ReasonCode != "model_unavailable" || runner.calls != 1 {
		t.Fatal("authored pin mismatch reached runner")
	}
}

func TestStandaloneUnpinnedJudgeUsesBoundDefinitions(t *testing.T) {
	agent := localPreviewJudgeAgent(t, "fake", "Greets `Hello, Ada`")
	agent.Examples[0].Assertions[0].JudgeModel = ""
	before, _ := json.Marshal(agent)
	result, err := localpreview.Test(localPreviewJudgeContext(t), agent, localpreview.Options{JournalDir: filepath.Join(t.TempDir(), "j"), Judge: LocalPreviewJudge})
	if err != nil || result.Verdict != runtime.EvaluationPass {
		t.Fatalf("standalone unpinned preview=%+v / %v", result, err)
	}
	row := result.Examples[0].Assertions[0]
	if !runtime.EvaluationJudgeEvidenceValid(row) || row.Judge.Model != "fake" || len(row.Judge.Scores) != 2 {
		t.Fatalf("unbound or silent unknown judge: %+v", row)
	}
	after, _ := json.Marshal(agent)
	if !bytes.Equal(before, after) {
		t.Fatal("standalone preview mutated authored definitions")
	}
}
