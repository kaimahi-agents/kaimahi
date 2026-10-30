package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
)

func TestFilePairCaptureWritesTestsAndReplays(t *testing.T) {
	t.Setenv("KMX_HOME", privateCommandHome(t))
	file, tests := cliPairFiles(t, true)
	source := []byte("---\nname: recorder\nmodel: fake\ntools: [ping]\n---\nCall the tool `ping`. Reply with exactly `done`.\n")
	if err := os.WriteFile(file, source, 0600); err != nil {
		t.Fatal(err)
	}
	testsSource := strings.Replace(cliPairTests, "input: Ada", "input: Ada\nmocks: {tools: [{name: ping, mode: recorded, calls: []}]}", 1)
	testsSource = strings.Replace(testsSource, "type: contains, value: Hello from pair", "type: toolCalled, tool: ping", 1)
	if err := os.WriteFile(tests, []byte(testsSource), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := agentfile.LoadPair(file, "")
	if err != nil {
		t.Fatal(err)
	}
	var out, diag bytes.Buffer
	deps, _ := testDependencies(&out, &diag)
	calls := 0
	deps.fileRunner = localpreview.NewRunner(localpreview.RunnerOptions{JournalDir: func() (string, error) { return privateCommandHome(t), nil }, ResolveAvailableTools: func(context.Context, []string) (*localtools.Host, error) {
		return localtools.NewTrustedHost([]api.ToolDefinition{{Name: "ping", Description: "schema", InputSchema: map[string]any{"type": "object"}}}, func(_ context.Context, _ controller.ToolCallContext, c api.ToolCall) (api.ToolResult, error) {
			calls++
			return api.ToolResult{ID: c.ID, Output: map[string]any{"value": "captured"}}, nil
		}, func(...string) error { return nil })
	}})
	for replay := range 2 {
		out.Reset()
		diag.Reset()
		if err := execute([]string{"test", file, "-o", "json"}, deps); err != nil {
			t.Fatalf("capture/replay %v: %s %s", err, out.String(), diag.String())
		}
		var result app.FilePreviewResult
		if err := json.Unmarshal(out.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		after, err := agentfile.LoadPair(file, "")
		if err != nil {
			t.Fatal(err)
		}
		agentBytes, err := os.ReadFile(file)
		if err != nil || !bytes.Equal(agentBytes, source) {
			t.Fatalf("agent rewritten: %v", err)
		}
		if calls != 1 || !result.FullPass || result.File.Revision != before.Agent.Revision {
			t.Fatalf("wrong execution identity/calls: %+v calls=%d", result, calls)
		}
		if replay == 0 {
			if result.SnapshotRevision != before.SnapshotRevision || result.SavedRevision != after.Tests.Revision || result.SavedSnapshotRevision != after.SnapshotRevision || result.RecordingTarget == nil || result.RecordingTarget.Role != "tests" || result.RecordingTarget.SelectionKey != before.Tests.Selection.Key || !result.Examples[0].Tools[0].RecordingSaved {
				t.Fatalf("wrong target acknowledgement: %+v", result)
			}
		} else if result.SavedRevision != "" || result.SnapshotRevision != after.SnapshotRevision || result.Examples[0].Tools[0].Source != "recorded" {
			t.Fatalf("wrong replay: %+v", result)
		}
	}
}
