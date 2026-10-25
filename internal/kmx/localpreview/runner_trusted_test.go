package localpreview

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
)

type trustedPreviewAccess struct {
	sends         int
	expired       bool
	reply         string
	onFollowup    func()
	validateCalls int
}

func (a *trustedPreviewAccess) CheckContent(fields ...string) error {
	for _, f := range fields {
		if strings.Contains(f, "provider-canary") {
			return agentpreview.ErrCredentialContent
		}
	}
	return nil
}
func (a *trustedPreviewAccess) Validate(ctx context.Context) error {
	a.validateCalls++
	if a.expired {
		return agentpreview.ErrModelUnavailable
	}
	return ctx.Err()
}
func (a *trustedPreviewAccess) Client(modelaccess.Route) (http.RoundTripper, error) { return a, nil }
func (a *trustedPreviewAccess) RoundTrip(r *http.Request) (*http.Response, error) {
	a.sends++
	raw, _ := io.ReadAll(r.Body)
	if strings.Contains(string(raw), `"role":"tool"`) {
		if a.onFollowup != nil {
			a.onFollowup()
		}
		return fixtureToolResponse(`{"model":"unit","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"done"}}]}`), nil
	}
	if a.reply != "" {
		return fixtureToolResponse(a.reply), nil
	}
	return fixtureToolResponse(`{"model":"unit","choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"c","type":"function","function":{"name":"ping","arguments":"{}"}}]}}]}`), nil
}
func fixtureToolResponse(raw string) *http.Response {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(raw))}
}

func TestTrustedRunnerFakeAndCopilotReplay(t *testing.T) {
	for _, provider := range []string{"fake", "copilot"} {
		t.Run(provider, func(t *testing.T) {
			req := runnerRequest(t)
			req.Examples = req.Examples[:1]
			req.Agent.Tools = []string{"lookup", "ping"}
			req.Agent.Instructions = "Call the tool `ping`. Reply with exactly `done`."
			dir := journalDir(t)
			access := &trustedPreviewAccess{}
			if provider == "copilot" {
				req.Agent.Model = "unit"
				req.Route = modelaccess.Route{Provider: "copilot", Model: "unit", Endpoint: modelaccess.CopilotEndpoint, Auth: "copilot"}
			}
			executions := 0
			options := RunnerOptions{JournalDir: func() (string, error) { return dir, nil }, Access: func(context.Context, []modelaccess.Route) (modelaccess.PreviewAccess, error) { return access, nil }, ResolveTools: func(_ context.Context, names []string) (*localtools.Host, error) {
				if fmt.Sprint(names) != "[lookup ping]" {
					t.Fatalf("names=%v", names)
				}
				names[0] = "mutated"
				return localtools.NewTrustedHost(generalDefinitions(), func(_ context.Context, _ controller.ToolCallContext, c api.ToolCall) (api.ToolResult, error) {
					executions++
					if c.Tool != "ping" {
						t.Fatal("wrong executor")
					}
					return api.ToolResult{ID: c.ID, Output: map[string]any{"ok": true}}, nil
				}, func(...string) error { return nil })
			}}
			out, err := NewRunner(options).Run(testContext(t), req)
			if err != nil || out.State != agentpreview.Complete || executions != 1 || out.Examples[0].Completion.ToolCalls != 1 || out.Examples[0].Completion.Answer != "done" || req.Agent.Tools[0] != "lookup" {
				t.Fatalf("out=%+v err=%v executions=%d", out, err, executions)
			}
			if provider == "copilot" && access.sends != 2 {
				t.Fatalf("sends=%d", access.sends)
			}
			replay, err := Inspect(testContext(t), dir, out.Journal)
			if err != nil || len(replay.Sessions) != 1 || replay.Sessions[0].Outcome != "completed" || replay.Sessions[0].ToolCalls != 1 || replay.ModelCalls != 0 || executions != 1 {
				t.Fatalf("replay=%+v err=%v", replay, err)
			}
		})
	}
}

func TestTrustedRunnerRejectsUnboundBeforeProviderAccess(t *testing.T) {
	req := runnerRequest(t)
	req.Agent.Tools = []string{"ping"}
	req.Agent.Model = "unit"
	req.Route = modelaccess.Route{Provider: "copilot", Model: "unit", Endpoint: modelaccess.CopilotEndpoint, Auth: "copilot"}
	for _, bound := range []bool{false, true} {
		options := RunnerOptions{Access: func(context.Context, []modelaccess.Route) (modelaccess.PreviewAccess, error) {
			t.Fatal("unbound accessed provider")
			return nil, nil
		}, JournalDir: func() (string, error) { t.Fatal("unbound journal"); return "", nil }}
		if bound {
			options.ResolveTools = func(context.Context, []string) (*localtools.Host, error) {
				return localtools.NewTrustedHost(generalDefinitions(), func(context.Context, controller.ToolCallContext, api.ToolCall) (api.ToolResult, error) {
					t.Fatal("unlisted executor")
					return api.ToolResult{}, nil
				}, func(...string) error { return nil })
			}
		}
		if _, err := NewRunner(options).Run(t.Context(), req); !errors.Is(err, agentpreview.ErrToolsUnavailable) {
			t.Fatalf("unbound error=%v", err)
		}
	}
}

func TestTrustedRunnerChecksResultAndExpiryBeforeFollowup(t *testing.T) {
	for _, failure := range []string{"credential", "expired"} {
		t.Run(failure, func(t *testing.T) {
			req := runnerRequest(t)
			req.Examples = req.Examples[:1]
			req.Agent.Tools = []string{"lookup", "ping"}
			req.Agent.Model = "unit"
			req.Route = modelaccess.Route{Provider: "copilot", Model: "unit", Endpoint: modelaccess.CopilotEndpoint, Auth: "copilot"}
			access := &trustedPreviewAccess{}
			dir := journalDir(t)
			executions := 0
			options := RunnerOptions{Access: func(context.Context, []modelaccess.Route) (modelaccess.PreviewAccess, error) { return access, nil }, JournalDir: func() (string, error) { return dir, nil }, ResolveTools: func(context.Context, []string) (*localtools.Host, error) {
				return localtools.NewTrustedHost(generalDefinitions(), func(_ context.Context, _ controller.ToolCallContext, c api.ToolCall) (api.ToolResult, error) {
					executions++
					value := "provider-canary"
					if failure == "expired" {
						access.expired = true
						value = "safe"
					}
					return api.ToolResult{ID: c.ID, Output: map[string]any{"answer": value}}, nil
				}, func(...string) error { return nil })
			}}
			out, err := NewRunner(options).Run(testContext(t), req)
			if err == nil || out.State == agentpreview.Complete || access.sends != 1 || executions != 1 {
				t.Fatalf("out=%+v err=%v sends=%d executions=%d", out, err, access.sends, executions)
			}
			runs, _ := ListRuns(dir)
			if len(runs) != 1 {
				t.Fatal("lost interrupted journal")
			}
			replay, err := Inspect(testContext(t), dir, runs[0])
			if err != nil || len(replay.Sessions) != 1 || replay.Sessions[0].Outcome == "completed" || strings.Contains(fmt.Sprint(replay), "provider-canary") {
				t.Fatalf("unsafe/complete replay %+v %v", replay, err)
			}
		})
	}
}
