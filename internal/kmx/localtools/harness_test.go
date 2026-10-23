package localtools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"github.com/agent-primitives/sessions/eventlog"
)

func TestHarnessOptionalCallAndOfflineProof(t *testing.T) {
	for _, useTool := range []bool{false, true} {
		t.Run(map[bool]string{false: "no call", true: "one call"}[useTool], func(t *testing.T) {
			models, tools := 0, 0
			log := eventlog.New()
			config, _ := json.Marshal(map[string]string{"instructions": "draft from facts"})
			model := func(_ context.Context, r api.ModelRequest) (api.ModelResponse, error) {
				models++
				if r.Model != "unit" || len(r.Tools) != 1 || r.Tools[0].Name != Operation {
					t.Fatal("wrong model declaration")
				}
				if useTool && models == 1 {
					return api.ModelResponse{Message: api.Message{Role: "assistant", Parts: []api.Part{{ToolCall: &api.ToolCall{ID: "call_1", Tool: Operation, Args: map[string]any{}}}}}}, nil
				}
				if useTool && (len(r.Messages) != 4 || r.Messages[3].Role != "tool" || r.Messages[3].Parts[0].ToolResult.ID != "call_1") {
					t.Fatal("uncorrelated tool context")
				}
				return api.ModelResponse{Message: *api.TextMessage("assistant", "Draft")}, nil
			}
			tool := func(_ context.Context, s controller.ToolCallContext, c api.ToolCall) (api.ToolResult, error) {
				tools++
				if s.SessionUID != "session" || c.IdempotencyKey == "" || c.Mediation != api.MediationControllerMediated {
					t.Fatal("no host correlation")
				}
				return api.ToolResult{ID: c.ID, Output: map[string]any{"pullRequests": []any{}}}, nil
			}
			live, err := controller.New(eventlog.AsStore(log), model, controller.WithSessionUID("session"), controller.WithToolExecutor(tool), controller.WithStart(config, 0))
			if err != nil {
				t.Fatal(err)
			}
			if err = live.Exec(t.Context(), Harness{Model: "unit"}, []api.Message{*api.TextMessage("user", "draft")}, 0); err != nil {
				t.Fatal(err)
			}
			records := log.Read(1)
			proof, err := VerifyTrace(t.Context(), records, "unit")
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if useTool {
				want = 1
			}
			if proof.Answer != "Draft" || proof.ToolCalls != want || models != want+1 || tools != want {
				t.Fatalf("bad counts: %+v live %d/%d", proof, models, tools)
			}
			for _, mutated := range [][]eventlog.Record{records[:len(records)-1], append(append([]eventlog.Record{}, records...), records[len(records)-1])} {
				if _, err := VerifyTrace(t.Context(), mutated, "unit"); err == nil {
					t.Fatal("accepted incomplete/extra trace")
				}
			}
			if _, err := VerifyTrace(t.Context(), records, "other"); err == nil {
				t.Fatal("accepted wrong model")
			}
		})
	}
}

func TestHarnessRejectsUnknownArgumentsAndExtraRound(t *testing.T) {
	for _, tc := range []struct {
		name string
		call api.ToolCall
	}{
		{"unknown", api.ToolCall{ID: "c", Tool: "write"}},
		{"arguments", api.ToolCall{ID: "c", Tool: Operation, Args: map[string]any{"repository": "other"}}},
		{"missing ID", api.ToolCall{Tool: Operation}},
		{"extra round", api.ToolCall{ID: "c", Tool: Operation}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := eventlog.New()
			tools := 0
			model := func(context.Context, api.ModelRequest) (api.ModelResponse, error) {
				return api.ModelResponse{Message: api.Message{Role: "assistant", Parts: []api.Part{{ToolCall: &tc.call}}}}, nil
			}
			tool := func(_ context.Context, _ controller.ToolCallContext, c api.ToolCall) (api.ToolResult, error) {
				tools++
				return api.ToolResult{ID: c.ID, Output: map[string]any{}}, nil
			}
			live, err := controller.New(eventlog.AsStore(log), model, controller.WithSessionUID("s"), controller.WithToolExecutor(tool), controller.WithStart([]byte(`{"instructions":"draft"}`), 0))
			if err != nil {
				t.Fatal(err)
			}
			if err = live.Exec(t.Context(), Harness{Model: "unit"}, []api.Message{*api.TextMessage("user", "draft")}, 0); err == nil {
				t.Fatal("accepted invalid call")
			}
			if tc.name != "extra round" && tools != 0 {
				t.Fatal("unauthorized tool called")
			}
			records := log.Read(1)
			if _, err := VerifyTrace(t.Context(), records, "unit"); err == nil {
				t.Fatal("failed execution proved complete")
			}
		})
	}
}

func TestHarnessExecutorFailureDoesNotSendFinalModel(t *testing.T) {
	calls := 0
	log := eventlog.New()
	model := func(context.Context, api.ModelRequest) (api.ModelResponse, error) {
		calls++
		return api.ModelResponse{Message: api.Message{Role: "assistant", Parts: []api.Part{{ToolCall: &api.ToolCall{ID: "c", Tool: Operation}}}}}, nil
	}
	live, err := controller.New(eventlog.AsStore(log), model, controller.WithSessionUID("s"), controller.WithToolExecutor(func(context.Context, controller.ToolCallContext, api.ToolCall) (api.ToolResult, error) {
		return api.ToolResult{}, errors.New("fixed failure")
	}), controller.WithStart([]byte(`{"instructions":"draft"}`), 0))
	if err != nil {
		t.Fatal(err)
	}
	if live.Exec(t.Context(), Harness{Model: "unit"}, []api.Message{*api.TextMessage("user", "draft")}, 0) == nil || calls != 1 {
		t.Fatal("continued after failure")
	}
}
