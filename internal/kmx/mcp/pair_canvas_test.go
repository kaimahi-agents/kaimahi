//go:build linux || darwin

package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestPairCanvasProjectionAndSparseEdits(t *testing.T) {
	for _, storage := range []string{"external", "absent", "inline"} {
		t.Run(storage, func(t *testing.T) {
			base, root := historyPaths(t)
			f := historyFiles(t, root, base)
			agent := []byte("---\nmodel: fake\n---\nPrompt\n")
			if storage == "inline" {
				agent = []byte(copilotPrompt + copilotExamples)
			}
			if _, err := f.Create(t.Context(), "a.agent.md", agent); err != nil {
				t.Fatal(err)
			}
			if storage == "external" {
				if err := os.WriteFile(filepath.Join(root, "a.agent.test.md"), []byte(historyTests), 0600); err != nil {
					t.Fatal(err)
				}
			}
			cs := historyClient(t, f)
			if storage != "external" {
				legacy, err := cs.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: agentResourceURI("a.agent.md")})
				if err != nil {
					t.Fatal(err)
				}
				var discovery map[string]any
				if json.Unmarshal([]byte(legacy.Contents[0].Text), &discovery) != nil || (discovery["pairOperations"] != nil) != (storage == "absent") {
					t.Fatalf("no v3 discovery: %s", legacy.Contents[0].Text)
				}
			}
			read, err := cs.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: sourcesURI("a.agent.md", "")})
			if err != nil {
				t.Fatal(err)
			}
			var d struct {
				SourceVersion string
				Canvas        *AgentView
			}
			if err := json.Unmarshal([]byte(read.Contents[0].Text), &d); err != nil || d.Canvas == nil {
				t.Fatalf("missing canvas projection: %s %v", read.Contents[0].Text, err)
			}
			if d.Canvas.File.Path != "a.agent.md" || d.Canvas.File.Revision != revision(agent) || d.Canvas.Instructions == "" || d.Canvas.Latest.Status != "none" {
				t.Fatalf("wrong projection: %+v", d.Canvas)
			}
			args := map[string]any{"schemaVersion": 3, "path": "a.agent.md", "expectedSourceVersion": d.SourceVersion, "exampleEdits": []map[string]any{{"id": "added", "input": "hi", "assertions": []map[string]any{{"id": "a", "type": "contains", "value": "hi"}}}}}
			if storage == "inline" {
				args["acknowledgeCopilotTests"] = true
			}
			result, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "edit", Arguments: args})
			if err != nil || result.IsError {
				t.Fatalf("edit: %+v %v", result, err)
			}
			var receipt pairSaveResult
			b, _ := json.Marshal(result.StructuredContent)
			if json.Unmarshal(b, &receipt) != nil || receipt.State != "complete" || receipt.SourceVersion == "" {
				t.Fatalf("bad receipt: %s", b)
			}
			got, _ := os.ReadFile(filepath.Join(root, "a.agent.md"))
			suite, suiteErr := os.ReadFile(filepath.Join(root, "a.agent.test.md"))
			if storage == "inline" {
				if string(got) == string(agent) || !os.IsNotExist(suiteErr) {
					t.Fatal("inline was migrated")
				}
			} else if string(got) != string(agent) || suiteErr != nil || len(suite) == 0 {
				t.Fatal("external edit changed agent or did not save suite")
			}
		})
	}
}

func TestPairCanvasProjectionRepairAndSettingsOrigin(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		t.Run(map[bool]string{false: "valid", true: "repair"}[invalid], func(t *testing.T) {
			base, root := historyPaths(t)
			f := historyFiles(t, root, base)
			for _, dir := range []string{"tests"} {
				if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
					t.Fatal(err)
				}
			}
			source := copilotPrompt
			if !invalid {
				source = strings.Replace(source, "model: fake", "model: fake\ntools: [ping]", 1)
			}
			if _, err := f.Create(t.Context(), "a.agent.md", []byte(source)); err != nil {
				t.Fatal(err)
			}
			for p, body := range map[string]string{"kmx.yaml": "tools:\n  ping: Disabled\n", "tests/kmx.yaml": "tools:\n  ping: Enabled\n", "tests/selected.agent.test.md": pairPendingTests} {
				if err := os.WriteFile(filepath.Join(root, p), []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			cs := historyClient(t, f)
			r, err := cs.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: sourcesURI("a.agent.md", "tests/selected.agent.test.md")})
			if err != nil {
				t.Fatal(err)
			}
			var d sourceDocument
			if err := json.Unmarshal([]byte(r.Contents[0].Text), &d); err != nil || d.Canvas == nil {
				t.Fatalf("projection %s %v", r.Contents[0].Text, err)
			}
			if d.Canvas.Authoring == nil || !d.Canvas.Authoring.Editable || len(d.Canvas.Examples) != 1 || d.Canvas.Latest.Status != "none" {
				t.Fatalf("repair data: %+v", d.Canvas)
			}
			if invalid {
				if d.Validation != "invalid_pair" || d.PairOperations.Execute || !d.PairOperations.Edit {
					t.Fatal("invalid pair not repair-only")
				}
			} else if len(d.Canvas.ToolSettings) != 1 || d.Canvas.ToolSettings[0].State != "Disabled" {
				t.Fatalf("settings came from suite folder: %+v", d.Canvas.ToolSettings)
			}
		})
	}
}

