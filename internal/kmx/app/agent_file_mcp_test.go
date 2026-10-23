package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func TestProductionMCPFactoryChecksCredentialBeforeJournal(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("APPDATA", t.TempDir())
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "tools.json")
	if err = os.WriteFile(path, []byte(`{"schemaVersion":"kmx.local-tools/v1","bindings":[{"name":"github_list_merged_prs","endpointRef":"github","repository":"owner/repo"}],"endpoints":{"github":{"url":"https://api.githubcopilot.com/mcp/readonly","credentialEnv":"TEST_PREVIEW_MCP_TOKEN"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	a := &agentfile.Agent{Identity: agentfile.Identity{ID: "writer"}, Title: "Writer", Model: "fake", Instructions: "Reply with exactly Draft.", Revision: "sha256:" + strings.Repeat("a", 64), Tools: []string{"github_list_merged_prs"}, Examples: []agentfile.Example{{ID: "one", Input: "mcp-credential-canary", Assertions: []runtime.EvaluationAssertion{{ID: "text", Type: "contains", Value: "Draft"}}}}}
	request := agentpreview.Request{Agent: a, Examples: a.Examples, ToolBindings: path, Route: modelaccess.Route{Provider: "fake", Model: "fake", Auth: "none"}}
	t.Setenv("TEST_PREVIEW_MCP_TOKEN", "mcp-credential-canary")
	runner := NewLocalFileRunner()
	out, err := runner.Run(t.Context(), request)
	if !errors.Is(err, agentpreview.ErrUnsafeToolContent) || out.Journal != "" {
		t.Fatal("production host factory did not perform credential preflight")
	}
	t.Setenv("TEST_PREVIEW_MCP_TOKEN", "")
	out, err = runner.Run(t.Context(), request)
	if !errors.Is(err, agentpreview.ErrToolsUnavailable) || out.Journal != "" {
		t.Fatal("missing explicit credential did not refuse before journal")
	}
}
