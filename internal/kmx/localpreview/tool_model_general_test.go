package localpreview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/agent-primitives/sessions/api"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestToolModelRejectsRoundedArguments(t *testing.T) {
	defs := []api.ToolDefinition{{Name: "lookup", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "integer"}}}}}
	raw := []byte(`{"model":"unit","choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"c","type":"function","function":{"name":"lookup","arguments":"{\"id\":9007199254740993}"}}]}}]}`)
	if _, err := parseToolReply(raw, "unit", true, defs...); err == nil {
		t.Fatal("tool identifier rounded before execution")
	}
}

func generalDefinitions() []api.ToolDefinition {
	return []api.ToolDefinition{{Name: "lookup", Description: "Trusted lookup", InputSchema: map[string]any{"type": "object", "required": []any{"query"}, "properties": map[string]any{"query": map[string]any{"type": "string"}}, "additionalProperties": false}}, {Name: "ping", InputSchema: map[string]any{"type": "object", "additionalProperties": false}}}
}

func TestToolModelGenericSchemaAndFollowup(t *testing.T) {
	for _, args := range []string{`{"query":"find"}`, `{}`, `{"query":17}`, `{"query":"find","extra":true}`, `{"query":"` + strings.Repeat("x", 8192) + `"}`} {
		t.Run(fmt.Sprint(len(args), args[:2]), func(t *testing.T) {
			sends := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sends++
				var body struct {
					Tools    []struct{ Function toolWireFunction }
					Messages []toolWireMessage
				}
				if json.NewDecoder(r.Body).Decode(&body) != nil || len(body.Tools) != 2 || body.Tools[0].Function.Name != "lookup" || body.Tools[1].Function.Name != "ping" || body.Tools[0].Function.Description != "Trusted lookup" {
					t.Error("lost definitions")
				}
				if sends == 1 {
					encoded, _ := json.Marshal(args)
					fmt.Fprintf(w, `{"model":"unit","choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"c","type":"function","function":{"name":"lookup","arguments":%s}}]}}]}`, encoded)
					return
				}
				if len(body.Messages) != 4 || body.Messages[2].ToolCalls[0].Function.Arguments != `{"query":"find"}` || body.Messages[3].ToolCallID != "c" {
					t.Error("lost arguments/correlation")
				}
				fmt.Fprint(w, `{"model":"unit","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"done"}}]}`)
			}))
			defer server.Close()
			client := newModelClient(route{model: "unit", baseURL: server.URL})
			req := api.ModelRequest{Model: "unit", Tools: generalDefinitions(), Messages: []api.Message{*api.TextMessage("system", "lookup"), *api.TextMessage("user", "input")}}
			first, err := client.ToolModel(t.Context(), req)
			if args != `{"query":"find"}` {
				if err == nil {
					t.Fatal("invalid arguments accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			req.Messages = append(req.Messages, first.Message, api.Message{Role: "tool", Parts: []api.Part{{ToolResult: &api.ToolResult{ID: "c", Output: map[string]any{"ok": true}}}}})
			final, err := client.ToolModel(t.Context(), req)
			if err != nil || final.Message.Text() != "done" || sends != 2 {
				t.Fatalf("follow-up: %q %v sends=%d", final.Message.Text(), err, sends)
			}
		})
	}
}

type toolFailureTransport struct{ err error }

func (t toolFailureTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, t.err }
func TestToolModelPreservesSafeFailuresAndDecodedCredentialRefusal(t *testing.T) {
	sentinel := errors.New("safe provider failure")
	req := api.ModelRequest{Model: "unit", Tools: generalDefinitions(), Messages: []api.Message{*api.TextMessage("system", "lookup"), *api.TextMessage("user", "input")}}
	c := newModelClient(route{model: "unit", baseURL: "http://fixture.invalid", transport: toolFailureTransport{sentinel}, safeErrors: true})
	if _, err := c.ToolModel(context.Background(), req); !errors.Is(err, sentinel) {
		t.Fatalf("safe error lost: %v", err)
	}
	for _, text := range []string{`private-canary`, `\u0070rivate-canary`} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `{"model":"unit","choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"c","type":"function","function":{"name":"lookup","arguments":"{\"query\":\"%s\"}"}}]}}]}`, text)
		}))
		c = newModelClient(route{model: "unit", baseURL: s.URL})
		c.check = func(fields ...string) error {
			for _, field := range fields {
				if strings.Contains(field, "private-canary") {
					return agentpreview.ErrCredentialContent
				}
			}
			return nil
		}
		if _, err := c.ToolModel(t.Context(), req); !errors.Is(err, agentpreview.ErrCredentialContent) {
			t.Fatalf("decoded refusal identity lost: %v", err)
		}
		s.Close()
	}
}
