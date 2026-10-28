//go:build unix

package localpreview

import (
	"context"
	"log/slog"
	"os"
	"slices"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestStdioPreviewCaptureThenOfflineReplay(t *testing.T) {
	req := recordingRequest(t)
	req.Examples[0].Mocks.Tools[0] = agentfile.MockTool{Name: "ping", Mode: "recorded", Calls: []agentfile.MockCall{}}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ownerCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var owned *localtools.StdioHost
	launches := 0
	dir := journalDir(t)
	runner := NewRunner(RunnerOptions{JournalDir: func() (string, error) { return dir, nil }, ResolveAvailableTools: func(ctx context.Context, names []string) (*localtools.Host, error) {
		if !slices.Equal(names, []string{"ping"}) {
			t.Fatalf("unexpected resolution: %v", names)
		}
		launches++
		var err error
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// Runner's resolver context is canceled when Run returns. The invocation
		// owns the process until it checks Close, so use its outer context.
		owned, err = localtools.StartStdioHost(ownerCtx, localtools.StdioConfig{Command: program, Args: []string{"-test.run=^TestStdioPreviewHelperProcess$", "--", "serve"}, Dir: t.TempDir(), Env: map[string]string{}, Tools: map[string]string{"ping": "ping"}})
		if err != nil {
			return nil, err
		}
		return owned.Host(), nil
	}})
	t.Cleanup(func() {
		if owned != nil {
			_ = owned.Close()
		}
	})
	out, err := runner.Run(ownerCtx, req)
	if err != nil {
		t.Fatal(err)
	}
	if err := owned.Close(); err != nil {
		t.Fatal("cleanup vetoed completion", err)
	}
	if out.State != agentpreview.Complete || out.Examples[0].Assertions[0].Verdict != runtime.EvaluationPass || out.Examples[0].Completion.Tools[0].Source != "live" || out.Examples[0].Completion.ToolCalls != 1 || len(out.Recordings) != 1 || launches != 1 {
		t.Fatalf("native fake-model capture: %+v launches=%d", out, launches)
	}
	// Use the checked captured exchange as a fixture; this is not source saving
	// or new execution evidence. Replay may resolve neither a host nor its env.
	captured := out.Recordings[0]
	req.Examples = agentfile.CloneExamples(req.Examples)
	req.Examples[0].Mocks.Tools[0] = captured.Tool
	replayed, err := NewRunner(RunnerOptions{JournalDir: func() (string, error) { return dir, nil }, ResolveAvailableTools: func(context.Context, []string) (*localtools.Host, error) {
		t.Fatal("frozen replay resolved a process or environment")
		return nil, nil
	}}).Run(testContext(t), req)
	if err != nil || replayed.State != agentpreview.Complete || replayed.Examples[0].Assertions[0].Verdict != runtime.EvaluationPass || replayed.Examples[0].Completion.Tools[0].Source != "recorded" || len(replayed.Recordings) != 0 {
		t.Fatalf("offline replay: %+v %v", replayed, err)
	}
	t.Log("fake-model walk: one real stdio dispatch/captured exchange, checked cleanup, then zero-launch recorded replay")
}

func TestStdioPreviewDisabledSelectionStartsNothing(t *testing.T) {
	req := recordingRequest(t)
	req.Examples[0].Mocks.Tools[0] = agentfile.MockTool{Name: "ping", Mode: "recorded"}
	req.ToolSettings = []localtools.ToolSetting{{Name: "ping", State: localtools.Disabled}}
	dir := journalDir(t)
	out, err := NewRunner(RunnerOptions{JournalDir: func() (string, error) { return dir, nil }, ResolveAvailableTools: func(context.Context, []string) (*localtools.Host, error) {
		// The invocation's selection owns launch filtering, not the shared app
		// bridge. PR3 will supply this filter from frozen CLI settings.
		if localtools.IsDisabled(req.ToolSettings, "ping") {
			return nil, nil
		}
		t.Fatal("Disabled selection attempted a launch")
		return nil, nil
	}}).Run(testContext(t), req)
	if err != nil || out.State != agentpreview.Complete || out.Examples[0].Completion.ToolCalls != 0 || out.Examples[0].Assertions[0].Verdict != runtime.EvaluationFail || len(out.Recordings) != 0 {
		t.Fatalf("Disabled selection: %+v %v", out, err)
	}
}

func TestStdioPreviewHelperProcess(t *testing.T) {
	if !slices.Contains(os.Args, "--") {
		return
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "preview-fixture", Version: "1"}, &mcp.ServerOptions{Logger: slog.New(slog.DiscardHandler)})
	server.AddTool(&mcp.Tool{Name: "ping", Description: "Return a value.", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{StructuredContent: map[string]any{"value": "saved"}}, nil
	})
	_ = server.Run(context.Background(), &mcp.StdioTransport{})
	os.Exit(0)
}
