package localpreview

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

type captureCloseExecutor struct {
	Executor
	after func() error
}

func (e captureCloseExecutor) Close() error { return errors.Join(e.Executor.Close(), e.after()) }

func TestCaptureOnlyAfterSuccessfulFinalization(t *testing.T) {
	for _, scenario := range []string{"capture", "assertion-fail", "unavailable", "uncalled", "live", "replay", "close", "cancel", "tool-error", "inexact", "credential"} {
		t.Run(scenario, func(t *testing.T) {
			req := recordingRequest(t)
			if scenario != "replay" {
				req.Examples[0].Mocks.Tools[0] = agentfile.MockTool{Name: "ping", Mode: "recorded", Calls: []agentfile.MockCall{}}
			}
			if scenario == "live" {
				req.Examples[0].Mocks.Tools[0].Mode = "live"
			}
			if scenario == "uncalled" {
				req.Agent.Instructions = "Reply with exactly `done`."
			}
			if scenario == "assertion-fail" {
				req.Examples[0].Assertions[0].Type = "toolNotCalled"
			}
			calls := 0
			dir := journalDir(t)
			r := NewRunner(RunnerOptions{JournalDir: func() (string, error) { return dir, nil }, ResolveAvailableTools: func(context.Context, []string) (*localtools.Host, error) {
				if scenario == "unavailable" {
					return nil, nil
				}
				return localtools.NewTrustedHost([]api.ToolDefinition{{Name: "ping", Description: "Live ping", InputSchema: map[string]any{"type": "object"}}}, func(_ context.Context, _ controller.ToolCallContext, c api.ToolCall) (api.ToolResult, error) {
					calls++
					if scenario == "tool-error" {
						return api.ToolResult{}, errors.New("tool failure")
					}
					if scenario == "inexact" {
						return api.ToolResult{ID: c.ID, Output: map[string]any{"value": int64(9007199254740993)}}, nil
					}
					if scenario == "credential" {
						return api.ToolResult{ID: c.ID, Output: map[string]any{"value": "ghp_" + strings.Repeat("a", 36)}}, nil
					}
					return api.ToolResult{ID: c.ID, Output: map[string]any{"value": "saved"}}, nil
				}, func(...string) error { return nil })
			}}).(*runner)
			if scenario == "close" {
				open := r.open
				r.open = func(d, m string) (Executor, string, error) {
					e, p, err := open(d, m)
					return captureCloseExecutor{e, func() error { return errors.New("close failure") }}, p, err
				}
			}
			ctx, cancel := context.WithCancel(testContext(t))
			defer cancel()
			if scenario == "cancel" {
				req.ExampleFinished = func(agentpreview.ExampleOutcome) { cancel() }
			}
			out, err := r.Run(ctx, req)
			want := scenario == "capture" || scenario == "assertion-fail"
			if (len(out.Recordings) == 1) != want {
				t.Fatalf("captures %d err %v state %s", len(out.Recordings), err, out.State)
			}
			if want {
				got := out.Recordings[0]
				if err != nil || got.ExampleID != req.Examples[0].ID || got.Tool.Name != "ping" || got.Tool.Description != "Live ping" || got.Tool.Calls[0].Result["value"] != "saved" || calls != 1 {
					t.Fatalf("capture %+v err %v calls %d", got, err, calls)
				}
				if scenario == "assertion-fail" && out.Examples[0].Assertions[0].Verdict != runtime.EvaluationFail {
					t.Fatal("capture changed assertion verdict")
				}
			}
			if (scenario == "close" || scenario == "cancel" || scenario == "tool-error" || scenario == "inexact" || scenario == "credential") && (err == nil || out.State == agentpreview.Complete) {
				t.Fatal("failed capture completed")
			}
			if (scenario == "replay" || scenario == "unavailable" || scenario == "uncalled") && calls != 0 {
				t.Fatal("unexpected live call")
			}
		})
	}
}

func TestCaptureSnapshotReuseAndIndependentJournalProof(t *testing.T) {
	req := recordingRequest(t)
	req.Examples[0].Mocks.Tools[0] = agentfile.MockTool{Name: "ping", Mode: "recorded", Calls: []agentfile.MockCall{}}
	calls := 0
	host, err := localtools.NewTrustedHost([]api.ToolDefinition{{Name: "ping", Description: "live", InputSchema: map[string]any{"type": "object"}}}, func(_ context.Context, _ controller.ToolCallContext, c api.ToolCall) (api.ToolResult, error) {
		calls++
		return api.ToolResult{ID: c.ID, Output: map[string]any{"value": "saved"}}, nil
	}, func(...string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	plan, err := makeToolPlan(req.Agent.Tools, req.Examples[0], host, func(...string) error { return nil }, func(ctx context.Context) error { return ctx.Err() }, nil)
	if err != nil {
		t.Fatal(err)
	}
	first := api.ToolCall{ID: "first", Tool: "ping", Args: map[string]any{}, IdempotencyKey: "first", Mediation: api.MediationControllerMediated}
	result, err := plan.execute(t.Context(), controller.ToolCallContext{SessionUID: "session"}, first)
	if err != nil {
		t.Fatal(err)
	}
	execution := Execution{Journaled: true, SessionUID: "session", ToolEvidenceComplete: true, ToolCalls: 1, ToolCallCounts: map[string]int{"ping": 1}, ToolExchanges: []localtools.ToolExchange{{Call: first, Result: result}}}
	for _, bad := range []string{"session", "missing", "result", "incomplete", "count"} {
		t.Run(bad, func(t *testing.T) {
			e := execution
			switch bad {
			case "session":
				e.SessionUID = "other"
			case "missing":
				e.ToolExchanges = nil
			case "result":
				e.ToolExchanges = []localtools.ToolExchange{{Call: first, Result: api.ToolResult{ID: "first", Output: map[string]any{"value": "unrelated"}}}}
			case "incomplete":
				e.ToolEvidenceComplete = false
			case "count":
				e.ToolCallCounts = map[string]int{}
			}
			if saved, err := plan.recordings(req.Examples[0].ID, e); err == nil || len(saved) != 0 {
				t.Fatalf("missing proof accepted: %+v %v", saved, err)
			}
		})
	}
	saved, err := plan.recordings(req.Examples[0].ID, execution)
	if err != nil || len(saved) != 1 {
		t.Fatalf("proof %+v %v", saved, err)
	}
	saved[0].Tool.Calls[0].Result["value"] = "mutated"
	result.Output["value"] = "also mutated"
	for _, id := range []string{"second", "third"} {
		got, err := plan.execute(t.Context(), controller.ToolCallContext{SessionUID: "session"}, api.ToolCall{ID: id, Tool: "ping", Args: map[string]any{}})
		if err != nil || got.ID != id || got.Output["value"] != "saved" || calls != 1 {
			t.Fatalf("snapshot %+v %v calls %d", got, err, calls)
		}
		got.Output["value"] = "changed"
	}
	if _, err := plan.execute(t.Context(), controller.ToolCallContext{SessionUID: "session"}, api.ToolCall{ID: "changed", Tool: "ping", Args: map[string]any{"different": true}}); !errors.Is(err, agentpreview.ErrRecordingMismatch) || calls != 1 {
		t.Fatalf("mismatch %v calls %d", err, calls)
	}
}
