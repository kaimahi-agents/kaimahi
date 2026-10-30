//go:build linux || darwin

package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestPairAuthoringCreateThenTestsOnlyEdit(t *testing.T) {
	base, root := historyPaths(t)
	f := historyFiles(t, root, base)
	cs := historyClient(t, f)
	agent := []byte("---\nmodel: fake\n---\nPrompt\n")
	result, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "create", Arguments: map[string]any{"schemaVersion": 3, "path": "a.agent.md", "content": string(agent), "testsContent": historyTests}})
	if err != nil {
		t.Fatal(err)
	}
	var first struct {
		SchemaVersion                          int
		State, SnapshotRevision, SourceVersion string
		Agent, Tests                           struct{ Status string }
	}
	data, _ := json.Marshal(result.StructuredContent)
	if json.Unmarshal(data, &first) != nil || result.IsError || first.State != "complete" || first.Agent.Status != "saved" || first.Tests.Status != "saved" || first.SnapshotRevision == "" {
		t.Fatalf("pair create: %s %v", data, err)
	}
	version := pairContextRead(t, cs, "a.agent.md", "")
	result, err = cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "edit", Arguments: map[string]any{"schemaVersion": 3, "path": "a.agent.md", "expectedSourceVersion": version, "testsContent": historyTests + "\nOnly tests changed\n"}})
	if err != nil {
		t.Fatal(err)
	}
	var second struct {
		State, SnapshotRevision string
		Agent, Tests            struct{ Status string }
		History                 *FileHistoryStatus
	}
	data, _ = json.Marshal(result.StructuredContent)
	if json.Unmarshal(data, &second) != nil || result.IsError || second.State != "complete" || second.Agent.Status != "unchanged" || second.Tests.Status != "saved" || second.SnapshotRevision == first.SnapshotRevision || second.History == nil || second.History.Status != "retained" {
		t.Fatalf("tests-only save: %s %v", data, err)
	}
	current, err := os.ReadFile(filepath.Join(root, "a.agent.md"))
	if err != nil || string(current) != string(agent) {
		t.Fatal("tests save changed prompt", err)
	}
	r, err := cs.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: sourcesURI("a.agent.md", "")})
	if err != nil {
		t.Fatal(err)
	}
	var document sourceDocument
	if json.Unmarshal([]byte(r.Contents[0].Text), &document) != nil || document.History == nil || len(document.History.Versions) != 1 || document.History.Versions[0].SourceKind != "pair" || document.History.Versions[0].SnapshotRevision != first.SnapshotRevision {
		t.Fatalf("preimage history lost: %s", r.Contents[0].Text)
	}
}

func TestPairAuthoringCrossInvalidViewNeverExecutes(t *testing.T) {
	for _, kind := range []string{"tool-reference", "inline-conflict"} {
		t.Run(kind, func(t *testing.T) {
			base, root := historyPaths(t)
			f := historyFiles(t, root, base)
			source := pendingRecordingSource(t)
			suite := []byte(pairPendingTests)
			if kind == "tool-reference" {
				source = []byte(strings.Split(string(source), "## Examples")[0])
				suite = []byte(strings.ReplaceAll(pairPendingTests, "ping", "other"))
			}
			if _, err := f.Create(t.Context(), "a.agent.md", source); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "a.agent.test.md"), suite, 0600); err != nil {
				t.Fatal(err)
			}
			runner := &pairPreviewFixture{}
			cs := connectFixture(t, Operations(f, &app.App{FileRunner: runner}, nil), func(s *sdk.Server) { AddAgentResource(s, f) })
			r, err := cs.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: sourcesURI("a.agent.md", "")})
			if err != nil {
				t.Fatal(err)
			}
			var d struct {
				Kind, Source, SourceVersion, Validation string
				Tests                                   struct{ Source string }
				PairOperations                          struct{ Execute, Edit bool }
			}
			if json.Unmarshal([]byte(r.Contents[0].Text), &d) != nil || d.Kind != "kmx.agent-sources" || d.SourceVersion == "" || d.Validation != "invalid_pair" || d.PairOperations.Execute || string(source) != d.Source || string(suite) != d.Tests.Source {
				t.Fatalf("cannot safely repair typed sources: %s", r.Contents[0].Text)
			}
			result, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "test", Arguments: map[string]any{"schemaVersion": 3, "path": "a.agent.md", "expectedSourceVersion": d.SourceVersion}})
			if err != nil {
				t.Fatal(err)
			}
			requireHistoryToolReason(t, result, "invalid_file")
			if runner.calls != 0 {
				t.Fatal("invalid pair reached execution")
			}
		})
	}
}
