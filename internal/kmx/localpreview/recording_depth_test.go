package localpreview

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// depthPayload has n mapping levels below its root (the root is level 0).
func depthPayload(n int, leaf string) map[string]any {
	var value any = leaf
	for i := 0; i < n; i++ {
		value = map[string]any{"k": value}
	}
	if m, ok := value.(map[string]any); ok {
		return m
	}
	return map[string]any{"k": leaf}
}

func depthRequest(t *testing.T, arguments, result map[string]any, copilot bool) agentpreview.Request {
	req := recordingRequest(t)
	call := &req.Examples[0].Mocks.Tools[0].Calls[0]
	call.Arguments, call.Result = arguments, result
	if copilot {
		req.Agent.Model = "unit"
		req.Route = modelaccess.Route{Provider: "copilot", Model: "unit", Endpoint: modelaccess.CopilotEndpoint, Auth: "copilot"}
	}
	return req
}

func depthReply(arguments map[string]any) string {
	raw, _ := json.Marshal(arguments)
	encoded, _ := json.Marshal(string(raw))
	return `{"model":"unit","choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"c","type":"function","function":{"name":"ping","arguments":` + string(encoded) + `}}]}}]}`
}

func runDepth(t *testing.T, req agentpreview.Request, access *trustedPreviewAccess) (agentpreview.Outcome, string, error) {
	t.Helper()
	dir := journalDir(t)
	opts := RunnerOptions{JournalDir: func() (string, error) { return dir, nil }}
	if access != nil {
		opts.Access = func(context.Context, []modelaccess.Route) (modelaccess.PreviewAccess, error) { return access, nil }
	}
	out, err := NewRunner(opts).Run(testContext(t), req)
	return out, dir, err
}

func TestRecordedResultRootDepth32RunsJournalsAndInspects(t *testing.T) {
	out, dir, err := runDepth(t, depthRequest(t, map[string]any{}, depthPayload(32, "deep"), false), nil)
	if err != nil || out.State != agentpreview.Complete || out.Examples[0].Assertions[0].Verdict != runtime.EvaluationPass {
		t.Fatalf("out %+v err %v", out, err)
	}
	replay, err := Inspect(testContext(t), dir, out.Journal)
	if err != nil || len(replay.Sessions) != 1 || replay.Sessions[0].Outcome != "completed" || replay.Sessions[0].ToolCalls != 1 {
		t.Fatalf("replay %+v %v", replay, err)
	}
	// One level past the root budget is refused before any journal exists.
	req := depthRequest(t, map[string]any{}, depthPayload(33, "deep"), false)
	if _, err = NewRunner(RunnerOptions{JournalDir: func() (string, error) { t.Fatal("over-budget fixture opened a journal"); return "", nil }}).Run(testContext(t), req); !errors.Is(err, agentpreview.ErrUnsafeToolContent) {
		t.Fatalf("over budget: %v", err)
	}
}

func TestRecordedArgumentsAndResultDepth32ThroughModelWire(t *testing.T) {
	args, result := depthPayload(32, "arg"), depthPayload(32, "res")
	access := &trustedPreviewAccess{reply: depthReply(args)}
	out, dir, err := runDepth(t, depthRequest(t, args, result, true), access)
	if err != nil || out.State != agentpreview.Complete || access.sends != 2 || out.Examples[0].Completion.ToolCalls != 1 {
		t.Fatalf("out %+v err %v sends %d", out, err, access.sends)
	}
	if replay, err := Inspect(testContext(t), dir, out.Journal); err != nil || replay.Sessions[0].Outcome != "completed" {
		t.Fatalf("replay %+v %v", replay, err)
	}
	// Over-budget model arguments or recorded payloads never reach the next step.
	for name, tc := range map[string]struct{ args, result map[string]any }{
		"model arguments": {depthPayload(33, "arg"), depthPayload(0, "res")},
		"recorded result": {depthPayload(0, "arg"), depthPayload(33, "res")},
	} {
		t.Run(name, func(t *testing.T) {
			access := &trustedPreviewAccess{reply: depthReply(tc.args)}
			// Keep the saved arguments valid: the model, not the fixture,
			// must be the source of the over-budget call.
			out, _, err := runDepth(t, depthRequest(t, map[string]any{}, tc.result, true), access)
			if err == nil || out.State == agentpreview.Complete || access.sends > 1 {
				t.Fatalf("over budget %+v err %v sends %d", out, err, access.sends)
			}
		})
	}
}

