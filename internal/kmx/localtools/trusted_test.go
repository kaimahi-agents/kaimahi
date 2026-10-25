package localtools

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"github.com/agent-primitives/sessions/eventlog"
	"strings"
	"testing"
)

func trustedDefinitions() []api.ToolDefinition {
	return []api.ToolDefinition{{Name: "lookup", Description: "Trusted lookup", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string", "minLength": 1}}, "required": []any{"query"}, "additionalProperties": false}}, {Name: "ping", Description: "Trusted ping", InputSchema: map[string]any{"type": "object", "additionalProperties": false}}}
}

func TestTrustedHostCopiesValidatesAndBounds(t *testing.T) {
	defs := trustedDefinitions()
	calls := 0
	host, err := NewTrustedHost(defs, func(_ context.Context, _ controller.ToolCallContext, c api.ToolCall) (api.ToolResult, error) {
		calls++
		return api.ToolResult{ID: c.ID, Output: map[string]any{"answer": "ok"}}, nil
	}, func(values ...string) error {
		for _, v := range values {
			if strings.Contains(v, "private-canary") {
				return errors.New("unsafe")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defs[0].Name = "changed"
	defs[0].InputSchema["type"] = "string"
	host.Definitions()[0].InputSchema["type"] = "boolean"
	if !host.Matches([]string{"ping", "lookup"}) || host.Matches([]string{"lookup"}) || host.Matches([]string{"lookup", "lookup"}) {
		t.Fatal("tool set escaped declared names")
	}
	for _, tc := range []struct {
		name  string
		args  map[string]any
		valid bool
	}{{"lookup", map[string]any{"query": "find"}, true}, {"lookup", map[string]any{}, false}, {"lookup", map[string]any{"query": 17}, false}, {"lookup", map[string]any{"query": "find", "extra": true}, false}, {"lookup", map[string]any{"query": strings.Repeat("x", 8192)}, false}, {"lookup", map[string]any{"query": "private-canary"}, false}, {"absent", map[string]any{}, false}} {
		before := calls
		_, err = host.Execute(t.Context(), controller.ToolCallContext{SessionUID: "session"}, api.ToolCall{ID: "call", Tool: tc.name, Args: tc.args, Mediation: api.MediationControllerMediated, IdempotencyKey: "exec:call"})
		if (err == nil) != tc.valid || calls-before != boolCount(tc.valid) {
			t.Fatalf("%s %v valid=%v calls=%d err=%v", tc.name, tc.args, tc.valid, calls-before, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	before := calls
	if _, err = host.Execute(ctx, controller.ToolCallContext{SessionUID: "s"}, api.ToolCall{ID: "c", Tool: "ping", Mediation: api.MediationControllerMediated, IdempotencyKey: "e:c"}); err == nil || calls != before {
		t.Fatal("canceled tool executed")
	}
	for _, value := range []string{strings.Repeat("x", 32*1024), "private-canary"} {
		h, err := NewTrustedHost(trustedDefinitions(), func(_ context.Context, _ controller.ToolCallContext, c api.ToolCall) (api.ToolResult, error) {
			return api.ToolResult{ID: c.ID, Output: map[string]any{"answer": value}}, nil
		}, host.Check)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = h.Execute(t.Context(), controller.ToolCallContext{SessionUID: "s"}, api.ToolCall{ID: "c", Tool: "ping", Mediation: api.MediationControllerMediated, IdempotencyKey: "e:c"}); err == nil {
			t.Fatal("unsafe/oversized result accepted")
		}
	}
}
func boolCount(v bool) int {
	if v {
		return 1
	}
	return 0
}

func TestTrustedHarnessJournalReplaysExactDefinitions(t *testing.T) {
	defs := trustedDefinitions()
	config, _ := json.Marshal(struct {
		Instructions string               `json:"instructions"`
		Definitions  []api.ToolDefinition `json:"definitions,omitempty"`
	}{"draft", defs})
	calls, tools := 0, 0
	log := eventlog.New()
	model := func(_ context.Context, r api.ModelRequest) (api.ModelResponse, error) {
		calls++
		if len(r.Tools) != 2 || r.Tools[0].Name != "lookup" {
			t.Fatal("lost offered definitions")
		}
		if calls == 1 {
			return api.ModelResponse{Message: api.Message{Role: "assistant", Parts: []api.Part{{ToolCall: &api.ToolCall{ID: "c", Tool: "lookup", Args: map[string]any{"query": "find"}}}}}}, nil
		}
		if len(r.Messages) != 4 {
			t.Fatal("lost correlated follow-up")
		}
		return api.ModelResponse{Message: *api.TextMessage("assistant", "done")}, nil
	}
	live, err := controller.New(eventlog.AsStore(log), model, controller.WithSessionUID("s"), controller.WithStart(config, 0), controller.WithToolExecutor(func(_ context.Context, _ controller.ToolCallContext, c api.ToolCall) (api.ToolResult, error) {
		tools++
		return api.ToolResult{ID: c.ID, Output: map[string]any{"ok": true}}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err = live.Exec(t.Context(), Harness{Model: "unit", Definitions: defs}, []api.Message{*api.TextMessage("user", "input")}, 0); err != nil {
		t.Fatal(err)
	}
	trace, err := VerifyTrace(t.Context(), log.Read(1), "unit")
	if err != nil || trace.Answer != "done" || trace.ToolCalls != 1 || calls != 2 || tools != 1 {
		t.Fatalf("trace %+v err %v calls=%d tools=%d", trace, err, calls, tools)
	}
}

func TestTrustedDefinitionsRejectInvalidAuthority(t *testing.T) {
	for _, mutate := range []func([]api.ToolDefinition) []api.ToolDefinition{
		func(d []api.ToolDefinition) []api.ToolDefinition { return nil },
		func(d []api.ToolDefinition) []api.ToolDefinition { return append(d, d[0]) },
		func(d []api.ToolDefinition) []api.ToolDefinition { d[0].Name = "bad name"; return d },
		func(d []api.ToolDefinition) []api.ToolDefinition { d[0].Name = "server.read"; return d },
		func(d []api.ToolDefinition) []api.ToolDefinition { d[0].Description = "private-canary"; return d },
		func(d []api.ToolDefinition) []api.ToolDefinition {
			d[0].InputSchema = map[string]any{"$ref": "https://untrusted.invalid/schema"}
			return d
		},
	} {
		_, err := NewTrustedHost(mutate(trustedDefinitions()), func(context.Context, controller.ToolCallContext, api.ToolCall) (api.ToolResult, error) {
			t.Fatal("executor called")
			return api.ToolResult{}, nil
		}, func(values ...string) error {
			for _, v := range values {
				if strings.Contains(v, "private-canary") {
					return errors.New("unsafe")
				}
			}
			return nil
		})
		if err == nil {
			t.Fatal("invalid definitions accepted")
		}
	}
}
