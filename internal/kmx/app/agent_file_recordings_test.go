package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func TestFileRecordedSourceProjectionAndMismatch(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		t.Run(map[bool]string{false: "replay", true: "mismatch"}[mismatch], func(t *testing.T) {
			opt := directOptions(t, "fake", false)
			a, err := agentfile.Load(opt.File)
			if err != nil {
				t.Fatal(err)
			}
			a.Tools = []string{"ping"}
			a.Instructions = "Call the tool `ping`. Reply with exactly `done`."
			for i := range a.Examples {
				a.Examples[i].Assertions = []runtime.EvaluationAssertion{{ID: "called", Type: "toolCalled", Tool: "ping"}}
				args := map[string]any{}
				if mismatch && i == 1 {
					args["different"] = true
				}
				a.Examples[i].Mocks = &agentfile.Mocks{Tools: []agentfile.MockTool{{Name: "ping", Mode: "recorded", Description: "Saved ping", Parameters: map[string]any{"type": "object"}, Calls: []agentfile.MockCall{{Arguments: args, Result: map[string]any{"private": "fixture-canary"}}}}}}
			}
			base, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(base, "journals")
			runner := localpreview.NewRunner(localpreview.RunnerOptions{JournalDir: func() (string, error) { return dir, nil }})
			r, err := (&App{FileRunner: runner}).PreviewAgentSnapshot(context.Background(), a, opt)
			if mismatch {
				if err == nil || r.ReasonCode != "recording_mismatch" || !strings.Contains(r.Reason, "Recording mismatch") || r.State != "error" || r.FullPass || r.Verdict != runtime.EvaluationUnknown || r.Examples[0].State != "complete" || r.Examples[0].Verdict != runtime.EvaluationPass || len(r.Examples[0].Tools) != 1 || r.Examples[0].Tools[0].CallCount != 1 || r.Examples[1].State != "not_run" {
					t.Fatalf("missing diagnostic %+v %v", r, err)
				}
				return
			}
			if err != nil || !r.FullPass || len(r.Examples[0].Tools) != 1 {
				t.Fatalf("projection %+v %v", r, err)
			}
			row := r.Examples[0].Tools[0]
			if row.Source != "recorded" || row.Mode != "recorded" || row.CallCount != 1 || row.RecordingSaved {
				t.Fatalf("source %+v", row)
			}
			raw, _ := json.Marshal(r)
			if strings.Contains(string(raw), "fixture-canary") || strings.Contains(string(raw), "Saved ping") {
				t.Fatal("default summary leaked fixture")
			}
		})
	}
}

func TestFileToolSummaryValidation(t *testing.T) {
	ex := agentfile.Example{Mocks: &agentfile.Mocks{Tools: []agentfile.MockTool{{Name: "ping", Mode: "recorded", Calls: []agentfile.MockCall{{}}}}}}
	for _, tc := range []struct {
		name  string
		rows  []agentpreview.ToolSummary
		count int
		want  bool
	}{
		{"valid", []agentpreview.ToolSummary{{Name: "ping", Mode: "recorded", Source: "recorded", CallCount: 1}}, 1, true},
		{"missing", nil, 0, false}, {"wrong name", []agentpreview.ToolSummary{{Name: "other", Mode: "recorded", Source: "recorded"}}, 0, false},
		{"live lie", []agentpreview.ToolSummary{{Name: "ping", Mode: "recorded", Source: "live"}}, 0, false},
		{"bad count", []agentpreview.ToolSummary{{Name: "ping", Mode: "recorded", Source: "recorded", CallCount: 2}}, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := validPreviewTools(agentpreview.Completion{Tools: tc.rows, ToolCalls: tc.count}, ex, []string{"ping"}); got != tc.want {
				t.Fatalf("valid=%v", got)
			}
		})
	}
	if !validPreviewTools(agentpreview.Completion{}, agentfile.Example{}, []string{"ping"}) {
		t.Fatal("legacy missing source rejected")
	}
}

func TestFileToolReplyUnusableHasItsOwnReason(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Messages []json.RawMessage }
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		if len(body.Messages) == 2 {
			_, _ = w.Write([]byte(`{"model":"unit","choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"c","type":"function","function":{"name":"ping","arguments":"{}"}}]}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"model":"unit","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"done","audio":null}}]}`))
	}))
	defer server.Close()
	opt := directOptions(t, "unit", false)
	opt.Route = &modelaccess.Route{Provider: "compatible", Model: "unit", Endpoint: server.URL, Auth: "none"}
	a, err := agentfile.Load(opt.File)
	if err != nil {
		t.Fatal(err)
	}
	a.Tools = []string{"ping"}
	for i := range a.Examples {
		a.Examples[i].Assertions = []runtime.EvaluationAssertion{{ID: "called", Type: "toolCalled", Tool: "ping"}}
		a.Examples[i].Mocks = &agentfile.Mocks{Tools: []agentfile.MockTool{{Name: "ping", Mode: "recorded", Description: "Saved ping", Parameters: map[string]any{"type": "object"}, Calls: []agentfile.MockCall{{Arguments: map[string]any{}, Result: map[string]any{"ok": true}}}}}}
	}
	dir := filepath.Join(t.TempDir(), "journals")
	runner := localpreview.NewRunner(localpreview.RunnerOptions{JournalDir: func() (string, error) { return dir, nil }})
	r, err := (&App{FileRunner: runner}).PreviewAgentSnapshot(context.Background(), a, opt)
	if err == nil || r.ReasonCode != "model_reply_unusable" || !strings.Contains(r.Reason, "cannot use") {
		t.Fatalf("reason %+v / %v", r, err)
	}
}