func TestPairCanvasOptionalProjectionNeverHidesSources(t *testing.T) {
	d := sourceDocument{SchemaVersion: 1, Kind: "kmx.agent-sources", Source: "exact agent", Tests: sourceTests{Source: "exact tests"}, Canvas: &AgentView{Instructions: strings.Repeat("x", 2<<20)}}
	r, err := sourceResourceResult(t.Context(), "kmx://agent-sources/a.agent.md", d)
	if err != nil {
		t.Fatal(err)
	}
	var got sourceDocument
	if err := json.Unmarshal([]byte(r.Contents[0].Text), &got); err != nil || got.Source != d.Source || got.Tests.Source != d.Tests.Source || got.Canvas != nil {
		t.Fatalf("partial sources: %+v %v", got, err)
	}
}

func TestPairCanvasBothSourcesSparseSave(t *testing.T) {
	base, root := historyPaths(t)
	f := historyFiles(t, root, base)
	source := copilotPrompt
	if _, err := f.Create(t.Context(), "a.agent.md", []byte(source)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.agent.test.md"), []byte(historyTests), 0600); err != nil {
		t.Fatal(err)
	}
	cs := historyClient(t, f)
	version := pairContextRead(t, cs, "a.agent.md", "")
	next := strings.Replace(source, "Review the changes.", "Review carefully.", 1)
	r, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "edit", Arguments: map[string]any{"schemaVersion": 3, "path": "a.agent.md", "expectedSourceVersion": version, "content": next, "exampleEdits": []map[string]any{{"beforeId": "greeting", "input": "Grace"}}}})
	if err != nil || r.IsError {
		t.Fatalf("both save: %+v %v", r, err)
	}
	var ack pairSaveResult
	b, _ := json.Marshal(r.StructuredContent)
	if json.Unmarshal(b, &ack) != nil || ack.State != "complete" || ack.Agent.Status != "saved" || ack.Tests.Status != "saved" {
		t.Fatalf("bad ACK: %s", b)
	}
	got, _ := os.ReadFile(filepath.Join(root, "a.agent.md"))
	suite, _ := os.ReadFile(filepath.Join(root, "a.agent.test.md"))
	if string(got) != next || !strings.Contains(string(suite), "Grace") || !strings.Contains(string(suite), "# Prose stays\r\n") {
		t.Fatal("Both save not exact")
	}
}

func TestPairCanvasSparseEditExclusivityAndConsent(t *testing.T) {
	base, root := historyPaths(t)
	f := historyFiles(t, root, base)
	agent := []byte(copilotPrompt + copilotExamples)
	if _, err := f.Create(t.Context(), "a.agent.md", agent); err != nil {
		t.Fatal(err)
	}
	cs := historyClient(t, f)
	version := pairContextRead(t, cs, "a.agent.md", "")
	patch := []map[string]any{{"id": "added", "input": "hi", "assertions": []map[string]any{{"id": "a", "type": "contains", "value": "hi"}}}}
	for name, extra := range map[string]map[string]any{"consent": {}, "raw suite": {"testsContent": historyTests}, "restore": {"restoreVersionId": "version"}, "empty": {"exampleEdits": []any{}}, "unknown field": {"exampleEdits": []any{map[string]any{"beforeId": "review", "input": "new", "unexpected": true}}}} {
		t.Run(name, func(t *testing.T) {
			args := map[string]any{"schemaVersion": 3, "path": "a.agent.md", "expectedSourceVersion": version, "exampleEdits": patch}
			for k, v := range extra {
				args[k] = v
			}
			result, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "edit", Arguments: args})
			if err != nil {
				t.Fatal(err)
			}
			reason := "invalid_options"
			if name == "consent" {
				reason = "confirmation_required"
			}
			requireHistoryToolReason(t, result, reason)
			got, _ := os.ReadFile(filepath.Join(root, "a.agent.md"))
			if string(got) != string(agent) {
				t.Fatal("refusal changed agent")
			}
			if _, err := os.Stat(filepath.Join(root, "a.agent.test.md")); !os.IsNotExist(err) {
				t.Fatal("refusal created tests")
			}
		})
	}
}
