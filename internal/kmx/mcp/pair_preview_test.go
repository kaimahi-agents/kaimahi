//go:build linux || darwin

package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type pairPreviewFixture struct{ previewFixture }

func (f *pairPreviewFixture) Run(ctx context.Context, r agentpreview.Request) (agentpreview.Outcome, error) {
	out, err := f.previewFixture.Run(ctx, r)
	for i := range out.Examples {
		out.Examples[i].Completion.SnapshotRevision = r.SnapshotRevision
		if r.SkipAssertions {
			out.Examples[i].Assertions = nil
		}
	}
	if out.AdHoc != nil {
		out.AdHoc.SnapshotRevision = r.SnapshotRevision
	}
	return out, err
}

func TestPairPreviewUsesCheckedExternalCases(t *testing.T) {
	for _, operation := range []string{"run", "test"} {
		t.Run(operation, func(t *testing.T) {
			base, root := historyPaths(t)
			f := historyFiles(t, root, base)
			source := []byte("---\nmodel: fake\n---\nPrompt only\n")
			file, err := f.Create(t.Context(), "a.agent.md", source)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "a.agent.test.md"), []byte(historyTests), 0600); err != nil {
				t.Fatal(err)
			}
			runner := &pairPreviewFixture{previewFixture: previewFixture{verdict: runtime.EvaluationPass}}
			runner.during = func(r agentpreview.Request) {
				if strings.TrimSpace(r.Agent.Instructions) != "Prompt only" || len(r.Examples) != 1 || r.Examples[0].Input != "Ada" {
					t.Error("source roles lost")
				}
				if r.SkipAssertions != (operation == "run") {
					t.Error("wrong assertion mode")
				}
				if r.ValidateSource == nil || r.ValidateSource(t.Context()) != nil {
					t.Error("missing bound source validator")
				}
			}
			cs := connectFixture(t, Operations(f, &app.App{FileRunner: runner}, nil), func(s *sdk.Server) { AddAgentResource(s, f) })
			version := pairContextRead(t, cs, "a.agent.md", "")
			result, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: operation, Arguments: map[string]any{"schemaVersion": 3, "path": "a.agent.md", "expectedSourceVersion": version}})
			if err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(result.StructuredContent)
			var envelope struct {
				SchemaVersion       int
				Kind, SourceVersion string
				Preview             app.FilePreviewResult
			}
			if json.Unmarshal(data, &envelope) != nil || result.IsError || runner.calls != 1 || envelope.SchemaVersion != 3 || envelope.Kind != "kmx.agent-pair-preview" || envelope.SourceVersion != version || envelope.Preview.State != "complete" || envelope.Preview.File.Revision != file.Revision || envelope.Preview.Tests == nil || envelope.Preview.Tests.State != "external" || envelope.Preview.SnapshotRevision == file.Revision {
				t.Fatalf("bad paired result: %s %v", data, err)
			}
			if operation == "run" && (envelope.Preview.FullPass || envelope.Preview.Verdict != runtime.EvaluationUnknown) {
				t.Fatalf("Run fabricated pass: %s", data)
			}
			if operation == "test" && !envelope.Preview.FullPass {
				t.Fatalf("Test lost checks: %s", data)
			}
		})
	}
}
