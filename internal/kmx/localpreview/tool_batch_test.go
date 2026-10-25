package localpreview

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/agent-primitives/sessions/api"
)

func batchReply(n int) string {
	calls := make([]toolWireCall, 0, n)
	for i := 0; i < n; i++ {
		calls = append(calls, toolWireCall{ID: fmt.Sprintf("c%d", i), Type: "function", Function: toolWireFunction{Name: "ping", Arguments: "{}"}})
	}
	raw, _ := json.Marshal(map[string]any{"model": "unit", "choices": []any{map[string]any{"finish_reason": "tool_calls", "message": map[string]any{"role": "assistant", "tool_calls": calls}}}})
	return string(raw)
}

func TestToolModelBatchFollowupAndCorrelation(t *testing.T) {
	for _, n := range []int{2, 5} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			sends := 0
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sends++
				if sends == 1 {
					fmt.Fprint(w, batchReply(n))
					return
				}
				var body struct{ Messages []toolWireMessage }
				if json.NewDecoder(r.Body).Decode(&body) != nil || len(body.Messages) != 3+n || len(body.Messages[2].ToolCalls) != n {
					t.Error("lost batch history")
				} else {
					for i := 0; i < n; i++ {
						if body.Messages[3+i].ToolCallID != fmt.Sprintf("c%d", i) || body.Messages[3+i].Content != `{"ok":true}` {
							t.Error("lost result correlation")
						}
					}
				}
				fmt.Fprint(w, `{"model":"unit","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"done"}}]}`)
			}))
			defer s.Close()
			c := newModelClient(route{model: "unit", baseURL: s.URL})
			req := api.ModelRequest{Model: "unit", Tools: generalDefinitions(), Messages: []api.Message{*api.TextMessage("system", "lookup"), *api.TextMessage("user", "input")}}
			first, err := c.ToolModel(t.Context(), req)
			if err != nil || len(first.Message.Parts) != n {
				t.Fatalf("batch parse: %v", err)
			}
			req.Messages = append(req.Messages, first.Message)
			for i := 0; i < n; i++ {
				req.Messages = append(req.Messages, api.Message{Role: "tool", Parts: []api.Part{{ToolResult: &api.ToolResult{ID: fmt.Sprintf("c%d", i), Output: map[string]any{"ok": true}}}}})
			}
			// An invalid final result must refuse before a second HTTP request.
			req.Messages[len(req.Messages)-1].Parts[0].ToolResult.ID = "wrong"
			if _, err := c.ToolModel(context.Background(), req); err == nil || sends != 1 {
				t.Fatal("sent mismatched result")
			}
			req.Messages[len(req.Messages)-1].Parts[0].ToolResult.ID = fmt.Sprintf("c%d", n-1)
			out, err := c.ToolModel(t.Context(), req)
			if err != nil || out.Message.Text() != "done" || sends != 2 {
				t.Fatalf("followup: %v / %d", err, sends)
			}
		})
	}
}

func TestToolBatchReplyRejectsLimitDuplicatesAndExtraRound(t *testing.T) {
	for _, raw := range []string{batchReply(0), batchReply(6)} {
		if _, err := parseToolReply([]byte(raw), "unit", true, generalDefinitions()...); err == nil {
			t.Fatal("out of bounds batch accepted")
		}
	}
	var envelope map[string]any
	_ = json.Unmarshal([]byte(batchReply(2)), &envelope)
	calls := envelope["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["tool_calls"].([]any)
	calls[1].(map[string]any)["id"] = "c0"
	raw, _ := json.Marshal(envelope)
	if _, err := parseToolReply(raw, "unit", true, generalDefinitions()...); err == nil {
		t.Fatal("duplicate IDs accepted")
	}
	if _, err := parseToolReply([]byte(batchReply(2)), "unit", false, generalDefinitions()...); err == nil {
		t.Fatal("extra tool round accepted")
	}
}
