package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
)

func TestFileToolsJudgeBudgetAndActualCallProof(t *testing.T) {
	opt := directOptions(t, "unit", true)
	a, err := agentfile.Load(opt.File)
	if err != nil {
		t.Fatal(err)
	}
	a.Tools = []string{localtools.Operation}
	models, judges, reads := 0, 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct{ Role, Content string } `json:"messages"`
			Tools    json.RawMessage                  `json:"tools"`
		}
		// A tool request's assistant content is null, which decodes to the empty string.
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			t.Error("bad request")
			return
		}
		if len(req.Messages) > 0 && req.Messages[0].Content == runtime.EvaluationJudgePolicy {
			judges++
			if len(req.Tools) != 0 {
				t.Error("judge offered tools")
			}
			previewEnvelope(w, "unit", `{"score":95,"reasoning":"safe"}`)
			return
		}
		models++
		if len(req.Messages) == 2 {
			fmt.Fprint(w, `{"model":"unit","choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"read","type":"function","function":{"name":"github_list_merged_prs","arguments":"{}"}}]}}]}`)
			return
		}
		previewEnvelope(w, "unit", "Hello, PRIVATE INPUT")
	}))
	defer server.Close()
	opt.Route = &modelaccess.Route{Provider: "compatible", Model: "unit", Endpoint: server.URL, Auth: "none"}
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	bindingDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bindingPath := filepath.Join(bindingDir, "tools.json")
	if err = os.WriteFile(bindingPath, []byte(`{"schemaVersion":"kmx.local-tools/v1","bindings":[{"name":"github_list_merged_prs","endpointRef":"github","repository":"owner/repo"}],"endpoints":{"github":{"url":"https://api.githubcopilot.com/mcp/readonly","credentialEnv":"GITHUB_MCP_TOKEN"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	opt.ToolBindings = bindingPath
	runner := localpreview.NewRunner(localpreview.RunnerOptions{JournalDir: func() (string, error) { return dir, nil }, Judge: localFilePreviewJudge, Tools: func(_ context.Context, b localtools.Binding) (*localtools.Host, error) {
		return localtools.NewHost(b, "tool-secret-canary", func(context.Context) ([]localtools.PullRequest, error) { reads++; return nil, nil })
	}})
	result, err := (&App{FileRunner: runner}).PreviewAgentSnapshot(t.Context(), a, opt)
	if err != nil || !result.FullPass || models != 4 || reads != 2 || judges != 4 {
		t.Fatalf("budgets %+v %v agent=%d read=%d judge=%d", result, err, models, reads, judges)
	}
	for _, ex := range result.Examples {
		if ex.ToolCalls != 1 {
			t.Fatal("missing actual-call metadata")
		}
	}
	paths, err := filepath.Glob(filepath.Join(dir, "*.db"))
	if err != nil || len(paths) != 1 {
		t.Fatal("missing journal", err)
	}
	replay, err := localpreview.Inspect(t.Context(), dir, strings.TrimSuffix(filepath.Base(paths[0]), ".db"))
	if err != nil || len(replay.Sessions) != 6 || replay.ModelCalls != 0 || models != 4 || reads != 2 || judges != 4 {
		t.Fatal("inspection dispatched model/tool/judge", err)
	}
	// Even an unused override is public metadata, so it must receive the host's
	// credential preflight before either returning it or opening another journal.
	for i := range a.Examples {
		a.Examples[i].Assertions = []runtime.EvaluationAssertion{{ID: "text", Type: "contains", Value: "Hello"}}
	}
	opt.JudgeRoute = &modelaccess.Route{Provider: "compatible", Model: "tool-secret-canary", Endpoint: server.URL, Auth: "none"}
	refused, err := (&App{FileRunner: runner}).PreviewAgentSnapshot(t.Context(), a, opt)
	encoded, _ := json.Marshal(refused)
	if err == nil || refused.ReasonCode != "unsafe_tool_content" || refused.JudgeRoute != nil || strings.Contains(string(encoded), "tool-secret-canary") || models != 4 || reads != 2 || judges != 4 {
		t.Fatalf("unused judge override leaked: %s %v", encoded, err)
	}
}

func TestFileToolsCopilotRefusesBeforeRunner(t *testing.T) {
	for _, judge := range []bool{false, true} {
		opt := directOptions(t, "fake", false)
		a, err := agentfile.Load(opt.File)
		if err != nil {
			t.Fatal(err)
		}
		a.Tools = []string{"github_list_merged_prs"}
		route := modelaccess.Route{Provider: "copilot", Model: "unit", Endpoint: modelaccess.CopilotEndpoint, Auth: "copilot"}
		if judge {
			opt.JudgeRoute = &route
		} else {
			a.Model = "unit"
			opt.Route = &route
		}
		runner := &scriptedPreview{}
		r, err := (&App{FileRunner: runner}).PreviewAgentSnapshot(t.Context(), a, opt)
		if err == nil || r.ReasonCode != "tools_unavailable" || runner.calls > 0 {
			t.Fatalf("Copilot tool request reached runner: %+v %v", r, err)
		}
	}
}

func TestDirectFileToolsNeverAccessProvider(t *testing.T) {
	opt := directOptions(t, "fake", false)
	a, err := agentfile.Load(opt.File)
	if err != nil {
		t.Fatal(err)
	}
	a.Tools = []string{"github_list_merged_prs"}
	r := &directFileRunner{}
	_, err = r.Run(t.Context(), agentpreview.Request{Agent: a, Examples: a.Examples, Route: modelaccess.Route{Provider: "fake", Model: "fake", Auth: "none"}})
	if !errors.Is(err, agentpreview.ErrToolsUnavailable) {
		t.Fatal("direct tool request not refused before provider", err)
	}
}

func TestFileToolsUnsafeContentWithholdsMetadata(t *testing.T) {
	opt := directOptions(t, "fake", false)
	a, err := agentfile.Load(opt.File)
	if err != nil {
		t.Fatal(err)
	}
	a.Tools = []string{"github_list_merged_prs"}
	a.Examples[0].ID = "tool-canary"
	opt.File = "tool-canary.agent.md"
	runner := &scriptedPreview{err: agentpreview.ErrUnsafeToolContent}
	r, err := (&App{FileRunner: runner}).PreviewAgentSnapshot(t.Context(), a, opt)
	raw, _ := json.Marshal(r)
	if err == nil || r.ReasonCode != "unsafe_tool_content" || strings.Contains(string(raw), "tool-canary") || r.FullPass {
		t.Fatalf("unsafe metadata escaped: %s %v", raw, err)
	}
}
