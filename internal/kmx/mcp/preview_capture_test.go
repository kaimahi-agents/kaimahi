package mcp

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func TestMCPPreviewCaptureThenOfflineFixtureAndStaleSave(t *testing.T) {
	for _, scenario := range []string{"saved", "copilot", "assertion-fail", "stale", "empty-description", "empty-description-space", "empty-description-uncalled", "empty-description-live"} {
		t.Run(scenario, func(t *testing.T) {
			files, _, source := canonicalFiles(t)
			source = source[:strings.Index(source, "## Examples")] + "## Tools\n- ping\n\n## Examples\n```yaml\nexamples:\n  - id: capture\n    input: original\n    mocks:\n      tools:\n        - name: ping # keep this comment\n          mode: recorded\n          calls: [] # waiting\n    assertions:\n      - id: called\n        type: toolCalled\n        tool: ping\n```\n"
			source = strings.Replace(source, "Greet the input.", "Call the tool `ping`. Reply with exactly `done`.", 1)
			// The repository greeter expresses its task in this section.
			begin := strings.Index(source, "## Accountable Responsibilities")
			end := begin + strings.Index(source[begin+3:], "\n## ") + 3
			source = source[:begin] + "## Accountable Responsibilities\nCall the tool `ping`. Reply with exactly `done`.\n" + source[end:]
			if scenario == "empty-description-uncalled" {
				source = strings.Replace(source, "Call the tool `ping`. Reply with exactly `done`.", "Reply with exactly `done`.", 1)
				source = strings.Replace(source, "type: toolCalled", "type: toolNotCalled", 1)
			}
			if scenario == "empty-description-live" {
				source = strings.Replace(source, "mode: recorded\n          calls: []", "mode: live", 1)
			}
			if scenario == "assertion-fail" {
				source = strings.Replace(source, "type: toolCalled", "type: toolNotCalled", 1)
			}
			const header = "---\ndescription: Record a call\nmodel: fake\ntools: [ping]\nfuture: {keep: 'as written'} # comment\n---\n"
			if scenario == "copilot" {
				source = header + "Call the tool `ping`. Reply with exactly `done`.\n\n" + source[strings.Index(source, "## Examples"):]
			}
			file, err := files.Create(t.Context(), "capture.agent.md", []byte(source))
			if err != nil {
				t.Fatal(err)
			}
			home := previewStateHome(t)
			t.Setenv("KMX_HOME", home)
			calls, resolves := 0, 0
			runner := localpreview.NewRunner(localpreview.RunnerOptions{JournalDir: func() (string, error) { return filepath.Join(filepath.Dir(home), "journals"), nil }, ResolveAvailableTools: func(context.Context, []string) (*localtools.Host, error) {
				resolves++
				description := "private-definition-canary"
				if strings.HasPrefix(scenario, "empty-description") {
					description = ""
					if scenario == "empty-description-space" {
						description = " \t"
					}
				}
				return localtools.NewTrustedHost([]api.ToolDefinition{{Name: "ping", Description: description, InputSchema: map[string]any{"type": "object"}}}, func(_ context.Context, _ controller.ToolCallContext, c api.ToolCall) (api.ToolResult, error) {
					calls++
					if scenario == "stale" {
						if _, err := files.Edit(t.Context(), file.Path, file.Revision, []byte(strings.Replace(source, "input: original", "input: edited", 1))); err != nil {
							t.Fatal(err)
						}
					}
					return api.ToolResult{ID: c.ID, Output: map[string]any{"value": "private-output-canary"}}, nil
				}, func(...string) error { return nil })
			}})
			cs := connectFixture(t, Operations(files, &app.App{FileRunner: runner}, nil))
			args := map[string]any{"schemaVersion": 1, "path": file.Path}
			if scenario == "copilot" {
				args["acknowledgeCopilotTests"] = true
			}
			first, response := previewCall(t, cs, "test", args)
			wantCalls := 1
			if scenario == "empty-description" || scenario == "empty-description-space" || scenario == "empty-description-uncalled" {
				wantCalls = 0
			}
			if calls != wantCalls || resolves != 1 || first.File.Revision != file.Revision {
				t.Fatalf("first %+v calls %d resolves %d", first, calls, resolves)
			}
			current, err := files.snapshot(t.Context(), file.Path)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "empty-description-uncalled" || scenario == "empty-description-live" {
				if response.IsError || first.SavedRevision != "" || !first.FullPass || current.Revision != file.Revision || len(current.Examples[0].Mocks.Tools[0].Calls) != 0 {
					t.Fatalf("non-capture refused %+v", first)
				}
				return
			}
			if scenario == "empty-description" || scenario == "empty-description-space" {
				if !response.IsError || first.ReasonCode != "recording_unrepresentable" || first.SavedRevision != "" || first.FullPass || current.Revision != file.Revision || len(current.Examples[0].Mocks.Tools[0].Calls) != 0 {
					t.Fatalf("unrepresentable capture %+v", first)
				}
				return
			}
			if scenario == "stale" {
				if !response.IsError || first.ReasonCode != "recording_source_changed" || first.FullPass || first.SavedRevision != "" || len(current.Examples[0].Mocks.Tools[0].Calls) != 0 || current.Examples[0].Input != "edited" {
					t.Fatalf("stale overwrite %+v", first)
				}
				return
			}
			if response.IsError || first.SavedRevision != current.Revision || first.SavedRevision == first.File.Revision || !first.Examples[0].Tools[0].RecordingSaved || current.Examples[0].Mocks.Tools[0].Calls[0].Result["value"] != "private-output-canary" {
				t.Fatalf("save %+v response %+v", first, response)
			}
			if scenario == "copilot" {
				_, saved, err := files.inspect(t.Context(), file.Path)
				if err != nil || !strings.HasPrefix(string(saved), header) {
					t.Fatal("capture changed unknown frontmatter", err)
				}
			}
			want := runtime.EvaluationPass
			if scenario == "assertion-fail" {
				want = runtime.EvaluationFail
			}
			if first.Verdict != want {
				t.Fatal("save changed verdict")
			}
			second, response := previewCall(t, cs, "test", map[string]any{"schemaVersion": 1, "path": file.Path})
			if response.IsError || calls != 1 || resolves != 1 || second.SavedRevision != "" || second.File.Revision != current.Revision || second.Examples[0].Tools[0].Source != "recorded" || second.Examples[0].Tools[0].RecordingSaved {
				t.Fatalf("replay %+v calls %d", second, calls)
			}
			for _, result := range []app.FilePreviewResult{first, second} {
				raw, _ := json.Marshal(result)
				if strings.Contains(string(raw), "private-output-canary") || strings.Contains(string(raw), "private-definition-canary") {
					t.Fatal("fixture content leaked")
				}
			}
		})
	}
}
