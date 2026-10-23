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
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPRuntimeModelChoice(t *testing.T) {
	for _, tc := range []struct{ name, authored, override, want, provider string }{
		{"default", "", "", "gpt-5.6-terra", "copilot"},
		{"authored", "authored-model", "", "authored-model", "copilot"},
		{"override", "authored-model", "picked-model", "picked-model", "copilot"},
		{"fake-to-real", "fake", "picked-model", "picked-model", "copilot"},
		{"real-to-fake", "authored-model", "fake", "fake", "fake"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, source := canonicalFiles(t)
			f, _ := unboundFiles(t)
			path := filepath.Join(t.TempDir(), "greeter.agent.md")
			if tc.authored == "" {
				source = strings.Replace(source, "## Model\n- **Preferred:** fake\n\n", "", 1)
			} else {
				source = strings.Replace(source, "**Preferred:** fake", "**Preferred:** "+tc.authored, 1)
			}
			saved, err := f.Create(t.Context(), path, []byte(source))
			if err != nil {
				t.Fatal(err)
			}
			runner := &previewFixture{verdict: runtime.EvaluationPass}
			runner.during = func(req agentpreview.Request) {
				if req.Route.Model != tc.want || req.Route.Provider != tc.provider || req.Agent.Model != tc.want {
					t.Errorf("wrong effective choice: %+v / %s", req.Route, req.Agent.Model)
				}
			}
			cs := connectFixture(t, Operations(f, &app.App{FileRunner: runner}, nil), func(s *sdk.Server) { AddAgentResource(s, f) })
			before := readView(t, cs, path)
			if !before.Authoring.Editable || before.Model != tc.authored {
				t.Fatal("source not editable or authored model lost")
			}
			for _, operation := range []string{"run", "test"} {
				args := map[string]any{"schemaVersion": 1, "path": path}
				if tc.override != "" {
					args["model"] = tc.override
				}
				result, reply := previewCall(t, cs, operation, args)
				if reply.IsError || !result.FullPass || result.ModelRoute.Model != tc.want || result.File.Revision != saved.Revision {
					t.Fatalf("wrong result: %+v", result)
				}
				view := readView(t, cs, path)
				if view.Latest.Status != "current" || view.Latest.Result.ModelRoute.Model != tc.want || view.Model != tc.authored {
					t.Fatal("runtime result not current independently of authored model")
				}
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != source {
				t.Fatal("runtime model rewrote source")
			}
		})
	}
}

func TestMCPModelOverrideKeepsStartupRoute(t *testing.T) {
	f, _, source := canonicalFiles(t)
	if _, err := f.Create(t.Context(), "greeter.agent.md", []byte(source)); err != nil {
		t.Fatal(err)
	}
	route := modelaccess.Route{Provider: "compatible", Endpoint: "https://trusted.invalid/v1", Auth: "none"}
	runner := &previewFixture{verdict: runtime.EvaluationPass, during: func(req agentpreview.Request) {
		if req.Route.Provider != route.Provider || req.Route.Endpoint != route.Endpoint || req.Route.Auth != route.Auth || req.Route.Model != "picked-model" {
			t.Error("override changed trusted route")
		}
	}}
	cs := connectFixture(t, Operations(f, &app.App{FileRunner: runner}, &route))
	result, reply := previewCall(t, cs, "test", map[string]any{"schemaVersion": 1, "path": "greeter.agent.md", "model": "picked-model"})
	if reply.IsError || !result.FullPass || runner.calls != 1 {
		t.Fatal("explicit route override failed")
	}
}
