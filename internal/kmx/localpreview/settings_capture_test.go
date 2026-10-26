package localpreview

import (
	"context"
	"testing"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

type observedCaptureExecutor struct {
	captureBatchExecutor
	result *Execution
}

func (e observedCaptureExecutor) Execute(ctx context.Context, turn Turn) (Execution, error) {
	result, err := e.captureBatchExecutor.Execute(ctx, turn)
	*e.result = result
	return result, err
}

func TestSettingsCaptureNeverRecordsDisabledAndKeepsAllExchanges(t *testing.T) {
	for _, scenario := range []string{"enabled", "disabled", "mixed", "disabled-frozen"} {
		t.Run(scenario, func(t *testing.T) {
			req := recordingRequest(t)
			if scenario != "disabled-frozen" {
				req.Examples[0].Mocks.Tools[0] = agentfile.MockTool{Name: "ping", Mode: "recorded", Calls: []agentfile.MockCall{}}
			} else {
				req.Examples[0].Mocks.Tools[0].Calls[0].Arguments = map[string]any{"different": true}
			}
			req.ToolSettings = []localtools.ToolSetting{{Name: "ping", State: localtools.Enabled}}
			requested := []string{"ping"}
			if scenario == "disabled" || scenario == "disabled-frozen" {
				req.ToolSettings[0].State = localtools.Disabled
				req.Examples[0].Assertions[0].Type = "toolNotCalled"
			}
			if scenario == "mixed" {
				req.Agent.Tools = append(req.Agent.Tools, "off")
				req.ToolSettings = append(req.ToolSettings, localtools.ToolSetting{Name: "off", State: localtools.Disabled})
				req.Examples[0].Mocks.Tools = append(req.Examples[0].Mocks.Tools, agentfile.MockTool{Name: "off", Mode: "recorded", Calls: []agentfile.MockCall{}})
				req.Examples[0].Assertions = append(req.Examples[0].Assertions, runtime.EvaluationAssertion{ID: "off", Type: "toolNotCalled", Tool: "off"})
				requested = []string{"off", "ping"}
			}
			calls := 0
			dir := journalDir(t)
			r := NewRunner(RunnerOptions{JournalDir: func() (string, error) { return dir, nil }, ResolveAvailableTools: func(context.Context, []string) (*localtools.Host, error) {
				description := "Read ping."
				if scenario == "disabled" {
					description = ""
				}
				defs := []api.ToolDefinition{{Name: "ping", Description: description, InputSchema: map[string]any{"type": "object"}}}
				if scenario == "mixed" {
					defs = append(defs, api.ToolDefinition{Name: "off", InputSchema: map[string]any{"type": "object"}})
				}
				return localtools.NewTrustedHost(defs, func(_ context.Context, _ controller.ToolCallContext, c api.ToolCall) (api.ToolResult, error) {
					calls++
					if c.Tool == "off" || scenario == "disabled" || scenario == "disabled-frozen" {
						t.Error("disabled native execution")
					}
					return api.ToolResult{ID: c.ID, Output: map[string]any{"snapshot": "live"}}, nil
				}, func(...string) error { return nil })
			}}).(*runner)
			var execution Execution
			open := r.open
			r.open = func(d, m string) (Executor, string, error) {
				e, path, err := open(d, m)
				return observedCaptureExecutor{captureBatchExecutor{Executor: e, requested: requested}, &execution}, path, err
			}
			out, err := r.Run(testContext(t), req)
			want := 0
			if scenario == "enabled" || scenario == "mixed" {
				want = 1
			}
			if err != nil || out.State != agentpreview.Complete || len(out.Recordings) != want || calls != want || execution.ToolCalls != want || len(execution.ToolExchanges) != len(requested) {
				t.Fatalf("scenario %s state %s records %d calls %d exchanges %d error %v", scenario, out.State, len(out.Recordings), calls, len(execution.ToolExchanges), err)
			}
			if want == 1 && out.Recordings[0].Tool.Name != "ping" {
				t.Fatal("disabled answer captured")
			}
			for _, assertion := range out.Examples[0].Assertions {
				if assertion.Verdict != runtime.EvaluationPass {
					t.Fatalf("assertion %+v", assertion)
				}
			}
			inspected, err := Inspect(testContext(t), dir, out.Journal)
			if err != nil {
				t.Fatal(err)
			}
			for _, source := range inspected.Sessions[0].ToolSources.Tools {
				if localtools.IsDisabled(req.ToolSettings, source.Name) && source.Capturing {
					t.Fatal("disabled selection marked capturing")
				}
			}
			if scenario == "mixed" && execution.ToolExchanges[1].Call.Tool != "ping" {
				t.Fatal("enabled exchange dropped behind disabled request")
			}
		})
	}
}
