package app

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func TestSessionsResponsesRefusesUnattributableOutcomes(t *testing.T) {
	for _, phase := range []string{"agent", "judge"} {
		for _, outcome := range []string{"incomplete", "tool", "refusal", "failed", "model-mismatch", "missing-text", "duplicate-model"} {
			t.Run(phase+"/"+outcome, func(t *testing.T) {
				t.Setenv("KMX_HOME", t.TempDir())
				t.Setenv("COPILOT_GITHUB_TOKEN", "outcome-source-value")
				old := http.DefaultTransport
				t.Cleanup(func() { http.DefaultTransport = old })
				sends := 0
				http.DefaultTransport = previewProviderWire(func(req *http.Request) (*http.Response, error) {
					if req.URL.Path == "/models" {
						return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"gpt-test","capabilities":{"type":"chat"},"policy":{"state":"enabled"},"supported_endpoints":["/responses"]}]}`))}, nil
					}
					if req.URL.Path != "/responses" {
						t.Error("inference changed protocol")
					}
					sends++
					var payload struct {
						Model string
						Input []directMessage
					}
					if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
						t.Error(err)
					}
					judge := len(payload.Input) > 0 && payload.Input[0].Content == runtime.EvaluationJudgePolicy
					text := "Hello, PRIVATE INPUT"
					if judge {
						text = `{"score":95,"reasoning":"good"}`
					}
					parts := []any{map[string]any{"type": "output_text", "text": text}}
					reply := map[string]any{"model": "gpt-test", "status": "completed", "output": []any{map[string]any{"type": "message", "role": "assistant", "content": parts}}}
					bad := phase == "agent" || judge
					if bad {
						switch outcome {
						case "incomplete":
							reply["status"] = "incomplete"
						case "tool":
							reply["output"] = []any{map[string]any{"type": "function_call", "call_id": "call", "name": "never_execute", "arguments": "{}"}}
						case "refusal":
							reply["output"] = []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "refusal", "refusal": "declined"}}}}
						case "failed":
							reply["status"] = "failed"
						case "model-mismatch":
							reply["model"] = "another-model"
						case "missing-text":
							reply["output"] = []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text"}}}}
						}
					}
					body, _ := json.Marshal(reply)
					if bad && outcome == "duplicate-model" {
						body = []byte(strings.TrimSuffix(string(body), "}") + `,"Model":"another-model"}`)
					}
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body)))}, nil
				})
				opt := directOptions(t, "gpt-test", true)
				route := copilotPreviewRoute()
				opt.Route = &route
				opt.ShowContent = true
				result, err := (&App{FileRunner: NewLocalFileRunner()}).PreviewAgentFile(t.Context(), opt)
				want := 1
				if phase == "judge" {
					want = 2
				}
				if err == nil || result.FullPass || result.Verdict != runtime.EvaluationUnknown || sends != want {
					t.Fatalf("invalid outcome published or retried: %+v / %v sends=%d", result, err, sends)
				}
				for _, ex := range result.Examples {
					if ex.State != "not_run" || ex.AnswerExcerpt != "" {
						t.Fatal("unfinished answer published")
					}
				}
			})
		}
	}
}
