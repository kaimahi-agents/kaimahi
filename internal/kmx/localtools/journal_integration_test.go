package localtools_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func fixtureBinding(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal("fixture path")
	}
	path := filepath.Join(dir, "binding.json")
	if os.WriteFile(path, []byte(`{"schemaVersion":"kmx.local-tools/v1","bindings":[{"name":"github_list_merged_prs","endpointRef":"github","repository":"owner/repo"}],"endpoints":{"github":{"url":"https://api.githubcopilot.com/mcp/readonly","credentialEnv":"TEST_MCP_TOKEN"}}}`), 0600) != nil {
		t.Fatal("binding fixture")
	}
	return path
}

// scriptedJournal is intentionally not real-LLM qualification. It exercises
// actual HTTP conversion, MCP dispatch, journal acceptance and zero-send replay.
func scriptedJournal(t *testing.T, binding string, factory func(context.Context, localtools.Binding) (*localtools.Host, error), unsafe bool, checkAfter func()) {
	t.Helper()
	var models atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		models.Add(1)
		var body struct {
			Messages []struct{ Role, Content string }
			Tools    []any
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid model request")
			w.WriteHeader(400)
			return
		}
		if len(body.Messages) == 2 {
			if len(body.Tools) != 1 {
				t.Error("tool not offered")
			}
			fmt.Fprint(w, `{"model":"unit","choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"read","type":"function","function":{"name":"github_list_merged_prs","arguments":"{}"}}]}}]}`)
		} else {
			if len(body.Messages) != 4 || body.Messages[3].Role != "tool" || len(body.Tools) != 1 {
				t.Error("uncorrelated final request")
			}
			fmt.Fprint(w, `{"model":"unit","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"Draft ready"}}]}`)
		}
	}))
	defer server.Close()
	a := &agentfile.Agent{Identity: agentfile.Identity{ID: "writer"}, Title: "Writer", Model: "unit", Instructions: "Read the host-selected merged PRs, then draft release notes.", Revision: "sha256:" + strings.Repeat("a", 64), Tools: []string{localtools.Operation}, Examples: []agentfile.Example{{ID: "draft", Input: "Draft release notes", Assertions: []runtime.EvaluationAssertion{{ID: "text", Type: "contains", Value: "Draft"}}}}}
	dir := filepath.Join(t.TempDir(), "journal")
	runner := localpreview.NewRunner(localpreview.RunnerOptions{JournalDir: func() (string, error) { return dir, nil }, Tools: factory})
	out, err := runner.Run(t.Context(), agentpreview.Request{Agent: a, Examples: a.Examples, ToolBindings: binding, Route: modelaccess.Route{Provider: "compatible", Model: "unit", Endpoint: server.URL, Auth: "none"}})
	if unsafe {
		if err == nil || models.Load() != 1 {
			t.Fatal("unsafe MCP result reached final model")
		}
		return
	}
	if err != nil || out.State != agentpreview.Complete || models.Load() != 2 || len(out.Examples) != 1 || out.Examples[0].Completion.ToolCalls != 1 {
		t.Fatal("scripted adapter/journal qualification failed")
	}
	if checkAfter != nil {
		checkAfter()
	}
	b, loadErr := localtools.Load(binding)
	if loadErr != nil {
		t.Fatal("binding fixture unavailable")
	}
	t.Setenv(b.CredentialEnv(), "")
	replay, err := localpreview.Inspect(t.Context(), dir, out.Journal)
	if err != nil || len(replay.Sessions) != 1 || replay.Sessions[0].Outcome != "completed" || replay.Sessions[0].ToolCalls != 1 || replay.ModelCalls != 0 || models.Load() != 2 {
		t.Fatal("offline tool proof failed")
	}
	if checkAfter != nil {
		checkAfter()
	}
	// No actual content is logged; the private temporary journal is removed by t.
}

func TestMCPAdapterJournalAndOfflineReplay(t *testing.T) {
	for _, unsafe := range []bool{false, true} {
		t.Run(fmt.Sprint(unsafe), func(t *testing.T) {
			text := `{"total_count":1,"incomplete_results":false,"items":[{"number":7,"title":"Fix parser","repository_url":"https://api.github.com/repos/owner/repo","pull_request":{"merged_at":"2026-10-23T00:00:00Z"}}]}`
			if unsafe {
				text = strings.Replace(text, "Fix parser", "mcp-credential-canary", 1)
			}
			f := localtools.MCPFixtureForTest(t, text)
			scriptedJournal(t, fixtureBinding(t), func(_ context.Context, b localtools.Binding) (*localtools.Host, error) {
				return localtools.NewMCPHostForTest(b, "mcp-credential-canary", f.Transport)
			}, unsafe, func() {
				if f.Calls.Load() != 1 {
					t.Fatal("MCP read repeated during replay")
				}
			})
		})
	}
}

func TestHostedMCPWithScriptedModelJournal(t *testing.T) {
	path := os.Getenv("KMX_TEST_TOOL_BINDINGS")
	if path == "" {
		t.Skip("opt-in hosted read; scripted model, not live LLM qualification")
	}
	// Selection is explicit and credentials are resolved only by production Host.
	scriptedJournal(t, path, localtools.NewMCPHost, false, nil)
}
