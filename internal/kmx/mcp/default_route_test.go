package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func TestMCPDefaultRoute(t *testing.T) {
	copilot := modelaccess.Route{Provider: "copilot", Model: "claude-haiku-5.5", Endpoint: modelaccess.CopilotEndpoint, Auth: "copilot"}
	local := modelaccess.Route{Provider: "compatible", Endpoint: "https://trusted.invalid/v1", Auth: "none"}
	for _, tc := range []struct {
		name, model, reason string
		bound               bool
		explicit            *modelaccess.Route
		want                modelaccess.Route
		cause               error
	}{
		{name: "unbound-copilot", model: copilot.Model, want: copilot},
		{name: "unbound-fake", model: "fake", want: modelaccess.Route{Provider: "fake", Model: "fake", Auth: "none"}},
		{name: "explicit-provider", model: copilot.Model, explicit: &local, want: modelaccess.Route{Provider: local.Provider, Model: copilot.Model, Endpoint: local.Endpoint, Auth: local.Auth}},
		{name: "bound-unchanged", model: copilot.Model, bound: true, reason: "model_unavailable"},
		{name: "no-provider-fallback", model: copilot.Model, want: copilot, cause: agentpreview.ErrModelUnavailable, reason: "model_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _, source := canonicalFiles(t)
			path := "greeter.agent.md"
			if !tc.bound {
				f, _ = unboundFiles(t)
				path = filepath.Join(t.TempDir(), path)
			}
			source = strings.Replace(source, "**Preferred:** fake", "**Preferred:** "+tc.model, 1)
			if _, err := f.Create(t.Context(), path, []byte(source)); err != nil {
				t.Fatal(err)
			}
			runner := &previewFixture{verdict: runtime.EvaluationPass, cause: tc.cause}
			runner.during = func(r agentpreview.Request) {
				if r.Route != tc.want {
					t.Errorf("route = %+v, want %+v", r.Route, tc.want)
				}
			}
			cs := connectFixture(t, Operations(f, &app.App{FileRunner: runner}, tc.explicit))
			if runner.calls != 0 {
				t.Fatal("startup executed a model")
			}
			wantReason := tc.reason
			if wantReason == "" {
				wantReason = "checks_passed"
			}
			for i, operation := range []string{"run", "test"} {
				result, reply := previewCall(t, cs, operation, map[string]any{"schemaVersion": 1, "path": path})
				if result.ReasonCode != wantReason || reply.IsError != (tc.reason != "") {
					t.Fatalf("%s: result=%+v, isError=%v", operation, result, reply.IsError)
				}
				wantCalls := i + 1
				if tc.bound {
					wantCalls = 0
				}
				if runner.calls != wantCalls {
					t.Fatalf("%s: calls=%d, want %d", operation, runner.calls, wantCalls)
				}
			}
		})
	}
}

func TestUnboundMCPDoesNotChooseAuthoredModels(t *testing.T) {
	_, _, original := canonicalFiles(t)
	judge := strings.Split(original, "## Examples")[0] + "## Examples\n```yaml\nexamples:\n  - id: greeting\n    input: Ada\n    assertions:\n      - id: judged\n        type: llmJudge\n        rubric: Greets the user.\n        minScore: 80\n        judgeModel: claude-opus-5.5\n        judgeVersion: v1\n```\n"
	for _, tc := range []struct{ name, source, reason string }{
		{"missing", strings.Replace(original, "**Preferred:** fake", "**Preferred:**", 1), "invalid_file"},
		{"different-judge", strings.Replace(judge, "**Preferred:** fake", "**Preferred:** claude-haiku-5.5", 1), "model_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := unboundFiles(t)
			path := filepath.Join(t.TempDir(), "greeter.agent.md")
			if err := os.WriteFile(path, []byte(tc.source), 0600); err != nil {
				t.Fatal(err)
			}
			runner := &previewFixture{verdict: runtime.EvaluationPass}
			cs := connectFixture(t, Operations(f, &app.App{FileRunner: runner}, nil))
			for _, operation := range []string{"run", "test"} {
				result, reply := previewCall(t, cs, operation, map[string]any{"schemaVersion": 1, "path": path})
				if !reply.IsError || result.ReasonCode != tc.reason || runner.calls != 0 {
					t.Fatalf("%s: result=%+v calls=%d", operation, result, runner.calls)
				}
			}
		})
	}
}
