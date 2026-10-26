package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func TestFileCaptureAcknowledgmentAndFailure(t *testing.T) {
	for _, scenario := range []string{"saved", "assertion-fail", "stale", "bounds", "io", "invalid-ack", "no-capability", "preflight", "uncalled", "unavailable", "prepare-cancel", "save-cancel", "ad-hoc", "disabled", "replay-only", "settings-changed"} {
		t.Run(scenario, func(t *testing.T) {
			opt := directOptions(t, "fake", false)
			parsed, err := agentfile.Load(opt.File)
			if err != nil {
				t.Fatal(err)
			}
			parsed.Tools = []string{"ping"}
			parsed.Instructions = "Call the tool `ping`. Reply with exactly `done`."
			parsed.Examples = parsed.Examples[:1]
			parsed.Examples[0].Mocks = &agentfile.Mocks{Tools: []agentfile.MockTool{{Name: "ping", Mode: "recorded", Calls: []agentfile.MockCall{}}}}
			parsed.Examples[0].Assertions = []runtime.EvaluationAssertion{{ID: "called", Type: "toolCalled", Tool: "ping"}}
			if scenario == "assertion-fail" {
				parsed.Examples[0].Assertions[0].Type = "toolNotCalled"
			}
			if scenario == "uncalled" {
				parsed.Instructions = "Reply with exactly `done`."
				parsed.Examples[0].Assertions[0].Type = "toolNotCalled"
			}
			wantCalls := 1
			if scenario == "ad-hoc" {
				input := "one-off"
				opt.Operation, opt.Input = "run", &input
				wantCalls = 2
			}
			if scenario == "replay-only" {
				opt.RecordedReplayOnly = true
			}
			if scenario == "settings-changed" {
				expected := []localtools.ToolSetting{{Name: "ping", State: localtools.Disabled}}
				opt.ExpectedToolSettings = &expected
			}
			prepared, saved, called := 0, 0, 0
			savedRevision := "sha256:" + strings.Repeat("b", 64)
			opt.PrepareRecordingSave = func(_ context.Context, rev string) (RecordingSaver, error) {
				prepared++
				if rev != parsed.Revision {
					t.Fatal("wrong executed revision")
				}
				if scenario == "prepare-cancel" {
					return nil, context.Canceled
				}
				if scenario == "preflight" {
					return nil, agentfile.ErrRecordingChanged
				}
				return func(_ context.Context, records []agentfile.Recording) (string, error) {
					saved++
					if len(records) != 1 || records[0].Tool.Calls[0].Result["private"] != "capture-canary" {
						t.Fatal("missing private capture")
					}
					switch scenario {
					case "save-cancel":
						return "", context.Canceled
					case "stale":
						return "", agentfile.ErrRecordingChanged
					case "bounds":
						return "", agentfile.ErrRecordingInvalid
					case "io":
						return "", agentfile.ErrRecordingSave
					case "invalid-ack":
						return "unsafe-ack", nil
					}
					return savedRevision, nil
				}, nil
			}
			if scenario == "no-capability" || scenario == "disabled" {
				opt.PrepareRecordingSave = nil
			}
			if scenario == "disabled" {
				if err := os.WriteFile(filepath.Join(filepath.Dir(opt.File), "kmx.yaml"), []byte("tools:\n  ping: Disabled\n"), 0644); err != nil {
					t.Fatal(err)
				}
				parsed.Examples[0].Assertions[0].Type = "toolNotCalled"
			}
			base, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			runner := localpreview.NewRunner(localpreview.RunnerOptions{JournalDir: func() (string, error) { return filepath.Join(base, "journal"), nil }, ResolveAvailableTools: func(context.Context, []string) (*localtools.Host, error) {
				if scenario == "unavailable" {
					return nil, nil
				}
				return localtools.NewTrustedHost([]api.ToolDefinition{{Name: "ping", Description: "capture-definition-canary", InputSchema: map[string]any{"type": "object"}}}, func(_ context.Context, _ controller.ToolCallContext, c api.ToolCall) (api.ToolResult, error) {
					called++
					return api.ToolResult{ID: c.ID, Output: map[string]any{"private": "capture-canary"}}, nil
				}, func(...string) error { return nil })
			}})
			result, err := (&App{FileRunner: runner}).PreviewAgentSnapshot(context.Background(), parsed, opt)
			success := scenario == "saved" || scenario == "assertion-fail" || scenario == "ad-hoc"
			if success {
				if result.SavedRevision != savedRevision || result.File.Revision != parsed.Revision || !result.Examples[0].Tools[0].RecordingSaved || result.Examples[0].Tools[0].Source != "live" || saved != 1 || called != wantCalls {
					t.Fatalf("result %+v error %v", result, err)
				}
				if (err == nil) != (scenario != "assertion-fail") {
					t.Fatalf("assertion status %v", err)
				}
				if scenario == "ad-hoc" && (result.AdHoc == nil || result.AdHoc.Tools[0].Mode != "live" || result.AdHoc.Tools[0].RecordingSaved) {
					t.Fatal("ad-hoc borrowed test capture")
				}
			} else if scenario == "uncalled" || scenario == "unavailable" || scenario == "disabled" {
				if err != nil || saved != 0 || called != 0 || result.SavedRevision != "" {
					t.Fatalf("unobserved capture %+v %v", result, err)
				}
			} else {
				if err == nil || result.FullPass || result.Verdict != runtime.EvaluationUnknown || result.SavedRevision != "" {
					t.Fatalf("save refusal %+v %v", result, err)
				}
				if scenario == "preflight" || scenario == "no-capability" || scenario == "prepare-cancel" || scenario == "replay-only" || scenario == "settings-changed" {
					if called != 0 || saved != 0 {
						t.Fatal("preflight had effects")
					}
				} else if saved != 1 || called != 1 {
					t.Fatal("save failure retried")
				}
			}
			if scenario == "no-capability" && (result.ReasonCode != "recording_save_failed" || result.Reason != "Recording save failed." || result.State != "refused") {
				t.Fatalf("preflight described execution: %+v", result)
			}
			if scenario == "disabled" && (prepared != 0 || result.ToolSettings[0].State != localtools.Disabled) {
				t.Fatal("disabled capture requested source write authority")
			}
			if (scenario == "replay-only" || scenario == "settings-changed") && (prepared != 0 || result.ReasonCode != map[string]string{"replay-only": "recording_missing", "settings-changed": "tool_settings_changed"}[scenario]) {
				t.Fatalf("precondition effects/reason %+v prepared%d", result, prepared)
			}
			if scenario != "replay-only" && scenario != "settings-changed" && scenario != "no-capability" && scenario != "disabled" && prepared != 1 {
				t.Fatalf("prepared %d", prepared)
			}
			raw, _ := json.Marshal(result)
			var text bytes.Buffer
			if err := WriteFilePreview(&text, result, "text"); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw)+text.String(), "capture-canary") || strings.Contains(string(raw)+text.String(), "capture-definition-canary") {
				t.Fatal("private capture leaked")
			}
			if success && !strings.Contains(text.String(), "recorded just now") {
				t.Fatal("new capture described as replay")
			}
			var previewErr *FilePreviewError
			if (scenario == "prepare-cancel" || scenario == "save-cancel") && (!errors.As(err, &previewErr) || previewErr.ExitCode() != 130 || result.State != "cancelled") {
				t.Fatal("lost cancellation status")
			}
			if scenario == "assertion-fail" && (!errors.As(err, &previewErr) || previewErr.ExitCode() != 2) {
				t.Fatal("lost failed assertion")
			}
		})
	}
}
