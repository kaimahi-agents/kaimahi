package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestPreviewCancellationRecoversFinishedTestsFromResource(t *testing.T) {
	for _, operation := range []string{"run", "test"} {
		for _, phase := range []string{"agent", "judge"} {
			t.Run(operation+"/"+phase, func(t *testing.T) {
				t.Setenv("KMX_HOME", t.TempDir())
				files, _, source := canonicalFiles(t)
				source = strings.Replace(source, "**Preferred:** fake", "**Preferred:** unit", 1)
				source = strings.Split(source, "## Examples")[0] + "## Examples\n```yaml\nexamples:\n"
				firstExpected, firstVerdict := "Hello", runtime.EvaluationPass
				blockAt := int32(2)
				if phase == "judge" {
					firstExpected, firstVerdict = "missing", runtime.EvaluationFail
					// First answer, second answer, then two judge samples. Stop
					// during the second sample: one score is not a finished check.
					blockAt = 4
				}
				for _, id := range []string{"first", "second", "third"} {
					expected := "Hello"
					if id == "first" {
						expected = firstExpected
					}
					source += fmt.Sprintf("  - id: %s\n    input: Ada\n    assertions:\n      - id: text\n        type: contains\n        value: %s\n", id, expected)
					if id == "second" {
						source += "      - id: judge\n        type: llmJudge\n        rubric: Greets the user.\n        minScore: 80\n        judgeModel: unit\n        judgeVersion: v1\n"
					}
				}
				saved, err := files.Create(t.Context(), "greeter.agent.md", []byte(source+"```\n"))
				if err != nil {
					t.Fatal(err)
				}
				args := map[string]any{"schemaVersion": 1, "path": saved.Path, "showContent": true}
				if operation == "run" {
					args["input"] = "Grace"
					blockAt++
				}
				started, stopped := make(chan struct{}), make(chan struct{})
				var sends atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body struct {
						Model    string
						Messages []struct{ Role, Content string }
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
						return
					}
					n := sends.Add(1)
					if body.Model != "unit" || len(body.Messages) != 2 || r.URL.Path != "/chat/completions" {
						t.Error("unexpected model request")
					}
					if n == blockAt {
						close(started)
						<-r.Context().Done()
						close(stopped)
						return
					}
					answer := "Hello, Ada"
					var judgeInput struct{ Answer string }
					if len(body.Messages) == 2 && json.Unmarshal([]byte(body.Messages[1].Content), &judgeInput) == nil && judgeInput.Answer != "" {
						answer = `{"score":95,"reasoning":"Greets the user."}`
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"model": "unit", "choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"role": "assistant", "content": answer}}}})
				}))
				defer server.Close()
				route := modelaccess.Route{Provider: "compatible", Model: "unit", Endpoint: server.URL, Auth: "none"}
				ops := Operations(files, &app.App{FileRunner: app.NewLocalFileRunner()}, &route)
				finished := make(chan *sdk.CallToolResult, 1)
				op := ops[operation]
				handler := op.Handler
				op.Handler = func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
					result, err := handler(ctx, req)
					finished <- result
					return result, err
				}
				ops[operation] = op
				cs := connectFixture(t, ops, func(s *sdk.Server) { AddAgentResource(s, files) })
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				callDone := make(chan error, 1)
				go func() {
					_, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: operation, Arguments: args})
					callDone <- err
				}()
				select {
				case <-started:
				case <-time.After(5 * time.Second):
					t.Fatal("second test did not reach the selected model request")
				}
				cancel() // The SDK sends notifications/cancelled for this request.
				select {
				case err := <-callDone:
					if err == nil {
						t.Fatal("cancelled tool call succeeded")
					}
				case <-time.After(5 * time.Second):
					t.Fatal("client did not finish cancellation")
				}
				select {
				case <-stopped:
				case <-time.After(5 * time.Second):
					t.Fatal("cancellation did not reach the model request")
				}
				select {
				case result := <-finished:
					if result == nil || !result.IsError {
						t.Fatal("cancelled preview not reported as a tool error")
					}
				case <-time.After(5 * time.Second):
					t.Fatal("preview did not finalize after cancellation")
				}
				// The cancelled CallTool does not deliver the server result. Recover
				// it through a new resource read after the handler has finalized.
				view := readView(t, cs, saved.Path)
				result := view.Latest.Result
				if view.Latest.Status != "current" || result == nil || result.File.Revision != saved.Revision || result.State != "cancelled" || result.ReasonCode != "cancelled" || result.FullPass || result.Verdict != runtime.EvaluationUnknown || sends.Load() != blockAt {
					t.Fatalf("cancellation not retained: %+v / %+v sends=%d", view.Latest, result, sends.Load())
				}
				if len(result.Examples) != 3 || result.Examples[0].State != "complete" || result.Examples[0].Verdict != firstVerdict || result.Examples[0].AnswerExcerpt != "Hello, Ada" || strings.Contains(result.Runner, "no journal") || result.Retention.Status != "not_requested" {
					t.Fatalf("finished test or journal contract lost: %+v", result)
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
				if operation == "run" && (result.AdHoc == nil || result.AdHoc.State != "complete" || result.AdHoc.AnswerExcerpt != "Hello, Ada") {
					t.Fatalf("finished ad-hoc input lost: %+v", result.AdHoc)
				}
			})
		}
	}
}
