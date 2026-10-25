package localpreview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
)

func toolBindingFile(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "tools.json")
	err = os.WriteFile(p, []byte(`{"schemaVersion":"kmx.local-tools/v1","bindings":[{"name":"github_list_merged_prs","endpointRef":"github","repository":"owner/repo"}],"endpoints":{"github":{"url":"https://api.githubcopilot.com/mcp/readonly","credentialEnv":"GITHUB_MCP_TOKEN"}}}`), 0600)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestToolRunnerOptionalCallCountsAndReplay(t *testing.T) {
	for _, useTool := range []bool{false, true} {
		t.Run(fmt.Sprint(useTool), func(t *testing.T) {
			req := runnerRequest(t)
			req.Agent.Tools = []string{localtools.Operation}
			req.Agent.Model = "unit"
			req.ToolBindings = toolBindingFile(t)
			models, reads := 0, 0
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				models++
				var body struct {
					Messages []toolWireMessage `json:"messages"`
				}
				if json.NewDecoder(r.Body).Decode(&body) != nil {
					t.Fatal("bad request")
				}
				if useTool && len(body.Messages) == 2 {
					fmt.Fprint(w, `{"model":"unit","choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"read","type":"function","function":{"name":"github_list_merged_prs","arguments":"{}"}}]}}]}`)
					return
				}
				fmt.Fprint(w, `{"model":"unit","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"Hello, Ada and Grace"}}]}`)
			}))
			defer s.Close()
			req.Route = modelaccess.Route{Provider: "compatible", Model: "unit", Endpoint: s.URL, Auth: "none"}
			dir := journalDir(t)
			r := NewRunner(RunnerOptions{JournalDir: func() (string, error) { return dir, nil }, Tools: func(_ context.Context, b localtools.Binding) (*localtools.Host, error) {
				return localtools.NewHost(b, "tool-secret-canary", func(context.Context) ([]localtools.PullRequest, error) {
					reads++
					return []localtools.PullRequest{{Number: 1, Title: "Fix", Repository: "owner/repo", Merged: true}}, nil
				})
			}})
			out, err := r.Run(testContext(t), req)
			if err != nil || out.State != agentpreview.Complete {
				t.Fatalf("out %+v err %v", out, err)
			}
			want := 0
			if useTool {
				want = 1
			}
			if models != 2*(want+1) || reads != 2*want {
				t.Fatal("wrong bounded counts", models, reads)
			}
			for _, ex := range out.Examples {
				if ex.Completion.ToolCalls != want {
					t.Fatal("lost tool count")
				}
			}
			replay, err := Inspect(testContext(t), dir, out.Journal)
			if err != nil || len(replay.Sessions) != 2 {
				t.Fatal("replay", err)
			}
			for _, session := range replay.Sessions {
				if session.Outcome != "completed" || session.ToolCalls != want || session.ModelCalls != 0 {
					t.Fatalf("bad offline proof %+v", session)
				}
			}
		})
	}
}

func TestToolRunnerPreflightAndUnboundCopilotHaveNoEffects(t *testing.T) {
	for _, field := range []string{"input", "instruction", "native task", "example ID", "assertion ID", "file label", "judge model", "copilot agent", "copilot judge", "missing factory"} {
		t.Run(field, func(t *testing.T) {
			req := runnerRequest(t)
			req.Agent.Tools = []string{localtools.Operation}
			req.ToolBindings = toolBindingFile(t)
			switch field {
			case "input":
				req.Examples[0].Input = "tool-secret-canary"
			case "instruction":
				req.Agent.Instructions = "tool-secret-canary"
			case "native task":
				req.Agent.NativeTaskInstructions = "tool-secret-canary"
			case "example ID":
				req.Examples[0].ID = "tool-secret-canary"
			case "assertion ID":
				req.Examples[0].Assertions[0].ID = "tool-secret-canary"
			case "file label":
				req.FileLabel = "tool-secret-canary"
			case "judge model":
				req.JudgeRoute = &modelaccess.Route{Provider: "compatible", Model: "tool-secret-canary", Endpoint: "http://127.0.0.1:1", Auth: "none"}
			case "copilot agent":
				req.ToolBindings = ""
				req.Route = modelaccess.Route{Provider: "copilot", Model: "fake", Endpoint: modelaccess.CopilotEndpoint, Auth: "copilot"}
			case "copilot judge":
				req.ToolBindings = ""
				req.JudgeRoute = &modelaccess.Route{Provider: "copilot", Model: "judge", Endpoint: modelaccess.CopilotEndpoint, Auth: "copilot"}
			}
			factory := func(_ context.Context, b localtools.Binding) (*localtools.Host, error) {
				if strings.HasPrefix(field, "copilot") {
					t.Fatal("credential factory reached for Copilot")
				}
				return localtools.NewHost(b, "tool-secret-canary", func(context.Context) ([]localtools.PullRequest, error) { t.Fatal("preflight read"); return nil, nil })
			}
			options := RunnerOptions{JournalDir: func() (string, error) { t.Fatal("preflight opened journal"); return "", nil }, Tools: factory}
			if field == "missing factory" {
				options.Tools = nil
			}
			out, err := NewRunner(options).Run(t.Context(), req)
			expected := agentpreview.ErrUnsafeToolContent
			if strings.HasPrefix(field, "copilot") {
				expected = agentpreview.ErrToolsUnavailable
			}
			if field == "missing factory" {
				expected = agentpreview.ErrToolsUnavailable
			}
			if !errors.Is(err, expected) || out.State == agentpreview.Complete || out.Journal != "" {
				t.Fatal("wrong preflight refusal", err)
			}
			if strings.Contains(fmt.Sprint(out), "tool-secret-canary") {
				t.Fatal("preflight leaked metadata")
			}
		})
	}
}
