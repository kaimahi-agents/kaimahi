package localpreview

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
)

func TestPerTurnToolsStayIsolatedAndReplayOffline(t *testing.T) {
	dir := journalDir(t)
	e, run, err := openJournalExecutor(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	liveCalls := 0
	for i, want := range []string{"first", "second", "different-schema"} {
		schema := map[string]any{"type": "object"}
		if i == 2 {
			schema["additionalProperties"] = false
		}
		defs := []api.ToolDefinition{{Name: "lookup", Description: "saved", InputSchema: schema}}
		sources := &localtools.SourceConfig{Version: 1, Tools: []localtools.ToolSource{{Name: "lookup", Mode: "recorded", Source: "recorded"}}}
		calls := 0
		turn := Turn{AgentID: "writer", Revision: "sha256:" + strings.Repeat("a", 64), ExampleID: want, Instructions: "Read", Input: "input", Model: "unit", ToolDefinitions: defs, ToolSources: sources}
		turn.Tool = func(_ context.Context, _ controller.ToolCallContext, c api.ToolCall) (api.ToolResult, error) {
			liveCalls++
			return api.ToolResult{ID: c.ID, Output: map[string]any{"value": want}}, nil
		}
		turn.Call = func(_ context.Context, r api.ModelRequest) (api.ModelResponse, error) {
			calls++
			if len(r.Tools) != 1 || r.Tools[0].Name != "lookup" {
				t.Fatalf("definitions %+v", r.Tools)
			}
			if calls == 1 {
				return api.ModelResponse{Message: api.Message{Role: "assistant", Parts: []api.Part{{ToolCall: &api.ToolCall{ID: "call", Tool: "lookup", Args: map[string]any{}}}}}}, nil
			}
			got := r.Messages[len(r.Messages)-1].Parts[0].ToolResult.Output["value"]
			if got != want {
				t.Fatalf("cross-test fixture: %v, want %s", got, want)
			}
			return api.ModelResponse{Message: *api.TextMessage("assistant", want)}, nil
		}
		got, err := e.Execute(ctx, turn)
		if err != nil || got.Answer != want || !got.ToolEvidenceComplete || got.ToolCallCounts["lookup"] != 1 {
			t.Fatalf("execution %+v: %v", got, err)
		}
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	replay, err := Inspect(ctx, dir, run)
	if err != nil || len(replay.Sessions) != 3 || liveCalls != 3 || replay.ModelCalls != 0 {
		t.Fatalf("inspection %+v: %v", replay, err)
	}
	for _, s := range replay.Sessions {
		if s.Outcome != "completed" || s.ToolSources == nil || s.ToolSources.Tools[0].Source != "recorded" {
			t.Fatalf("source lost %+v", s)
		}
	}
}
