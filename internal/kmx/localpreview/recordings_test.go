package localpreview

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func recordingRequest(t *testing.T) agentpreview.Request {
	req := runnerRequest(t)
	req.Examples = req.Examples[:1]
	req.Agent.Tools = []string{"ping"}
	req.Agent.Instructions = "Call the tool `ping`. Reply with exactly `done`."
	req.Examples[0].Assertions = []runtime.EvaluationAssertion{{ID: "called", Type: "toolCalled", Tool: "ping"}}
	req.Examples[0].Mocks = &agentfile.Mocks{Tools: []agentfile.MockTool{{Name: "ping", Mode: "recorded", Description: "Saved ping", Parameters: map[string]any{"type": "object"}, Calls: []agentfile.MockCall{{Arguments: map[string]any{}, Result: map[string]any{"value": "saved"}}}}}}
	return req
}

func TestRecordedRunnerNoResolverAndCompleteCallEvidence(t *testing.T) {
	for _, called := range []bool{true, false} {
		t.Run(map[bool]string{true: "called", false: "not called"}[called], func(t *testing.T) {
			req := recordingRequest(t)
			if !called {
				req.Agent.Instructions = "Reply with exactly `done`."
				req.Examples[0].Assertions[0].Type = "toolNotCalled"
			}
			dir := journalDir(t)
			out, err := NewRunner(RunnerOptions{JournalDir: func() (string, error) { return dir, nil }, ResolveTools: func(context.Context, []string) (*localtools.Host, error) {
				t.Fatal("frozen recording resolved a live tool")
				return nil, nil
			}}).Run(testContext(t), req)
			if err != nil || out.State != agentpreview.Complete || out.Examples[0].Assertions[0].Verdict != runtime.EvaluationPass {
				t.Fatalf("out %+v err %v", out, err)
			}
			row := out.Examples[0].Completion.Tools[0]
			want := 0
			if called {
				want = 1
			}
			if row.Source != "recorded" || row.CallCount != want || row.Mode != "recorded" {
				t.Fatalf("source %+v", row)
			}
			replay, err := Inspect(testContext(t), dir, out.Journal)
			if err != nil || replay.Sessions[0].ToolSources.Tools[0].Source != "recorded" {
				t.Fatalf("replay %+v %v", replay, err)
			}
		})
	}
}

func TestRecordingMismatchStopsWholeRunWithoutFallback(t *testing.T) {
	req := recordingRequest(t)
	req.Examples[0].Mocks.Tools[0].Calls[0].Arguments = map[string]any{"different": true}
	req.Examples = append(req.Examples, agentfile.CloneExamples(req.Examples)[0])
	req.Examples[1].ID = "later"
	dir := journalDir(t)
	out, err := NewRunner(RunnerOptions{JournalDir: func() (string, error) { return dir, nil }}).Run(testContext(t), req)
	if !errors.Is(err, agentpreview.ErrRecordingMismatch) || out.Examples[1].Completion.State != agentpreview.NotRun {
		t.Fatalf("out %+v err %v", out, err)
	}
	runs, _ := ListRuns(dir)
	replay, err := Inspect(testContext(t), dir, runs[0])
	if err != nil || len(replay.Sessions) != 1 || replay.Sessions[0].Outcome == "completed" {
		t.Fatalf("replay %+v %v", replay, err)
	}
}

