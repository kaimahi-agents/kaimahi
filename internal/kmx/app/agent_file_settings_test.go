package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
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

// Cancel at the loader's first context check, after the App's precheck.
type cancelDuringSettingsRead struct {
	context.Context
	cancel context.CancelFunc
	checks atomic.Int32
}

func (c *cancelDuringSettingsRead) Err() error {
	if c.checks.Add(1) == 2 {
		c.cancel()
	}
	return c.Context.Err()
}

func TestFilePreviewCancellationDuringSettingsRead(t *testing.T) {
	file := previewFile(t)
	agent, err := agentfile.Load(file)
	if err != nil {
		t.Fatal(err)
	}
	agent.Tools = []string{"ping"}
	if err := os.WriteFile(filepath.Join(filepath.Dir(file), "kmx.yaml"), []byte("tools: {ping: Disabled}"), 0600); err != nil {
		t.Fatal(err)
	}
	base, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx := &cancelDuringSettingsRead{Context: base, cancel: cancel}
	runner := &scriptedPreview{verdict: "pass"}
	result, err := (&App{FileRunner: runner}).PreviewAgentSnapshot(ctx, agent, FilePreviewOptions{File: file, Operation: "test"})
	var previewErr *FilePreviewError
	if base.Err() != context.Canceled || !errors.As(err, &previewErr) || previewErr.ExitCode() != 130 || result.State != "cancelled" || result.ReasonCode != "cancelled" || result.FullPass || runner.calls != 0 {
		t.Fatalf("settings IO cancellation lost: %+v, %v; runner=%d", result, err, runner.calls)
	}
}

func TestFilePreviewTextIncludesFrozenToolStates(t *testing.T) {
	result := FilePreviewResult{State: "complete", ToolSettings: []localtools.ToolSetting{{Name: "ping", State: localtools.Disabled}, {Name: "lookup", State: localtools.Enabled}}, Examples: []FilePreviewExample{{ID: "one", FilePreviewCompletion: FilePreviewCompletion{Tools: []agentpreview.ToolSummary{{Name: "ping", Mode: "live", Source: "live"}, {Name: "lookup", Mode: "recorded", Source: "recorded", CallCount: 1}}}}}}
	var output bytes.Buffer
	if err := WriteFilePreview(&output, result, "text"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"tool setting ping: Disabled", "tool setting lookup: Enabled", "tool ping: Not called; mode=live source=live", "tool lookup: 1 call(s); mode=recorded source=recorded"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("missing %q: %s", want, output.String())
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
