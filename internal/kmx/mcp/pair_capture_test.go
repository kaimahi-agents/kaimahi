//go:build linux || darwin

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func pairedCaptureRunner(t *testing.T, live *int) agentpreview.Runner {
	t.Helper()
	return localpreview.NewRunner(localpreview.RunnerOptions{JournalDir: func() (string, error) { return filepath.Join(t.TempDir(), "journal"), nil }, ResolveAvailableTools: func(context.Context, []string) (*localtools.Host, error) {
		return localtools.NewTrustedHost([]api.ToolDefinition{{Name: "ping", Description: "Ping", InputSchema: map[string]any{"type": "object"}}}, func(_ context.Context, _ controller.ToolCallContext, call api.ToolCall) (api.ToolResult, error) {
			(*live)++
			return api.ToolResult{ID: call.ID, Output: map[string]any{"n": 1}}, nil
		}, func(...string) error { return nil })
	}})

}

type captureRunnerFunc func(context.Context, agentpreview.Request) (agentpreview.Outcome, error)

func (f captureRunnerFunc) Run(ctx context.Context, r agentpreview.Request) (agentpreview.Outcome, error) {
	return f(ctx, r)
}

func TestPairCaptureVetoAndUnavailableHistory(t *testing.T) {
	for _, kind := range []string{"journal-veto", "cancel-veto", "source-changed", "history-unavailable"} {
		t.Run(kind, func(t *testing.T) {
			base, root := historyPaths(t)
			f := historyFiles(t, root, base)
			source := []byte("---\nmodel: fake\ntools: [ping]\n---\nCall the tool `ping`. Reply with exactly `Hello Ada`.\n")
			if _, err := f.Create(t.Context(), "a.agent.md", source); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(root, "a.agent.test.md")
			suite := []byte(pairPendingTests)
			if err := os.WriteFile(target, suite, 0600); err != nil {
				t.Fatal(err)
			}
			if kind == "history-unavailable" {
				f.historyDir = func() (string, error) { return "", errors.New("private history failure") }
			}
			live := 0
			inner := pairedCaptureRunner(t, &live)
			runner := captureRunnerFunc(func(ctx context.Context, r agentpreview.Request) (agentpreview.Outcome, error) {
				out, err := inner.Run(ctx, r)
				if err != nil {
					return out, err
				}
				switch kind {
				case "journal-veto":
					return out, agentpreview.ErrJournalFailed
				case "cancel-veto":
					return out, context.Canceled
				case "source-changed":
					if err := os.WriteFile(target, []byte(pairPendingTests+"\nExternal edit\n"), 0600); err != nil {
						return out, err
					}
				}
				return out, nil
			})
			cs := connectFixture(t, Operations(f, &app.App{FileRunner: runner}, nil), func(s *sdk.Server) { AddAgentResource(s, f) })
			version := pairContextRead(t, cs, "a.agent.md", "")
			result, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "test", Arguments: map[string]any{"schemaVersion": 3, "path": "a.agent.md", "expectedSourceVersion": version}})
			if err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(result.StructuredContent)
			var body pairPreviewResult
			if json.Unmarshal(data, &body) != nil {
				t.Fatal("invalid envelope")
			}
			if kind == "history-unavailable" {
				if result.IsError || body.Preview.SavedRevision == "" || body.Capture == nil || body.Capture.History == nil || body.Capture.History.Status != "unavailable" {
					t.Fatalf("history incorrectly vetoed capture: %s", data)
				}
			} else {
				if !result.IsError || body.Preview.SavedRevision != "" || body.Preview.SavedSnapshotRevision != "" || body.Preview.RecordingTarget != nil || body.SavedSourceVersion != "" {
					t.Fatalf("failed capture forged save: %s", data)
				}
				if kind == "source-changed" {
					if body.Capture == nil || body.Capture.State != "not_saved" || body.Capture.Tests.Status != "not_saved" {
						t.Fatalf("lost refused save receipt: %s", data)
					}
				} else {
					if body.Capture != nil {
						t.Fatal("saved after close/cancellation veto")
					}
				}
				want := string(suite)
				if kind == "source-changed" {
					want += "\nExternal edit\n"
				}
				got, _ := os.ReadFile(target)
				if string(got) != want {
					t.Fatal("veto changed suite")
				}
			}
			if got, _ := os.ReadFile(filepath.Join(root, "a.agent.md")); string(got) != string(source) {
				t.Fatal("capture changed agent")
			}
			if strings.Contains(string(data), "private history failure") || strings.Contains(string(data), root) {
				t.Fatal("receipt restored private path/error")
			}
		})
	}
}

