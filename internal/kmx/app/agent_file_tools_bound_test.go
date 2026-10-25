package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess/copilot"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

type boundToolAccess struct {
	sends, judges int
	failure       error
}

func (*boundToolAccess) CheckContent(...string) error                          { return nil }
func (*boundToolAccess) Validate(ctx context.Context) error                    { return ctx.Err() }
func (a *boundToolAccess) Client(modelaccess.Route) (http.RoundTripper, error) { return a, nil }
func (a *boundToolAccess) RoundTrip(r *http.Request) (*http.Response, error) {
	a.sends++
	if a.failure != nil {
		return nil, a.failure
	}
	var body struct {
		Model    string
		Tools    []any
		Messages []struct {
			Role    string
			Content any
		}
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return nil, err
	}
	w := httptest.NewRecorder()
	if body.Messages[0].Content == runtime.EvaluationJudgePolicy {
		a.judges++
		if len(body.Tools) != 0 {
			return nil, fmt.Errorf("judge offered tools")
		}
		previewEnvelope(w, body.Model, `{"score":95,"reasoning":"good"}`)
	} else if len(body.Messages) == 2 {
		fmt.Fprintf(w, `{"model":%q,"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"c","type":"function","function":{"name":"ping","arguments":"{}"}}]}}]}`, body.Model)
	} else {
		previewEnvelope(w, body.Model, "Hello, PRIVATE INPUT")
	}
	return w.Result(), nil
}

func TestFileToolsCopilotTrustedBindingAndPlainRefusal(t *testing.T) {
	for _, failure := range []error{nil, copilot.ErrToolsUnsupported, copilot.ErrAuthRevoked} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			opt := directOptions(t, "gpt-test", true)
			route := copilotPreviewRoute()
			opt.Route = &route
			a, err := agentfile.Load(opt.File)
			if err != nil {
				t.Fatal(err)
			}
			a.Tools = []string{"ping"}
			access := &boundToolAccess{failure: failure}
			executions := 0
			dir := filepath.Join(t.TempDir(), "journals")
			runner := localpreview.NewRunner(localpreview.RunnerOptions{JournalDir: func() (string, error) { return dir, nil }, Judge: localFilePreviewJudge, Access: func(context.Context, []modelaccess.Route) (modelaccess.PreviewAccess, error) { return access, nil }, ResolveTools: func(context.Context, []string) (*localtools.Host, error) {
				return localtools.NewTrustedHost([]api.ToolDefinition{{Name: "ping", InputSchema: map[string]any{"type": "object", "additionalProperties": false}}}, func(_ context.Context, _ controller.ToolCallContext, c api.ToolCall) (api.ToolResult, error) {
					executions++
					return api.ToolResult{ID: c.ID, Output: map[string]any{"ok": true}}, nil
				}, func(...string) error { return nil })
			}})
			result, err := (&App{FileRunner: runner}).PreviewAgentSnapshot(t.Context(), a, opt)
			if failure != nil {
				want := "tools_unavailable"
				if failure == copilot.ErrAuthRevoked {
					want = "copilot_auth_revoked"
				}
				if err == nil || result.ReasonCode != want || executions != 0 || access.sends != 1 {
					t.Fatalf("result %+v err=%v sends=%d calls=%d", result, err, access.sends, executions)
				}
				return
			}
			if err != nil || !result.FullPass || access.sends != 8 || access.judges != 4 || executions != 2 {
				t.Fatalf("result %+v err=%v sends=%d judges=%d calls=%d", result, err, access.sends, access.judges, executions)
			}
		})
	}
}