func TestPendingAndUnavailableRunnerSources(t *testing.T) {
	for _, available := range []bool{false, true} {
		t.Run(map[bool]string{true: "live", false: "unavailable"}[available], func(t *testing.T) {
			req := recordingRequest(t)
			req.Examples[0].Mocks.Tools[0] = agentfile.MockTool{Name: "ping", Mode: "recorded", Calls: []agentfile.MockCall{}}
			req.Agent.Tools = append(req.Agent.Tools, "absent")
			count := 0
			dir := journalDir(t)
			opts := RunnerOptions{JournalDir: func() (string, error) { return dir, nil }}
			opts.ResolveAvailableTools = func(_ context.Context, names []string) (*localtools.Host, error) {
				if !available {
					return nil, nil
				}
				return localtools.NewTrustedHost([]api.ToolDefinition{{Name: "ping", InputSchema: map[string]any{"type": "object"}}}, func(_ context.Context, _ controller.ToolCallContext, c api.ToolCall) (api.ToolResult, error) {
					count++
					return api.ToolResult{ID: c.ID, Output: map[string]any{"ok": true}}, nil
				}, func(...string) error { return nil })
			}
			out, err := NewRunner(opts).Run(testContext(t), req)
			if err != nil || out.Examples[0].Assertions[0].Verdict != runtime.EvaluationPass {
				t.Fatalf("out %+v %v", out, err)
			}
			source := "unavailable"
			want := 0
			if available {
				source = "live"
				want = 1
			}
			if count != want || out.Examples[0].Completion.Tools[0].Source != source || len(req.Examples[0].Mocks.Tools[0].Calls) != 0 {
				t.Fatalf("out %+v calls %d", out, count)
			}
		})
	}
}

func TestReplayReturnsExactlySavedOutput(t *testing.T) {
	req := recordingRequest(t)
	saved := req.Examples[0].Mocks.Tools[0].Calls[0]
	plan, err := makeToolPlan(req.Agent.Tools, req.Examples[0], nil, func(...string) error { return nil }, func(ctx context.Context) error { return ctx.Err() })
	if err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(saved.Result)
	for _, id := range []string{"first", "fresh"} {
		got, err := plan.execute(t.Context(), controller.ToolCallContext{}, api.ToolCall{ID: id, Tool: "ping", Args: map[string]any{}})
		raw, _ := json.Marshal(got.Output)
		if err != nil || string(raw) != string(want) || got.ID != id {
			t.Fatalf("changed saved output: %+v %v", got, err)
		}
		got.Output["changed"] = true
	}
}

func TestIncompleteToolEvidenceNeverPassesNegative(t *testing.T) {
	req := recordingRequest(t)
	req.Examples[0].Assertions[0].Type = "toolNotCalled"
	e := &runnerExecutor{}
	out, err := injectedRunner(e, 0).Run(testContext(t), req)
	if err == nil || out.Examples[0].Assertions[0].Verdict != runtime.EvaluationUnknown {
		t.Fatalf("incomplete evidence passed: %+v %v", out, err)
	}
}

func TestToolPlanRefusalBeforeState(t *testing.T) {
	req := recordingRequest(t)
	req.Examples[0].Mocks.Tools[0].Parameters = map[string]any{"type": "invalid"}
	_, err := NewRunner(RunnerOptions{JournalDir: func() (string, error) { t.Fatal("invalid plan resolved state"); return "", nil }}).Run(testContext(t), req)
	if !errors.Is(err, agentpreview.ErrToolsUnavailable) {
		t.Fatalf("wrong plan refusal: %v", err)
	}
}

func TestFrozenCredentialsRefusedBeforeJournal(t *testing.T) {
	for _, provider := range []bool{false, true} {
		t.Run(map[bool]string{false: "known-shape", true: "provider-key"}[provider], func(t *testing.T) {
			req := recordingRequest(t)
			opts := RunnerOptions{JournalDir: func() (string, error) { t.Fatal("unsafe fixture opened journal"); return "", nil }}
			key := "ghp_" + strings.Repeat("a", 36)
			if provider {
				key = "provider-canary"
				req.Agent.Model = "unit"
				req.Route = modelaccess.Route{Provider: "copilot", Model: "unit", Endpoint: modelaccess.CopilotEndpoint, Auth: "copilot"}
				opts.Access = func(context.Context, []modelaccess.Route) (modelaccess.PreviewAccess, error) {
					return &trustedPreviewAccess{}, nil
				}
			}
			req.Examples[0].Mocks.Tools[0].Calls[0].Result = map[string]any{key: "hidden"}
			out, err := NewRunner(opts).Run(testContext(t), req)
			if err == nil || out.State == agentpreview.Complete {
				t.Fatalf("unsafe %+v %v", out, err)
			}
		})
	}
}