func TestPairCaptureCredentialRedactionWithholdsOuterContext(t *testing.T) {
	base, root := historyPaths(t)
	f := historyFiles(t, root, base)
	if _, err := f.Create(t.Context(), "a.agent.md", []byte("---\nmodel: fake\ntools: []\n---\nPrompt\n")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.agent.test.md"), []byte(historyTests), 0600); err != nil {
		t.Fatal(err)
	}
	runner := captureRunnerFunc(func(context.Context, agentpreview.Request) (agentpreview.Outcome, error) {
		return agentpreview.Outcome{}, agentpreview.ErrCredentialContent
	})
	cs := connectFixture(t, Operations(f, &app.App{FileRunner: runner}, nil), func(s *sdk.Server) { AddAgentResource(s, f) })
	version := pairContextRead(t, cs, "a.agent.md", "")
	result, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "test", Arguments: map[string]any{"schemaVersion": 3, "path": "a.agent.md", "expectedSourceVersion": version}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(result.StructuredContent)
	var body pairPreviewResult
	json.Unmarshal(raw, &body)
	t.Logf("ACK=%s", raw)
	if body.Preview.ReasonCode != "credential_content" {
		t.Fatal("did not exercise redaction")
	}
	if body.SourceVersion != "" {
		t.Error("executed sourceVersion survives App credential redaction")
	}
}

func TestPairCaptureAfterCloseHistoryAndReplay(t *testing.T) {
	base, root := historyPaths(t)
	f := historyFiles(t, root, base)
	source := []byte("---\nmodel: fake\ntools: [ping]\n---\nCall the tool `ping`. Reply with exactly `Hello Ada`.\n")
	if _, err := f.Create(t.Context(), "a.agent.md", source); err != nil {
		t.Fatal(err)
	}
	suite := []byte(strings.ReplaceAll(pairPendingTests, "\n", "\r\n"))
	target := filepath.Join(root, "a.agent.test.md")
	if err := os.WriteFile(target, suite, 0600); err != nil {
		t.Fatal(err)
	}
	live := 0
	runner := pairedCaptureRunner(t, &live)
	cs := connectFixture(t, Operations(f, &app.App{FileRunner: runner}, nil), func(s *sdk.Server) { AddAgentResource(s, f) })
	version := pairContextRead(t, cs, "a.agent.md", "")
	result, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "test", Arguments: map[string]any{"schemaVersion": 3, "path": "a.agent.md", "expectedSourceVersion": version}})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(result.StructuredContent)
	var body struct {
		SourceVersion, SavedSourceVersion string
		Preview                           app.FilePreviewResult
		Capture                           struct {
			State   string
			History *FileHistoryStatus
		}
	}
	if json.Unmarshal(data, &body) != nil || result.IsError || !body.Preview.FullPass || body.Preview.SavedSnapshotRevision == "" || body.Preview.SnapshotRevision == body.Preview.SavedSnapshotRevision || body.SavedSourceVersion == "" || body.SourceVersion != version || body.Capture.State != "complete" || body.Capture.History == nil || body.Capture.History.Status != "retained" || live != 1 {
		t.Fatalf("capture missing/false: %s live%d", data, live)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "a.agent.md")); string(got) != string(source) {
		t.Fatal("external capture changed agent")
	}
	got, err := os.ReadFile(target)
	if err != nil || !strings.Contains(string(got), "# Keep this prose\r\n") || !strings.Contains(string(got), "\r\n") || string(got) == string(suite) {
		t.Fatal("sparse capture lost exact prose/CRLF", err)
	}
	refreshed := pairContextRead(t, cs, "a.agent.md", "")
	if refreshed != body.SavedSourceVersion {
		t.Fatal("saved context not current")
	}
	result, err = cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "test", Arguments: map[string]any{"schemaVersion": 3, "path": "a.agent.md", "expectedSourceVersion": refreshed, "recordedReplayOnly": true}})
	if err != nil {
		t.Fatal(err)
	}
	data, _ = json.Marshal(result.StructuredContent)
	var replay struct{ Preview app.FilePreviewResult }
	if json.Unmarshal(data, &replay) != nil || result.IsError || !replay.Preview.FullPass || replay.Preview.SavedRevision != "" || live != 1 {
		t.Fatalf("replay recaptured: %s live%d", data, live)
	}
	view, err := cs.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: sourcesURI("a.agent.md", "")})
	if err != nil {
		t.Fatal(err)
	}
	var d sourceDocument
	if json.Unmarshal([]byte(view.Contents[0].Text), &d) != nil || d.History == nil || len(d.History.Versions) != 1 || d.History.Versions[0].SnapshotRevision != body.Preview.SnapshotRevision {
		t.Fatal("capture preimage absent")
	}
	result, err = cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "edit", Arguments: map[string]any{"schemaVersion": 3, "path": "a.agent.md", "expectedSourceVersion": refreshed, "restoreVersionId": d.History.Versions[0].VersionID}})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("restore: %+v", result.StructuredContent)
	}
	if got, _ := os.ReadFile(target); string(got) != string(suite) {
		t.Fatal("restore did not preserve original exact suite")
	}
}
