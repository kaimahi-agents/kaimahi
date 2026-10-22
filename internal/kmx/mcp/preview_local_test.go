package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func TestPreviewToolsUseJournaledLocalRunner(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KMX_HOME", home)
	files, _, source := canonicalFiles(t)
	saved, err := files.Create(t.Context(), "greeter.agent.md", []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	cs := connectFixture(t, Operations(files, &app.App{FileRunner: app.NewLocalFileRunner()}, nil))
	for _, op := range []string{"test", "run"} {
		args := map[string]any{"schemaVersion": 1, "path": saved.Path, "caseId": "greeting"}
		if op == "run" {
			args["input"] = "Grace"
			args["showContent"] = true
		}
		result, call := previewCall(t, cs, op, args)
		if call.IsError || result.State != "complete" || result.Verdict != runtime.EvaluationPass || strings.Contains(result.Runner, "no journal") {
			t.Fatalf("adapter not used by MCP: %+v / %+v", result, call)
		}
		if op == "run" && (result.AdHoc == nil || result.AdHoc.AnswerExcerpt != "Hello, Grace") {
			t.Fatalf("ad-hoc answer missing: %+v", result)
		}
		encoded, _ := json.Marshal(result)
		if strings.Contains(string(encoded), home) || strings.Contains(string(encoded), ".sqlite") {
			t.Fatalf("journal handle exposed: %s", encoded)
		}
	}
}
