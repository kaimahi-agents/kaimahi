package localpreview

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsessions"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
)

func TestToolJournalExecutionAndOfflineInspection(t *testing.T) {
	dir := journalDir(t)
	calls, tools := 0, 0
	execute := func(_ context.Context, _ controller.ToolCallContext, c api.ToolCall) (api.ToolResult, error) {
		tools++
		return api.ToolResult{ID: c.ID, Output: map[string]any{"pullRequests": []any{}}}, nil
	}
	e, run, err := openToolJournalExecutor(dir, "unit", execute)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	model := func(context.Context, api.ModelRequest) (api.ModelResponse, error) {
		calls++
		if calls == 1 {
			return api.ModelResponse{Message: api.Message{Role: "assistant", Parts: []api.Part{{ToolCall: &api.ToolCall{ID: "c", Tool: localtools.Operation}}}}}, nil
		}
		return api.ModelResponse{Message: *api.TextMessage("assistant", "Draft")}, nil
	}
	result, err := e.Execute(ctx, Turn{AgentID: "writer", Revision: "sha256:" + strings.Repeat("a", 64), ExampleID: "one", Instructions: "Draft release notes", Input: "Draft", Model: "unit", Call: model})
	if err != nil {
		t.Fatal(err)
	}
	if result.Answer != "Draft" || result.ToolCalls != 1 || calls != 2 || tools != 1 {
		t.Fatalf("result %+v counts %d/%d", result, calls, tools)
	}
	proof, err := e.host.client.VerifyCase(ctx, agentsessions.VerifyRequest{SessionUID: result.SessionUID, Harness: localtools.HarnessName, Model: "unit", Head: agentsessions.JournalHead{Seq: 9, Hash: strings.Repeat("a", 64)}, AnswerSHA256: strings.Repeat("a", 64)})
	if err != nil || proof.Status != "unsupported" {
		t.Fatalf("tool trace qualified suite proof: %+v %v", proof, err)
	}
	if err = e.Close(); err != nil {
		t.Fatal(err)
	}
	replay, err := Inspect(ctx, dir, run)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay.Sessions) != 1 || replay.Sessions[0].Outcome != "completed" || replay.Sessions[0].Answer != "Draft" || replay.Sessions[0].ToolCalls != 1 || replay.Sessions[0].ModelCalls != 0 || calls != 2 || tools != 1 {
		t.Fatalf("offline replay %+v", replay)
	}
}
