package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func capturePreviewPair(t *testing.T) *agentfile.Pair {
	t.Helper()
	p := previewPair(t)
	raw, err := os.ReadFile(p.AgentPath)
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.Replace(string(raw), "## Examples", "## Tools\n- ping\n\n## Examples", 1))
	raw = []byte(strings.Replace(string(raw), "PRIVATE INSTRUCTIONS", "Call the tool `ping`. Reply with exactly `done`.", 1))
	if err := os.WriteFile(p.AgentPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	tests := "---\nschemaVersion: 1\n---\nPrivate prose\n```yaml agent-test\nid: external\ninput: external input\nassertions: [{id: check, type: toolNotCalled, tool: ping}]\nmocks: {tools: [{name: ping, mode: recorded, calls: []}]}\n```\n"
	if err := os.WriteFile(p.Tests.Selection.Path, []byte(tests), 0600); err != nil {
		t.Fatal(err)
	}
	p, err = agentfile.LoadPair(p.AgentPath, p.Tests.Selection.Path)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFilePairCapturePublicationAndRetention(t *testing.T) {
	for _, scenario := range []string{"saved", "wrong-role", "journal-veto", "cancelled", "no-recording", "source-changed"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("KMX_HOME", filepath.Join(t.TempDir(), "state"))
			p := capturePreviewPair(t)
			executed := p.SnapshotRevision
			prepared, saved := 0, 0
			opt := WithPairFilePreviewRetention(FilePreviewOptions{File: "display.agent.md", Operation: "test"}, p)
			// Settings come from the physical agent, not the display or tests folders.
			if err := os.WriteFile(filepath.Join(filepath.Dir(p.AgentPath), "kmx.yaml"), []byte("tools:\n  ping: Enabled\n"), 0600); err != nil {
				t.Fatal(err)
			}
			opt.PreparePairRecordingSave = func(_ context.Context, frozen *agentfile.Pair) (PairRecordingSaver, error) {
				prepared++
				if frozen.SnapshotRevision != executed {
					t.Fatal("wrong frozen pair")
				}
				return func(_ context.Context, records []agentfile.Recording) (PairRecordingSaveResult, error) {
					saved++
					if len(records) != 1 {
						t.Fatal("missing captured recording")
					}
					ack := PairRecordingSaveResult{Revision: p.Agent.Revision, AgentRevision: p.Agent.Revision, SnapshotRevision: "sha256:" + strings.Repeat("d", 64), Target: pairRecordingTarget(p)}
					switch scenario {
					case "wrong-role":
						ack.Target.Role = "agent"
					}
					// Capture may publish changed source; retention still belongs to execution.
					raw, err := os.ReadFile(p.Tests.Selection.Path)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(p.Tests.Selection.Path, append(raw, []byte("Changed after capture\n")...), 0600); err != nil {
						t.Fatal(err)
					}
					return ack, nil
				}, nil
			}
			if scenario == "source-changed" {
				opt.ValidateSource = func(context.Context) error { return errors.New("changed") }
			}
			runner := localpreview.NewRunner(localpreview.RunnerOptions{JournalDir: func() (string, error) { return filepath.Join(t.TempDir(), "journal"), nil }, ResolveAvailableTools: func(context.Context, []string) (*localtools.Host, error) {
				if scenario == "no-recording" {
					return nil, nil
				}
				return localtools.NewTrustedHost([]api.ToolDefinition{{Name: "ping", Description: "private description", InputSchema: map[string]any{"type": "object"}}}, func(_ context.Context, _ controller.ToolCallContext, call api.ToolCall) (api.ToolResult, error) {
					return api.ToolResult{ID: call.ID, Output: map[string]any{"value": "private capture"}}, nil
				}, func(...string) error { return nil })
			}})
			if scenario == "journal-veto" || scenario == "cancelled" {
				inner := runner
				runner = pairPreviewRunner(func(ctx context.Context, r agentpreview.Request) (agentpreview.Outcome, error) {
					out, err := inner.Run(ctx, r)
					if err != nil {
						t.Fatal(err)
					}
					if scenario == "journal-veto" {
						return out, agentpreview.ErrJournalFailed
					}
					return out, context.Canceled
				})
			}
			result, err := (&App{FileRunner: runner}).PreviewAgentPairSnapshot(t.Context(), p, opt)
			if prepared != 1 {
				t.Fatal("save callback not prepared before execution")
			}
			if scenario == "saved" {
				if err == nil || result.Verdict != runtime.EvaluationFail || saved != 1 || result.SavedRevision != p.Agent.Revision || result.SnapshotRevision != executed || result.SavedSnapshotRevision == executed || result.Retention.Status != "retained" {
					t.Fatalf("valid failing capture: %+v %v", result, err)
				}
				result.DurationMs = max(result.DurationMs, result.Examples[0].DurationMs)
				var summary bytes.Buffer
				if result.RecordingTarget.Path != filepath.Base(p.Tests.Selection.Path) {
					t.Fatal("capture leaked physical target")
				}
				if err := WriteFilePreviewGitHubSummary(&summary, result); err != nil {
					t.Fatal(err)
				}
				if strings.Contains(summary.String(), p.Tests.Selection.Path) || strings.Contains(summary.String(), "private capture") {
					t.Fatal("unsafe summary")
				}
				var text bytes.Buffer
				if err := WriteFilePreview(&text, result, "text"); err != nil || !strings.Contains(text.String(), "saved tests revision:") {
					t.Fatal("saved role not displayed")
				}
				var report bytes.Buffer
				if err := (&App{Out: &report}).ExplainSuiteEvaluation(t.Context(), SuiteEvaluationExplainOptions{File: p.AgentPath, TestsFile: p.Tests.Selection.Path}); err != nil || !strings.Contains(report.String(), "historical") {
					t.Fatalf("postcapture retention: %s %v", report.String(), err)
				}
			} else if scenario == "no-recording" {
				if saved != 0 || result.SavedRevision != "" {
					t.Fatal("uncalled recording saved")
				}
			} else {
				if err == nil || result.SavedRevision != "" || result.RecordingTarget != nil || result.SavedSnapshotRevision != "" {
					t.Fatalf("accepted failed capture: %+v %v", result, err)
				}
				if (scenario == "journal-veto" || scenario == "cancelled" || scenario == "source-changed") && saved != 0 {
					t.Fatal("saved after execution veto")
				}
				if scenario == "source-changed" && result.ReasonCode != "source_changed" {
					t.Fatal("source hook misclassified")
				}
			}
		})
	}
}
