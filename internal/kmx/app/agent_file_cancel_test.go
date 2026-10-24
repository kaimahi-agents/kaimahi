package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func TestCopilotPreviewCancellationRetainsOnlyFinishedTests(t *testing.T) {
	for _, operation := range []string{"run", "test"} {
		for _, phase := range []string{"agent", "judge", "second-judge"} {
			t.Run(operation+"/"+phase, func(t *testing.T) {
				t.Setenv("KMX_HOME", t.TempDir())
				opt := directOptions(t, "gpt-test", false)
				source, err := os.ReadFile(opt.File)
				if err != nil {
					t.Fatal(err)
				}
				text := strings.Split(string(source), "## Examples")[0] + "## Examples\n```yaml\nexamples:\n"
				firstExpected, firstVerdict := "Hello", runtime.EvaluationPass
				if phase != "agent" {
					firstExpected, firstVerdict = "missing", runtime.EvaluationFail
				}
				for _, id := range []string{"first", "second", "third"} {
					expected := "Hello"
					if id == "first" {
						expected = firstExpected
					}
					text += fmt.Sprintf("  - id: %s\n    input: PRIVATE INPUT\n    assertions:\n      - id: text\n        type: contains\n        value: %s\n", id, expected)
					if id == "second" {
						text += "      - id: judge\n        type: llmJudge\n        rubric: Greets the user.\n        minScore: 80\n        judgeModel: gpt-test\n        judgeVersion: v1\n"
					}
				}
				agent, err := agentfile.Parse(opt.File, []byte(text+"```\n"))
				if err != nil {
					t.Fatal(err)
				}
				route := copilotPreviewRoute()
				opt.Route, opt.Operation, opt.ShowContent = &route, operation, true
				blockAt := 2
				if phase != "agent" {
					blockAt++
				}
				if phase == "second-judge" {
					blockAt++
				}
				if operation == "run" {
					input := "PRIVATE AD HOC"
					opt.Input = &input
					blockAt++
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				started := make(chan struct{})
				provider := &journalCopilotAccess{}
				provider.beforeSend = func(req *http.Request) error {
					if provider.sends == blockAt {
						close(started)
						<-req.Context().Done()
						return req.Context().Err()
					}
					return nil
				}
				done := make(chan struct{})
				var result FilePreviewResult
				var cause error
				go func() {
					defer close(done)
					result, cause = (&App{FileRunner: newLocalFileRunner(provider.PreviewClient)}).PreviewAgentSnapshot(ctx, agent, opt)
				}()
				select {
				case <-started:
				case <-time.After(5 * time.Second):
					t.Fatal("second test did not reach the selected send")
				}
				cancel()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("cancel did not stop the preview")
				}
				var previewErr *FilePreviewError
				if !errors.As(cause, &previewErr) || previewErr.ExitCode() != 130 || result.State != "cancelled" || result.ReasonCode != "cancelled" || result.FullPass || result.Verdict != runtime.EvaluationUnknown || provider.sends != blockAt {
					t.Fatalf("cancellation lost: %+v / %v sends=%d", result, cause, provider.sends)
				}
				if len(result.Examples) != 3 || result.File.Revision != agent.Revision || result.Examples[0].State != "complete" || result.Examples[0].Verdict != firstVerdict || result.Examples[0].AnswerExcerpt != "Hello, PRIVATE INPUT" {
					t.Fatalf("finished test lost: %+v", result)
				}
				for _, row := range result.Examples[1:] {
					if row.State != "not_run" || row.AnswerExcerpt != "" || row.Verdict != runtime.EvaluationUnknown {
						t.Fatalf("unfinished test published: %+v", row)
					}
					for _, check := range row.Assertions {
						if check.ReasonCode != "not_run" || check.Verdict != runtime.EvaluationUnknown {
							t.Fatalf("unfinished check published: %+v", check)
						}
					}
				}
				if operation == "run" && (result.AdHoc == nil || result.AdHoc.State != "complete" || result.AdHoc.AnswerExcerpt != "Hello, PRIVATE INPUT") {
					t.Fatalf("finished ad-hoc input lost: %+v", result.AdHoc)
				}
			})
		}
	}
}
