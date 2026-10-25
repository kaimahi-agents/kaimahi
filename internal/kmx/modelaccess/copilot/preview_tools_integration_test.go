package copilot

import (
	"context"
	"errors"
	"io"
	"net/http"
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

func TestCopilotBoundToolsJournalAndReplay(t *testing.T) {
	for _, path := range []string{"/chat/completions", "/responses"} {
		for _, fault := range []string{"none", "arguments", "result-credential", "revoked"} {
			t.Run(path+"/"+fault, func(t *testing.T) {
				sends, tools := 0, 0
				offline := false
				var p *Provider
				p, _ = toolFixture(t, func(w http.ResponseWriter, r *http.Request) {
					if offline {
						t.Error("offline replay contacted provider")
					}
					if r.URL.Path == "/models" {
						io.WriteString(w, toolCatalog(path, true))
						return
					}
					sends++
					if sends == 1 {
						if path == "/responses" {
							reply := responsesToolReply
							if fault == "arguments" {
								reply = strings.Replace(reply, `\"name\":\"Ada\"`, `\"url\":\"bad\"`, 1)
							}
							io.WriteString(w, reply)
						} else {
							reply := `{"model":"gpt-test","choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"name\":\"Ada\"}"}}]}}]}`
							if fault == "arguments" {
								reply = strings.Replace(reply, `\"name\":\"Ada\"`, `\"url\":\"bad\"`, 1)
							}
							io.WriteString(w, reply)
						}
						if fault == "revoked" {
							p.mu.Lock()
							p.generation++
							p.mu.Unlock()
						}
					} else if path == "/responses" {
						io.WriteString(w, previewResponsesText("Hello Ada"))
					} else {
						io.WriteString(w, `{"model":"gpt-test","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"Hello Ada"}}]}`)
					}
				})
				definition := api.ToolDefinition{Name: "lookup", Description: "Read a name", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}, "required": []any{"name"}, "additionalProperties": false}}
				host, err := localtools.NewTrustedHost([]api.ToolDefinition{definition}, func(_ context.Context, _ controller.ToolCallContext, call api.ToolCall) (api.ToolResult, error) {
					if offline {
						t.Error("offline replay executed tool")
					}
					tools++
					if call.Tool != "lookup" || call.Args["name"] != "Ada" {
						t.Error("wrong tool/arguments")
					}
					answer := "Ada"
					if fault == "result-credential" {
						answer = canary
					}
					return api.ToolResult{ID: call.ID, Output: map[string]any{"name": answer}}, nil
				}, func(...string) error { return nil })
				if err != nil {
					t.Fatal(err)
				}
				root, err := filepath.EvalSymlinks(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				dir := filepath.Join(root, "journals")
				runner := localpreview.NewRunner(localpreview.RunnerOptions{JournalDir: func() (string, error) { return dir, nil }, Access: p.PreviewClient, ResolveTools: func(context.Context, []string) (*localtools.Host, error) {
					if offline {
						t.Error("offline replay bound tools")
					}
					return host, nil
				}})
				agent := &agentfile.Agent{Identity: agentfile.Identity{ID: "tool-test"}, Revision: "sha256:" + strings.Repeat("a", 64), Model: "gpt-test", Instructions: "Use lookup", Tools: []string{"lookup"}, Examples: []agentfile.Example{{ID: "hello", Input: "Ada", Assertions: []runtime.EvaluationAssertion{{ID: "text", Type: "contains", Value: "Hello Ada"}}}}}
				out, err := runner.Run(t.Context(), agentpreview.Request{Agent: agent, Examples: agent.Examples, Route: selected()})
				if fault != "none" {
					if err == nil || out.State == agentpreview.Complete || sends != 1 {
						t.Fatalf("fault passed/continued: %v sends=%d", err, sends)
					}
					if fault == "arguments" || fault == "revoked" {
						if tools != 0 {
							t.Fatal("invalid/revoked call executed")
						}
					} else if tools != 1 {
						t.Fatal("wrong executor count")
					}
					if fault == "revoked" && !errors.Is(err, ErrAuthRevoked) {
						t.Fatalf("lost credential refusal: %v", err)
					}
				} else {
					if err != nil || out.State != agentpreview.Complete || out.Journal == "" || sends != 2 || tools != 1 || out.Examples[0].Completion.Answer != "Hello Ada" || out.Examples[0].Completion.ToolCalls != 1 {
						t.Fatalf("bound preview failed: %v sends=%d tools=%d", err, sends, tools)
					}
					offline = true
					replay, err := localpreview.Inspect(t.Context(), dir, out.Journal)
					if err != nil || len(replay.Sessions) != 1 || replay.Sessions[0].Outcome != "completed" || replay.Sessions[0].ToolCalls != 1 || replay.ModelCalls != 0 || sends != 2 || tools != 1 {
						t.Fatalf("replay was not effect-free: %v", err)
					}
				}
				if err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
					if err != nil {
						return err
					}
					if d.IsDir() {
						return nil
					}
					b, err := os.ReadFile(path)
					if err == nil && (strings.Contains(string(b), canary) || strings.Contains(string(b), "private-reasoning")) {
						t.Error("credential/private continuation entered journal")
					}
					return err
				}); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
