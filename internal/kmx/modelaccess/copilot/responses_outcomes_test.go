package copilot

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"
)

func TestResponsesCompletionOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name, reply, model, content, finish string
		refusal, tools                      bool
		wantErr                             error
	}{
		{name: "incomplete", reply: `{"model":"responses-only","status":"incomplete","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"partial"}]}]}`, model: "responses-only", content: "partial", finish: "length"},
		{name: "refusal", reply: `{"model":"responses-only","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"refusal","refusal":"Cannot answer."}]}]}`, model: "responses-only", finish: "stop", refusal: true},
		{name: "tool", reply: `{"model":"responses-only","status":"completed","output":[{"type":"function_call","call_id":"call_test","name":"lookup","arguments":"{}"}]}`, model: "responses-only", finish: "tool_calls", tools: true},
		{name: "actual-model", reply: `{"model":"actual-other-model","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}]}`, model: "actual-other-model", content: "answer", finish: "stop"},
		{name: "reasoning-is-not-an-answer", reply: `{"model":"responses-only","status":"completed","output":[{"type":"reasoning","summary":[{"type":"summary_text","text":"not an answer"}]}]}`, model: "responses-only", finish: "stop"},
		{name: "failed", reply: `{"model":"responses-only","status":"failed","error":{"code":"server_error","message":"private diagnostic"},"output":[]}`, wantErr: ErrUnavailable},
		{name: "duplicate-text", reply: `{"model":"responses-only","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"wrong","text":"answer"}]}]}`, wantErr: ErrMalformed},
		{name: "duplicate-model", reply: `{"model":"other","model":"responses-only","status":"completed","output":[]}`, wantErr: ErrMalformed},
		{name: "case-model", reply: `{"model":"other","MODEL":"responses-only","status":"completed","output":[]}`, wantErr: ErrMalformed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			posts := 0
			p, store := fixture(t, func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path == "/models" {
					io.WriteString(w, catalog)
					return
				}
				posts++
				io.WriteString(w, tc.reply)
			})
			store.token = canary
			route := selected()
			route.Model = "responses-only"
			client, err := p.Client(context.Background(), route)
			if err != nil {
				t.Fatal(err)
			}
			response, err := client.RoundTrip(chatRequest(t, `{"model":"responses-only","messages":[{"role":"user","content":"hello"}],"stream":false}`))
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) || response != nil {
					t.Fatalf("error: got %v, want %v", err, tc.wantErr)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				var result struct {
					Model   string `json:"model"`
					Choices []struct {
						Finish  string `json:"finish_reason"`
						Message struct {
							Content string `json:"content"`
							Refusal string `json:"refusal"`
							Tools   []struct {
								ID       string                           `json:"id"`
								Type     string                           `json:"type"`
								Function struct{ Name, Arguments string } `json:"function"`
							} `json:"tool_calls"`
						} `json:"message"`
					} `json:"choices"`
				}
				if json.NewDecoder(response.Body).Decode(&result) != nil || len(result.Choices) != 1 {
					t.Fatal("missing Chat-shaped outcome")
				}
				choice := result.Choices[0]
				if result.Model != tc.model || choice.Message.Content != tc.content || choice.Finish != tc.finish || (choice.Message.Refusal != "") != tc.refusal || (len(choice.Message.Tools) != 0) != tc.tools {
					t.Fatalf("outcome changed: %+v", result)
				}
				if tc.tools && (choice.Message.Tools[0].ID != "call_test" || choice.Message.Tools[0].Type != "function" || choice.Message.Tools[0].Function.Name != "lookup" || choice.Message.Tools[0].Function.Arguments != "{}") {
					t.Fatal("tool signal lost")
				}
			}
			if posts != 1 {
				t.Fatalf("inference replayed: %d", posts)
			}
		})
	}
}
