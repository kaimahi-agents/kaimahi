package app

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func TestFileBatchCountsFinalAndProgress(t *testing.T) {
	for _, count := range []int{5, 6} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			file := previewFile(t)
			source, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte(strings.Replace(string(source), "## Examples", "## Tools\n- ping\n\n## Examples", 1)), 0600); err != nil {
				t.Fatal(err)
			}
			runner := &progressPreview{scriptedPreview{verdict: runtime.EvaluationPass, mutate: func(out *agentpreview.Outcome) {
				for i := range out.Examples {
					out.Examples[i].Completion.ToolCalls = count
				}
			}}}
			rows := 0
			out, err := (&App{FileRunner: runner}).PreviewAgentFile(t.Context(), FilePreviewOptions{File: file, Operation: "test", CaseFinished: func(FilePreviewCaseProgress) { rows++ }})
			if count == 5 {
				if err != nil || !out.FullPass || rows != 2 || out.Examples[0].ToolCalls != 5 {
					t.Fatalf("batch rejected: %+v err=%v rows=%d", out, err, rows)
				}
			} else if err == nil || out.FullPass || rows != 0 || out.ReasonCode != "invalid_completion" {
				t.Fatalf("over-bound completion escaped: %+v %v rows=%d", out, err, rows)
			}
		})
	}
}
