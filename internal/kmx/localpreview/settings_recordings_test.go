package localpreview

import (
	"context"
	"testing"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func TestDisabledSettingsGateEverySelectedSource(t *testing.T) {
	for _, source := range []string{"live", "recorded", "unavailable"} {
		t.Run(source, func(t *testing.T) {
			req := recordingRequest(t)
			req.ToolSettings = []localtools.ToolSetting{{Name: "ping", State: localtools.Disabled}}
			req.Examples[0].Assertions[0].Type = "toolNotCalled"
			if source == "recorded" {
				// Disabled never enters the recording matcher, even for different saved arguments.
				req.Examples[0].Mocks.Tools[0].Calls[0].Arguments = map[string]any{"different": true}
			} else {
				req.Examples[0].Mocks = nil
			}
			dir, calls := journalDir(t), 0
			opts := RunnerOptions{JournalDir: func() (string, error) {
				// Plans must already have detached policy snapshots before journal creation.
				req.ToolSettings[0].State = localtools.Enabled
				return dir, nil
			}}
			var host *localtools.Host
			if source == "live" {
				var err error
				host, err = localtools.NewTrustedHost([]api.ToolDefinition{{Name: "ping", InputSchema: map[string]any{"type": "object"}}}, func(_ context.Context, _ controller.ToolCallContext, call api.ToolCall) (api.ToolResult, error) {
					calls++
					return api.ToolResult{ID: call.ID, Output: map[string]any{"live": true}}, nil
				}, func(...string) error { return nil })
				if err != nil {
					t.Fatal(err)
				}
			}
			opts.ResolveAvailableTools = func(context.Context, []string) (*localtools.Host, error) {
				if source == "recorded" {
					t.Fatal("recording-only case resolved a live binding")
				}
				return host, nil
			}
			plan, err := makeToolPlan(req.Agent.Tools, req.Examples[0], host, func(...string) error { return nil }, func(ctx context.Context) error { return ctx.Err() }, req.ToolSettings)
			if err != nil {
				t.Fatal(err)
			}
			answer, err := plan.execute(t.Context(), controller.ToolCallContext{SessionUID: "s"}, api.ToolCall{ID: "local", Tool: "ping", Args: map[string]any{}, Mediation: api.MediationControllerMediated, IdempotencyKey: "e:local"})
			if err != nil || answer.ID != "local" || answer.Output["status"] != "disabled" || answer.Output["message"] != "This tool is disabled for this test." || calls != 0 {
				t.Fatalf("not a local disabled answer: %+v, %v, live=%d", answer, err, calls)
			}
			out, err := NewRunner(opts).Run(testContext(t), req)
			if err != nil || out.State != agentpreview.Complete || calls != 0 || out.Examples[0].Assertions[0].Verdict != runtime.EvaluationPass {
				t.Fatalf("disabled dispatched or lost evidence: %+v, %v, live=%d", out, err, calls)
			}
			completion := out.Examples[0].Completion
			if completion.ToolCalls != 0 || len(completion.Tools) != 1 || completion.Tools[0].Source != source || completion.Tools[0].CallCount != 0 {
				t.Fatalf("wrong policy/source provenance: %+v", completion)
			}
			replay, err := Inspect(testContext(t), dir, out.Journal)
			if err != nil || len(replay.Sessions) != 1 || replay.Sessions[0].ToolCalls != 0 || replay.Sessions[0].ToolSources.Tools[0].Source != source {
				t.Fatalf("combined policy/source replay failed: %+v, %v", replay, err)
			}
		})
	}
}
