package localtools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"github.com/agent-primitives/sessions/eventlog"
)

func TestSettingsJournalReplayCountsOnlyLiveCalls(t *testing.T) {
	for _, state := range []ToolState{Enabled, Disabled} {
		t.Run(string(state), func(t *testing.T) {
			executions, models := 0, 0
			binding, _ := Parse([]byte(bindingJSON))
			host, err := NewHost(binding, "test-credential", func(context.Context) ([]PullRequest, error) { executions++; return nil, nil })
			if err != nil {
				t.Fatal(err)
			}
			settings := []ToolSetting{{Name: Operation, State: state}}
			execute := func(ctx context.Context, scope controller.ToolCallContext, call api.ToolCall) (api.ToolResult, error) {
				if IsDisabled(settings, call.Tool) {
					return DisabledResult(call.ID), nil
				}
				return host.Execute(ctx, scope, call)
			}
			config, _ := json.Marshal(Config{Instructions: "draft", ToolSettings: settings})
			log := eventlog.New()
			c, err := controller.New(eventlog.AsStore(log), func(_ context.Context, r api.ModelRequest) (api.ModelResponse, error) {
				models++
				if models == 1 {
					return api.ModelResponse{Message: api.Message{Role: "assistant", Parts: []api.Part{{ToolCall: &api.ToolCall{ID: "c", Tool: Operation, Args: map[string]any{}}}}}}, nil
				}
				if state == Disabled && r.Messages[3].Parts[0].ToolResult.Output["message"] != "This tool is disabled for this test." {
					t.Fatal("model did not receive disabled answer")
				}
				return api.ModelResponse{Message: *api.TextMessage("assistant", "done")}, nil
			}, controller.WithSessionUID("s"), controller.WithStart(config, 0), controller.WithToolExecutor(execute))
			if err != nil {
				t.Fatal(err)
			}
			if err := c.Exec(t.Context(), Harness{Model: "unit", ToolSettings: settings}, []api.Message{*api.TextMessage("user", "input")}, 0); err != nil {
				t.Fatal(err)
			}
			proof, err := VerifyTrace(t.Context(), log.Read(1), "unit")
			want := 0
			if state == Enabled {
				want = 1
			}
			if err != nil || proof.ToolCalls != want || proof.CallsByName[Operation] != want || executions != want || models != 2 || proof.Answer != "done" {
				t.Fatalf("false live proof: %+v %v executions/models=%d/%d", proof, err, executions, models)
			}
		})
	}
}

func TestHarnessRejectsSettingsConfigMismatchBeforeModel(t *testing.T) {
	config, _ := json.Marshal(Config{Instructions: "draft", ToolSettings: []ToolSetting{{Name: Operation, State: Enabled}}})
	log := eventlog.New()
	c, err := controller.New(eventlog.AsStore(log), func(context.Context, api.ModelRequest) (api.ModelResponse, error) {
		t.Fatal("mismatched policy reached model")
		return api.ModelResponse{}, nil
	}, controller.WithStart(config, 0))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Exec(t.Context(), Harness{Model: "unit", ToolSettings: []ToolSetting{{Name: Operation, State: Disabled}}}, []api.Message{*api.TextMessage("user", "input")}, 0); err == nil {
		t.Fatal("policy mismatch accepted")
	}
}
