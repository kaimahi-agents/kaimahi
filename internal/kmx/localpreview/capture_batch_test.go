package localpreview

import (
	"context"
	"fmt"
	"testing"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
)

type captureBatchExecutor struct {
	Executor
	mismatch  bool
	requested []string
}

func (e captureBatchExecutor) Execute(ctx context.Context, turn Turn) (Execution, error) {
	sends := 0
	turn.Call = func(_ context.Context, request api.ModelRequest) (api.ModelResponse, error) {
		sends++
		if sends == 1 {
			names := e.requested
			if len(names) == 0 {
				names = []string{"ping", "ping", "ping", "ping", "ping"}
			}
			parts := make([]api.Part, len(names))
			for i := range parts {
				args := map[string]any{}
				if e.mismatch && i == len(parts)-1 {
					args["changed"] = true
				}
				parts[i] = api.Part{ToolCall: &api.ToolCall{ID: fmt.Sprintf("call-%d", i), Tool: names[i], Args: args}}
			}
			return api.ModelResponse{Message: api.Message{Role: "assistant", Parts: parts}}, nil
		}
		if sends != 2 {
			return api.ModelResponse{}, fmt.Errorf("unexpected model send")
		}
		var previous any
		for _, message := range request.Messages {
			if message.Role != "tool" {
				continue
			}
			value := message.Parts[0].ToolResult.Output["snapshot"]
			if previous != nil && previous != value {
				return api.ModelResponse{}, fmt.Errorf("reused a different snapshot")
			}
			previous = value
		}
		return api.ModelResponse{Message: *api.TextMessage("assistant", "done")}, nil
	}
	return e.Executor.Execute(ctx, turn)
}

func TestCaptureFiveRequestsOncePerTestAndMismatchDiscardsAll(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		t.Run(fmt.Sprint(mismatch), func(t *testing.T) {
			req := recordingRequest(t)
			req.Examples[0].Mocks.Tools[0] = agentfile.MockTool{Name: "ping", Mode: "recorded", Calls: []agentfile.MockCall{}}
			req.Examples = append(req.Examples, agentfile.CloneExamples(req.Examples)[0])
			req.Examples[1].ID = "second"
			calls := 0
			dir := journalDir(t)
			r := NewRunner(RunnerOptions{JournalDir: func() (string, error) { return dir, nil }, ResolveAvailableTools: func(context.Context, []string) (*localtools.Host, error) {
				return localtools.NewTrustedHost([]api.ToolDefinition{{Name: "ping", Description: "Read ping.", InputSchema: map[string]any{"type": "object"}}}, func(_ context.Context, _ controller.ToolCallContext, c api.ToolCall) (api.ToolResult, error) {
					calls++
					return api.ToolResult{ID: c.ID, Output: map[string]any{"snapshot": calls}}, nil
				}, func(...string) error { return nil })
			}}).(*runner)
			open := r.open
			r.open = func(d, m string) (Executor, string, error) {
				e, path, err := open(d, m)
				return captureBatchExecutor{Executor: e, mismatch: mismatch}, path, err
			}
			out, err := r.Run(testContext(t), req)
			if mismatch {
				if err == nil || len(out.Recordings) != 0 || calls != 1 || out.Examples[1].Completion.State != agentpreview.NotRun {
					t.Fatalf("mismatch captures %+v err %v calls %d", out.Recordings, err, calls)
				}
				return
			}
			if err != nil || calls != 2 || len(out.Recordings) != 2 {
				t.Fatalf("capture %+v %v calls %d", out, err, calls)
			}
			for i, recording := range out.Recordings {
				if recording.ExampleID != req.Examples[i].ID || recording.Tool.Calls[0].Result["snapshot"] != float64(i+1) || out.Examples[i].Completion.Tools[0].CallCount != localtools.MaxCalls {
					t.Fatalf("cross-test capture %+v", out)
				}
			}
			replay, err := Inspect(testContext(t), dir, out.Journal)
			if err != nil || len(replay.Sessions) != 2 || replay.ModelCalls != 0 {
				t.Fatalf("inspect %+v %v", replay, err)
			}
			for _, session := range replay.Sessions {
				if !session.ToolSources.Tools[0].Capturing {
					t.Fatal("capture selection not journaled")
				}
			}
		})
	}
}
