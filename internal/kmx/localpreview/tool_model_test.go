package localpreview

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agent-primitives/sessions/api"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
)

func TestToolModelWireAndReplyRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, reply string
		ok, call    bool
	}{
		{"call", `{"model":"unit","choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"github_list_merged_prs","arguments":"{}"}}]}}]}`, true, true},
		{"final", `{"model":"unit","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"Draft"}}]}`, true, false},
		{"wrong model", `{"model":"other","choices":[]}`, false, false},
		{"duplicate", `{"model":"unit","model":"unit","choices":[]}`, false, false},
		{"legacy", `{"model":"unit","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"Draft","function_call":{"name":"write"}}}]}`, false, false},
		{"alias", `{"model":"unit","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"Draft","Tool_Calls":[{}]}}]}`, false, false},
		{"arguments", `{"model":"unit","choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"github_list_merged_prs","arguments":"{\"url\":\"evil\"}"}}]}}]}`, false, false},
		{"truncated", `{"model":"unit","choices":[{"finish_reason":"length","message":{"role":"assistant","content":"Draft"}}]}`, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req map[string]any
				if json.NewDecoder(r.Body).Decode(&req) != nil || req["stream"] != false || len(req["tools"].([]any)) != 1 {
					t.Error("invalid wire")
				}
				w.Write([]byte(tc.reply))
			}))
			defer server.Close()
			client := newModelClient(route{model: "unit", baseURL: server.URL})
			req := api.ModelRequest{Model: "unit", Messages: []api.Message{*api.TextMessage("system", "draft"), *api.TextMessage("user", "now")}, Tools: []api.ToolDefinition{localtools.Definition()}}
			result, err := client.ToolModel(context.Background(), req)
			if (err == nil) != tc.ok {
				t.Fatalf("result %+v err %v", result, err)
			}
			if tc.ok && ((result.Message.Parts[0].ToolCall != nil) != tc.call) {
				t.Fatal("wrong response kind")
			}
			if tc.call {
				req.Messages = append(req.Messages, result.Message, api.Message{Role: "tool", Parts: []api.Part{{ToolResult: &api.ToolResult{ID: "c1", Output: map[string]any{"pullRequests": []any{}}}}}})
				if _, err = client.ToolModel(t.Context(), req); err == nil {
					t.Fatal("second call accepted")
				}
			}
		})
	}
}

func TestToolModelGuardsRawCredentialBeforeJournal(t *testing.T) {
	for _, text := range []string{`tool-secret-canary`, `\u0074ool-secret-canary`} {
		t.Run(text, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(`{"model":"unit","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"` + text + `"}}]}`))
			}))
			defer s.Close()
			client := newModelClient(route{model: "unit", baseURL: s.URL})
			client.check = func(values ...string) error {
				for _, v := range values {
					if strings.Contains(v, "tool-secret-canary") {
						return errRunnerExecution
					}
				}
				return nil
			}
			req := api.ModelRequest{Model: "unit", Messages: []api.Message{*api.TextMessage("system", "draft"), *api.TextMessage("user", "now")}, Tools: []api.ToolDefinition{localtools.Definition()}}
			if _, err := client.ToolModel(t.Context(), req); err == nil || strings.Contains(err.Error(), "tool-secret-canary") {
				t.Fatal("credential reply escaped")
			}
		})
	}
}
