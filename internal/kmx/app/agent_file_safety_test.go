package app

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func TestFilePreviewRootedSnapshotDoesNotReopen(t *testing.T) {
	for _, operation := range []string{"run", "test"} {
		t.Run(operation, func(t *testing.T) {
			path := previewFile(t)
			snapshot, err := agentfile.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			fake := &scriptedPreview{verdict: runtime.EvaluationPass}
			a := &App{FileRunner: fake}
			opt := FilePreviewOptions{File: path, Operation: operation}
			for _, removed := range []bool{false, true} {
				if removed {
					err = os.Remove(path)
				} else {
					err = os.WriteFile(path, []byte("replacement is not the original agent"), 0600)
				}
				if err != nil {
					t.Fatal(err)
				}
				result, err := a.PreviewAgentSnapshot(context.Background(), snapshot, opt)
				if err != nil || !result.FullPass || result.File.Revision != snapshot.Revision || fake.request.Agent.Instructions != snapshot.Instructions {
					t.Fatalf("snapshot replaced: %+v / %v", result, err)
				}
			}
			if fake.calls != 2 {
				t.Fatal("snapshot operation resent or skipped execution")
			}
			result, err := a.PreviewAgentFile(context.Background(), opt)
			if err == nil || result.ReasonCode != "invalid_file" || fake.calls != 2 {
				t.Fatal("path wrapper did not load its own file")
			}
			result, err = a.PreviewAgentSnapshot(context.Background(), nil, opt)
			if err == nil || result.ReasonCode != "invalid_file" || fake.calls != 2 {
				t.Fatal("nil snapshot accepted")
			}
		})
	}
}

func TestFilePreviewParserDiagnostic(t *testing.T) {
	path := previewFile(t)
	if err := os.WriteFile(path, []byte("---\nmodel: [\n---\nReview the changes.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f := &scriptedPreview{}
	r, err := (&App{FileRunner: f}).PreviewAgentFile(context.Background(), FilePreviewOptions{File: path, Operation: "test"})
	if err == nil || f.calls != 0 || r.Diagnostic == nil || r.Diagnostic.Field != "Frontmatter" || r.Diagnostic.Line < 1 {
		t.Fatalf("%+v / %v", r, err)
	}
}
func TestFilePreviewContentBoundAndInvalidCompletion(t *testing.T) {
	for _, size := range []int{4096, 4097, 256 * 1024, 256*1024 + 1} {
		f := &scriptedPreview{verdict: runtime.EvaluationPass, mutate: func(o *agentpreview.Outcome) { o.Examples[0].Completion.Answer = strings.Repeat("x", size) }}
		r, err := (&App{FileRunner: f}).PreviewAgentFile(context.Background(), FilePreviewOptions{File: previewFile(t), Operation: "test", ShowContent: true})
		if size > 256*1024 {
			if err == nil || r.FullPass {
				t.Fatal("oversized completion passed")
			}
			continue
		}
		if err != nil || len(r.Examples[0].AnswerExcerpt) > 4096 || r.Examples[0].Truncated != (size > 4096) {
			t.Fatalf("size %d / %+v / %v", size, r.Examples[0], err)
		}
	}
}
func TestFilePreviewCancelledBeforeSend(t *testing.T) {
	f := &scriptedPreview{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, err := (&App{FileRunner: f}).PreviewAgentFile(ctx, FilePreviewOptions{File: previewFile(t), Operation: "test"})
	if err == nil || f.calls != 0 || r.ReasonCode != "cancelled" || r.FullPass || r.Examples[0].Assertions[0].ReasonCode != "not_run" {
		t.Fatalf("%+v / %v", r, err)
	}
}
func TestFilePreviewNoRoutePickingForJudge(t *testing.T) {
	path := previewFile(t)
	data, _ := os.ReadFile(path)
	data = []byte(strings.ReplaceAll(string(data), "type: contains\n        value: PRIVATE OPERAND", "type: llmJudge\n        rubric: PRIVATE RUBRIC\n        minScore: 80\n        judgeModel: judge\n        judgeVersion: v1"))
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	f := &scriptedPreview{verdict: runtime.EvaluationPass}
	opt := FilePreviewOptions{File: path, Operation: "test"}
	r, err := (&App{FileRunner: f}).PreviewAgentFile(context.Background(), opt)
	if err == nil || f.calls != 0 || r.ReasonCode != "model_unavailable" {
		t.Fatalf("picked undeclared judge route: %+v / %v", r, err)
	}
	judge := modelaccess.Route{Provider: "compatible", Model: "judge", Endpoint: "http://127.0.0.1:1234", Auth: "none"}
	opt.JudgeRoute = &judge
	r, err = (&App{FileRunner: f}).PreviewAgentFile(context.Background(), opt)
	if err != nil || f.calls != 1 || f.request.JudgeRoute == nil || *f.request.JudgeRoute != judge || !r.FullPass {
		t.Fatalf("explicit judge refused: %+v / %v", r, err)
	}
}