func TestRecordedBudgetDoesNotWidenInstructionJSON(t *testing.T) {
	req := depthRequest(t, map[string]any{}, map[string]any{}, true)
	raw, err := json.Marshal(depthPayload(29, "instruction"))
	if err != nil {
		t.Fatal(err)
	}
	req.Agent.Instructions = string(raw)
	access := &trustedPreviewAccess{}
	out, _, err := runDepth(t, req, access)
	if err == nil || out.State == agentpreview.Complete || access.sends != 0 {
		t.Fatalf("instruction JSON gained a payload exemption: state=%v err=%v sends=%d", out.State, err, access.sends)
	}
}

func TestRecordedPlanArgumentDispatchDepth32(t *testing.T) {
	req := depthRequest(t, depthPayload(32, "arg"), depthPayload(32, "res"), false)
	plan, err := makeToolPlan(req.Agent.Tools, req.Examples[0], nil, func(...string) error { return nil }, func(ctx context.Context) error { return ctx.Err() }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = plan.execute(t.Context(), controller.ToolCallContext{}, api.ToolCall{ID: "c", Tool: "ping", Args: depthPayload(32, "arg")}); err != nil {
		t.Fatalf("depth 32 dispatch: %v", err)
	}
	if _, err = plan.execute(t.Context(), controller.ToolCallContext{}, api.ToolCall{ID: "c", Tool: "ping", Args: depthPayload(33, "arg")}); err == nil {
		t.Fatal("depth 33 dispatch accepted")
	}
}

func TestDeepRecordedPayloadsStillRefuseCredentials(t *testing.T) {
	shape := "ghp_" + strings.Repeat("a", 36)
	for _, provider := range []bool{false, true} {
		for name, leaf := range map[string]string{"shape": shape, "known": "provider-canary", "embedded": `{"x":"provider-canary"}`} {
			if !provider && name != "shape" {
				continue
			}
			for _, field := range []string{"arguments", "result"} {
				t.Run(field+"/"+name, func(t *testing.T) {
					args, result := depthPayload(0, "ok"), depthPayload(0, "ok")
					if field == "arguments" {
						args = depthPayload(32, leaf)
					} else {
						result = depthPayload(32, leaf)
					}
					opts := RunnerOptions{JournalDir: func() (string, error) { t.Fatal("unsafe fixture opened journal"); return "", nil }}
					if provider {
						opts.Access = func(context.Context, []modelaccess.Route) (modelaccess.PreviewAccess, error) {
							return &trustedPreviewAccess{}, nil
						}
					}
					out, err := NewRunner(opts).Run(testContext(t), depthRequest(t, args, result, provider))
					if err == nil || out.State == agentpreview.Complete {
						t.Fatalf("unsafe %+v %v", out, err)
					}
				})
			}
		}
	}
}

func TestDefaultHostResultCheckKeepsOriginalCeiling(t *testing.T) {
	for _, tc := range []struct {
		depth int
		ok    bool
	}{{31, true}, {32, false}} {
		host, err := localtools.NewTrustedHost([]api.ToolDefinition{{Name: "ping", Description: "Ping.", InputSchema: map[string]any{"type": "object"}}}, func(_ context.Context, _ controller.ToolCallContext, c api.ToolCall) (api.ToolResult, error) {
			return api.ToolResult{ID: c.ID, Output: depthPayload(tc.depth, "x")}, nil
		}, func(...string) error { return nil })
		if err != nil {
			t.Fatal(err)
		}
		_, err = host.Execute(t.Context(), controller.ToolCallContext{SessionUID: "s"}, api.ToolCall{ID: "c", Tool: "ping", Args: map[string]any{}, Mediation: api.MediationControllerMediated, IdempotencyKey: "k"})
		if (err == nil) != tc.ok {
			t.Fatalf("live depth %d: %v", tc.depth, err)
		}
	}
}
