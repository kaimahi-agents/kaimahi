package localpreview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func runOnlyRequest(t *testing.T, req agentpreview.Request) agentpreview.Request {
	t.Helper()
	req.SkipAssertions = true
	return req
}

func TestRunOnlySkipsJudgeFactoryAndAccess(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprint(explicit), func(t *testing.T) {
			req := runOnlyRequest(t, runnerRequest(t))
			score := 50
			req.Examples[0].Assertions = []runtime.EvaluationAssertion{{ID: "foreign", Type: "llmJudge", Rubric: "greets", MinScore: &score, JudgeModel: "foreign", JudgeVersion: "2"}}
			if explicit {
				req.JudgeRoute = &modelaccess.Route{Provider: "copilot", Model: "unused", Endpoint: modelaccess.CopilotEndpoint, Auth: "copilot"}
			}
			before, _ := json.Marshal(req.Agent)
			dir := journalDir(t)
			out, err := NewRunner(RunnerOptions{JournalDir: func() (string, error) { return dir, nil },
				Access: func(context.Context, []modelaccess.Route) (modelaccess.PreviewAccess, error) {
					t.Error("Run accessed judge credentials")
					return nil, agentpreview.ErrModelUnavailable
				},
				Judge: func(string, string, http.RoundTripper, []runtime.EvaluationCase) (runtime.EvaluationJudge, error) {
					t.Error("Run created judge")
					return nil, agentpreview.ErrModelUnavailable
				},
			}).Run(testContext(t), req)
			if err != nil || out.State != agentpreview.Complete || out.Journal == "" {
				t.Fatalf("Run failed: %s / %v", out.State, err)
			}
			for _, row := range out.Examples {
				if len(row.Assertions) != 0 {
					t.Fatalf("Run evaluated checks: %+v", row.Assertions)
				}
			}
			after, _ := json.Marshal(req.Agent)
			if string(before) != string(after) {
				t.Fatal("authored assertions mutated")
			}
		})
	}
}

func TestRunOnlyPreservesOrderedFixtures(t *testing.T) {
	req := runOnlyRequest(t, recordingRequest(t))
	second := agentfile.CloneExamples(req.Examples)[0]
	second.ID, second.Input = "second", "Grace"
	second.Mocks.Tools[0].Calls[0].Result = map[string]any{"value": "second-fixture"}
	req.Examples = []agentfile.Example{second, req.Examples[0]}
	req.Agent.Model = "unit"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct{ Role, Content string }
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		last := body.Messages[len(body.Messages)-1]
		if last.Role == "tool" {
			fmt.Fprint(w, replyForModel(last.Content, "unit"))
			return
		}
		fmt.Fprint(w, `{"model":"unit","choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"c","type":"function","function":{"name":"ping","arguments":"{}"}}]}}]}`)
	}))
	defer server.Close()
	req.Route = modelaccess.Route{Provider: "compatible", Model: "unit", Endpoint: server.URL, Auth: "none"}
	dir := journalDir(t)
	out, err := NewRunner(RunnerOptions{JournalDir: func() (string, error) { return dir, nil }}).Run(testContext(t), req)
	if err != nil || len(out.Examples) != 2 {
		t.Fatalf("ordered execution: %+v / %v", out, err)
	}
	for i, want := range []string{`{"value":"second-fixture"}`, `{"value":"saved"}`} {
		row := out.Examples[i]
		if row.ID != req.Examples[i].ID || row.Completion.Answer != want || len(row.Assertions) != 0 || row.Completion.Tools[0].Source != "recorded" {
			t.Fatalf("fixture/order lost: %+v", row)
		}
	}
	replay, err := Inspect(testContext(t), dir, out.Journal)
	if err != nil || len(replay.Sessions) != 2 || replay.Sessions[0].Example != "second" || replay.Sessions[1].Example != req.Examples[1].ID || replay.ModelCalls != 0 {
		t.Fatalf("one ordered journal: %+v / %v", replay, err)
	}
}

func TestRunOnlyExplicitInputWithCases(t *testing.T) {
	req := runOnlyRequest(t, runnerRequest(t))
	input := "Ada"
	req.Input = &input
	e := &runnerExecutor{}
	out, err := injectedRunner(e, time.Second).Run(t.Context(), req)
	if err != nil || len(e.turns) != 3 || out.AdHoc == nil || out.AdHoc.Answer != "Hello, Ada" || len(out.Examples) != 2 {
		t.Fatalf("input/cases: %+v / %v", out, err)
	}
	for _, row := range out.Examples {
		if len(row.Assertions) != 0 {
			t.Fatal("Run evaluated checks")
		}
	}
}

func TestRunOnlyJournalCloseVeto(t *testing.T) {
	req := runOnlyRequest(t, runnerRequest(t))
	e := &runnerExecutor{closeErr: errors.New("close failed")}
	out, err := injectedRunner(e, time.Second).Run(t.Context(), req)
	if !errors.Is(err, agentpreview.ErrJournalFailed) || out.State == agentpreview.Complete || out.Journal != "" || len(out.Recordings) != 0 {
		t.Fatalf("close accepted: %+v / %v", out, err)
	}
	for _, row := range out.Examples {
		if len(row.Assertions) != 0 {
			t.Fatal("Run retained assertion rows")
		}
	}
}

func TestRunOnlyChecksOriginalAssertionContentBeforeJournal(t *testing.T) {
	for _, field := range []string{"value", "rubric", "model"} {
		t.Run(field, func(t *testing.T) {
			req := runOnlyRequest(t, runnerRequest(t))
			req.Agent.Model = "unit"
			req.Route = modelaccess.Route{Provider: "copilot", Model: "unit", Endpoint: modelaccess.CopilotEndpoint, Auth: "copilot"}
			a := &req.Examples[0].Assertions[0]
			switch field {
			case "value":
				a.Value = "provider-canary"
			case "rubric":
				a.Type = "llmJudge"
				a.Rubric = "provider-canary"
			case "model":
				a.Type = "llmJudge"
				a.JudgeModel = "provider-canary"
			}
			out, err := NewRunner(RunnerOptions{Access: func(context.Context, []modelaccess.Route) (modelaccess.PreviewAccess, error) {
				return &trustedPreviewAccess{}, nil
			}, JournalDir: func() (string, error) { t.Fatal("unsafe assertions reached journal"); return "", nil }}).Run(t.Context(), req)
			if !errors.Is(err, agentpreview.ErrCredentialContent) || len(out.Examples) != 0 {
				t.Fatalf("authored content escaped preflight: %+v / %v", out, err)
			}
		})
	}
}
