package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
)

func TestFileCommandsCaptureSelectedFileAndReplayWithoutLiveHost(t *testing.T) {
	for _, operation := range []string{"test", "run"} {
		t.Run(operation, func(t *testing.T) {
			source := "# Recorder\n## Identity\n- **ID:** recorder\n- **Purpose:** Record.\n## Accountable Responsibilities\nCall the tool `ping`. Reply with exactly `done`.\n## Non-Responsibilities\nNo effects.\n## Collaboration and Review Authority\nNone.\n## Model\n- **Preferred:** fake\n## Tools\n- ping\n## Examples\n```yaml\nexamples:\n  - id: sample\n    input: hello\n    mocks:\n      tools:\n        - name: ping # unchanged\n          mode: recorded\n          calls: [] # waiting\n    assertions:\n      - id: calls\n        type: toolCalled\n        tool: ping\n```\n"
			base, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(base, "selected charter.md") // CLI has never required .agent.md.
			if err := os.WriteFile(file, []byte(source), 0644); err != nil {
				t.Fatal(err)
			}
			original, err := agentfile.Load(file)
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("KMX_HOME", privateCommandHome(t))
			var output, diagnostic bytes.Buffer
			deps, loads := testDependencies(&output, &diagnostic)
			summary := filepath.Join(base, "step-summary.md")
			if operation == "test" {
				t.Setenv("GITHUB_STEP_SUMMARY", summary)
			}
			calls, resolves := 0, 0
			deps.fileRunner = localpreview.NewRunner(localpreview.RunnerOptions{JournalDir: func() (string, error) { return filepath.Join(base, "journals"), nil }, ResolveAvailableTools: func(context.Context, []string) (*localtools.Host, error) {
				resolves++
				return localtools.NewTrustedHost([]api.ToolDefinition{{Name: "ping", Description: "private-cli-schema", InputSchema: map[string]any{"type": "object"}}}, func(_ context.Context, _ controller.ToolCallContext, c api.ToolCall) (api.ToolResult, error) {
					calls++
					return api.ToolResult{ID: c.ID, Output: map[string]any{"value": "private-cli-result"}}, nil
				}, func(...string) error { return nil })
			}})
			for run := range 2 {
				output.Reset()
				diagnostic.Reset()
				args := []string{operation, file, "-o", "json"}
				if operation == "test" {
					if err := os.Remove(summary); err != nil && !os.IsNotExist(err) {
						t.Fatal(err)
					}
					args = append(args, "--github-summary")
				}
				if err := execute(args, deps); err != nil {
					t.Fatalf("CLI %v: %s", err, diagnostic.String())
				}
				var result app.FilePreviewResult
				if err := json.Unmarshal(output.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if !result.FullPass || calls != 1 || resolves != 1 || *loads != 0 {
					t.Fatalf("CLI result %+v calls %d resolve %d", result, calls, resolves)
				}
				if run == 0 {
					saved, err := agentfile.Load(file)
					if err != nil {
						t.Fatal(err)
					}
					if result.File.Revision != original.Revision || result.SavedRevision != saved.Revision || !result.Examples[0].Tools[0].RecordingSaved || saved.Examples[0].Mocks.Tools[0].Calls[0].Result["value"] != "private-cli-result" {
						t.Fatalf("save %+v", result)
					}
				} else if result.SavedRevision != "" || result.Examples[0].Tools[0].Source != "recorded" || result.Examples[0].Tools[0].RecordingSaved {
					t.Fatalf("replay %+v", result)
				}
				if strings.Contains(output.String(), "private-cli-result") || strings.Contains(output.String(), "private-cli-schema") {
					t.Fatal("default CLI leaked recording")
				}
				if operation == "test" {
					markdown, err := os.ReadFile(summary)
					wantSource := "`live`"
					if run == 1 {
						wantSource = "`recorded`"
					}
					if err != nil || !bytes.Contains(markdown, []byte("## kmx test: PASS")) || !bytes.Contains(markdown, []byte("`ping`")) || !bytes.Contains(markdown, []byte(wantSource)) || bytes.Contains(markdown, []byte("private-cli-result")) || bytes.Contains(markdown, []byte("private-cli-schema")) || bytes.Contains(markdown, []byte(file)) {
						t.Fatalf("capture summary run=%d: %q, %v", run, markdown, err)
					}
				}
			}
			saved, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(saved, []byte("# unchanged")) || !bytes.Contains(saved, []byte("# waiting")) {
				t.Fatal("capture deleted source comments")
			}
		})
	}
}
