package localtools

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"github.com/agent-primitives/sessions/eventlog"
)

func batchMessage(n int) api.Message {
	m := api.Message{Role: "assistant"}
	for i := 0; i < n; i++ {
		m.Parts = append(m.Parts, api.Part{ToolCall: &api.ToolCall{ID: fmt.Sprintf("call_%d", i), Tool: Operation, Args: map[string]any{}}})
	}
	return m
}

// Run the real controller so counts, ordering and replay include actual journal effects.
func execBatch(t *testing.T, m api.Message, cancelAfter int, definitions ...api.ToolDefinition) ([]eventlog.Record, int, int, error) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	log := eventlog.New()
	config, _ := json.Marshal(Config{Instructions: "draft", Definitions: definitions})
	models, tools := 0, 0
	c, err := controller.New(eventlog.AsStore(log), func(_ context.Context, r api.ModelRequest) (api.ModelResponse, error) {
		models++
		if models == 1 {
			return api.ModelResponse{Message: m}, nil
		}
		if len(r.Messages) != 3+len(m.Parts) || len(r.Messages[2].Parts) != len(m.Parts) {
			t.Fatal("lost batch transcript")
		}
		for i, p := range m.Parts {
			if r.Messages[i+3].Parts[0].ToolResult.ID != p.ToolCall.ID {
				t.Fatal("result correlation/order changed")
			}
		}
		return api.ModelResponse{Message: *api.TextMessage("assistant", "done")}, nil
	}, controller.WithSessionUID("session"), controller.WithStart(config, 0), controller.WithToolExecutor(func(_ context.Context, _ controller.ToolCallContext, c api.ToolCall) (api.ToolResult, error) {
		if c.ID != fmt.Sprintf("call_%d", tools) {
			t.Fatal("calls not dispatched in order")
		}
		tools++
		if tools == cancelAfter {
			cancel()
		}
		return api.ToolResult{ID: c.ID, Output: map[string]any{"ok": true}}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	err = c.Exec(ctx, Harness{Model: "unit", Definitions: definitions}, []api.Message{*api.TextMessage("user", "input")}, 0)
	return log.Read(1), models, tools, err
}

func TestHarnessBatchAndOfflineReplay(t *testing.T) {
	for _, n := range []int{2, 5} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			records, models, tools, err := execBatch(t, batchMessage(n), 0)
			if err != nil || models != 2 || tools != n {
				t.Fatalf("batch failed: models=%d tools=%d err=%v", models, tools, err)
			}
			trace, err := VerifyTrace(t.Context(), records, "unit")
			if err != nil || trace.ToolCalls != n || trace.Answer != "done" {
				t.Fatalf("trace: %+v / %v", trace, err)
			}
			if _, err := VerifyTrace(t.Context(), records[:len(records)-1], "unit"); err == nil {
				t.Fatal("incomplete batch proved complete")
			}
			// Structurally complete records still need exact call/result correlation.
			for _, index := range []int{5, 7} {
				raw, _ := json.Marshal(records)
				var bad []eventlog.Record
				_ = json.Unmarshal(raw, &bad)
				bad[index].Event.Result.ID = "other"
				if _, err := VerifyTrace(t.Context(), bad, "unit"); err == nil {
					t.Fatal("mismatched result replayed")
				}
			}
		})
	}
}

func TestHarnessBatchCountsDistinctNames(t *testing.T) {
	m := batchMessage(5)
	first, second := Definition(), Definition()
	first.Name, second.Name = "read", "ping"
	for i := range m.Parts {
		m.Parts[i].ToolCall.Tool = "read"
		if i%2 == 1 {
			m.Parts[i].ToolCall.Tool = "ping"
		}
	}
	records, models, tools, err := execBatch(t, m, 0, first, second)
	if err != nil || models != 2 || tools != 5 {
		t.Fatalf("batch: %v / %d/%d", err, models, tools)
	}
	trace, err := VerifyTrace(t.Context(), records, "unit")
	if err != nil || len(trace.CallsByName) != 2 || trace.CallsByName["read"] != 3 || trace.CallsByName["ping"] != 2 {
		t.Fatalf("lost per-tool counts: %+v / %v", trace, err)
	}
}

func TestHarnessBatchValidatesBeforeFirstDispatch(t *testing.T) {
	for _, name := range []string{"six", "duplicate", "invalid later"} {
		t.Run(name, func(t *testing.T) {
			m := batchMessage(5)
			switch name {
			case "six":
				m = batchMessage(6)
			case "duplicate":
				m.Parts[4].ToolCall.ID = m.Parts[0].ToolCall.ID
			case "invalid later":
				m.Parts[4].ToolCall.Args = map[string]any{"extra": true}
			}
			_, models, tools, err := execBatch(t, m, 0)
			if err == nil || models != 1 || tools != 0 {
				t.Fatalf("invalid batch dispatched: models=%d tools=%d err=%v", models, tools, err)
			}
		})
	}
}

func TestHarnessBatchCancellationStopsRemainingCalls(t *testing.T) {
	records, models, tools, err := execBatch(t, batchMessage(5), 2)
	if err == nil || models != 1 || tools != 2 {
		t.Fatalf("continued after cancellation: %d/%d %v", models, tools, err)
	}
	if _, err := VerifyTrace(t.Context(), records, "unit"); err == nil {
		t.Fatal("cancelled batch proved complete")
	}
}

func TestLegacyOneCallFollowupHash(t *testing.T) {
	records, _, _, err := execBatch(t, batchMessage(1), 0)
	if err != nil {
		t.Fatal(err)
	}
	// Captured from unmodified bde6ed25 before widening the harness.
	const original = "4aab85457249eca60934a593a2ce7280626801e6008f43135bf909419aa015d6"
	if records[6].Event.ModelCall.InputHash != original {
		t.Fatal("legacy one-call follow-up encoding changed")
	}
}
