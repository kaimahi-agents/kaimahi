package app

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
)

func TestFilePreviewFolderSettingsAndFrozenProvenance(t *testing.T) {
	for _, body := range []string{"", "tools: {ping: Enabled}", "tools: {ping: Disabled}", "tools: {ping: Ask}"} {
		for _, op := range []string{"run", "test"} {
			t.Run(op+body, func(t *testing.T) {
				file := previewFile(t)
				source, _ := os.ReadFile(file)
				text := strings.Replace(string(source), "PRIVATE INSTRUCTIONS", "Call the tool `ping`. Reply with exactly `PRIVATE OPERAND`.", 1)
				text = strings.Replace(text, "## Examples", "## Tools\n- ping\n\n## Examples", 1)
				if err := os.WriteFile(file, []byte(text), 0600); err != nil {
					t.Fatal(err)
				}
				yaml := filepath.Join(filepath.Dir(file), "kmx.yaml")
				if body != "" {
					if err := os.WriteFile(yaml, []byte(body), 0600); err != nil {
						t.Fatal(err)
					}
				}
				executions, resolves := 0, 0
				journals := filepath.Join(t.TempDir(), "journal")
				runner := localpreview.NewRunner(localpreview.RunnerOptions{JournalDir: func() (string, error) { return journals, nil }, ResolveTools: func(context.Context, []string) (*localtools.Host, error) {
					resolves++
					// Simulate an editor saving a new mode after the run froze its input.
					if err := os.WriteFile(yaml, []byte("tools: {ping: Enabled}"), 0600); err != nil {
						t.Fatal(err)
					}
					return localtools.NewTrustedHost([]api.ToolDefinition{{Name: "ping", InputSchema: map[string]any{"type": "object", "additionalProperties": false}}}, func(_ context.Context, _ controller.ToolCallContext, c api.ToolCall) (api.ToolResult, error) {
						executions++
						return api.ToolResult{ID: c.ID, Output: map[string]any{"ok": true}}, nil
					}, func(...string) error { return nil })
				}})
				out, err := (&App{FileRunner: runner}).PreviewAgentFile(t.Context(), FilePreviewOptions{File: file, Operation: op})
				if strings.Contains(body, "Ask") {
					if err == nil || out.ReasonCode != "invalid_tool_settings" || resolves != 0 || executions != 0 {
						t.Fatalf("invalid config ran: %+v %v %d/%d", out, err, resolves, executions)
					}
					return
				}
				state := localtools.Enabled
				want := 1
				if strings.Contains(body, "Disabled") {
					state = localtools.Disabled
					want = 0
				}
				if err != nil || !out.FullPass || resolves != 1 || executions != 2*want || !reflect.DeepEqual(out.ToolSettings, []localtools.ToolSetting{{Name: "ping", State: state}}) {
					t.Fatalf("wrong settings/proof: %+v %v live=%d", out, err, executions)
				}
				for _, ex := range out.Examples {
					if ex.ToolCalls != want {
						t.Fatal("disabled answer counted live")
					}
				}
				runs, err := localpreview.ListRuns(journals)
				if err != nil || len(runs) != 1 {
					t.Fatalf("journal: %v", err)
				}
				replay, err := localpreview.Inspect(t.Context(), journals, runs[0])
				if err != nil || replay.ModelCalls != 0 || executions != 2*want {
					t.Fatalf("replay effect: %v", err)
				}
				for _, s := range replay.Sessions {
					if s.ToolCalls != want || s.Outcome != "completed" {
						t.Fatalf("wrong replay live count: %+v", s)
					}
				}
			})
		}
	}
}

func TestSnapshotToolSettingsUseTrustedSourceNotDisplay(t *testing.T) {
	file := previewFile(t)
	a, err := agentfile.Load(file)
	if err != nil {
		t.Fatal(err)
	}
	a.Tools = []string{"ping"}
	if err := os.WriteFile(filepath.Join(filepath.Dir(file), "kmx.yaml"), []byte("tools: {ping: Disabled}"), 0600); err != nil {
		t.Fatal(err)
	}
	runner := &scriptedPreview{verdict: "pass"}
	out, err := (&App{FileRunner: runner}).PreviewAgentSnapshot(t.Context(), a, FilePreviewOptions{File: "display-only.agent.md", SourceFile: file, Operation: "test"})
	if err != nil || len(out.ToolSettings) != 1 || out.ToolSettings[0].State != localtools.Disabled {
		t.Fatalf("read display folder: %+v %v", out, err)
	}
}
