//go:build linux || darwin

package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestPairPreviewRefusesStaleContextBeforeRunner(t *testing.T) {
	for _, change := range []string{"unknown-context", "changed-tests", "same-byte-tests", "new-default", "new-parent", "selection"} {
		t.Run(change, func(t *testing.T) {
			base, root := historyPaths(t)
			f := historyFiles(t, root, base)
			if _, err := f.Create(t.Context(), "a.agent.md", []byte("---\nmodel: fake\n---\nPrompt\n")); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(root, "a.agent.test.md")
			if change != "new-default" && change != "new-parent" {
				if err := os.WriteFile(target, []byte(historyTests), 0600); err != nil {
					t.Fatal(err)
				}
			}
			runner := &pairPreviewFixture{previewFixture: previewFixture{verdict: runtime.EvaluationPass}}
			cs := connectFixture(t, Operations(f, &app.App{FileRunner: runner}, nil), func(s *sdk.Server) { AddAgentResource(s, f) })
			version := pairContextRead(t, cs, "a.agent.md", "")
			args := map[string]any{"schemaVersion": 3, "path": "a.agent.md", "expectedSourceVersion": version}
			switch change {
			case "unknown-context":
				args["expectedSourceVersion"] = "not-issued"
			case "changed-tests", "new-default":
				if err := os.WriteFile(target, []byte(historyTests+"\nChanged\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "same-byte-tests":
				replacement := filepath.Join(root, "replacement")
				if err := os.WriteFile(replacement, []byte(historyTests), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(replacement, target); err != nil {
					t.Fatal(err)
				}
			case "new-parent":
				if err := os.WriteFile(filepath.Join(root, "created-neighbor"), []byte("new"), 0600); err != nil {
					t.Fatal(err)
				}
			case "selection":
				other := filepath.Join(root, "other.md")
				if err := os.WriteFile(other, []byte(historyTests), 0600); err != nil {
					t.Fatal(err)
				}
				args["tests"] = other
			}
			result, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "test", Arguments: args})
			if err != nil {
				t.Fatal(err)
			}
			requireHistoryToolReason(t, result, "stale_revision")
			if runner.calls != 0 {
				t.Fatal("stale pair reached runner")
			}
		})
	}
}

func TestPairPreviewInlinePendingDoesNotRequestConsent(t *testing.T) {
	base, root := historyPaths(t)
	f := historyFiles(t, root, base)
	canonical := string(pendingRecordingSource(t))
	source := "---\nmodel: fake\ntools: [ping]\n---\nPrompt\n\n" + canonical[strings.Index(canonical, "## Examples"):]
	if _, err := f.Create(t.Context(), "a.agent.md", []byte(source)); err != nil {
		t.Fatal(err)
	}
	runner := &pairPreviewFixture{}
	cs := connectFixture(t, Operations(f, &app.App{FileRunner: runner}, nil), func(s *sdk.Server) { AddAgentResource(s, f) })
	version := pairContextRead(t, cs, "a.agent.md", "")
	result, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "test", Arguments: map[string]any{"schemaVersion": 3, "path": "a.agent.md", "expectedSourceVersion": version}})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(result.StructuredContent)
	var body struct{ Preview app.FilePreviewResult }
	if json.Unmarshal(data, &body) != nil || body.Preview.ReasonCode != "recording_save_failed" || runner.calls != 0 {
		t.Fatalf("unused consent gate: %s calls%d", data, runner.calls)
	}
}

func TestPairPreviewPendingCaptureIsPreSendUnavailable(t *testing.T) {
	base, root := historyPaths(t)
	f := historyFiles(t, root, base)
	agent := []byte(strings.Split(string(pendingRecordingSource(t)), "## Examples")[0])
	if _, err := f.Create(t.Context(), "a.agent.md", agent); err != nil {
		t.Fatal(err)
	}
	tests := []byte(pairPendingTests)
	target := filepath.Join(root, "a.agent.test.md")
	if err := os.WriteFile(target, tests, 0600); err != nil {
		t.Fatal(err)
	}
	runner := &pairPreviewFixture{previewFixture: previewFixture{verdict: runtime.EvaluationPass}}
	cs := connectFixture(t, Operations(f, &app.App{FileRunner: runner}, nil), func(s *sdk.Server) { AddAgentResource(s, f) })
	version := pairContextRead(t, cs, "a.agent.md", "")
	result, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "test", Arguments: map[string]any{"schemaVersion": 3, "path": "a.agent.md", "expectedSourceVersion": version}})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(result.StructuredContent)
	var envelope struct{ Preview app.FilePreviewResult }
	if json.Unmarshal(data, &envelope) != nil || !result.IsError || runner.calls != 0 || envelope.Preview.ReasonCode != "recording_save_failed" || envelope.Preview.SavedSnapshotRevision != "" {
		t.Fatalf("pending capture not honestly refused: %s calls%d", data, runner.calls)
	}
	for path, want := range map[string][]byte{filepath.Join(root, "a.agent.md"): agent, target: tests} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != string(want) {
			t.Fatal("unavailable capture changed source", err)
		}
	}
	resource, err := cs.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: sourcesURI("a.agent.md", "")})
	if err != nil {
		t.Fatal(err)
	}
	var d sourceDocument
	if json.Unmarshal([]byte(resource.Contents[0].Text), &d) != nil || d.PairOperations == nil || !d.PairOperations.Execute || d.PairOperations.Capture || d.PairOperations.Edit || d.PairOperations.Create || d.PairOperations.Restore || len(d.History.Versions) != 0 {
		t.Fatalf("falsely advertised writers: %s", resource.Contents[0].Text)
	}
}
