package copilot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"github.com/agent-primitives/sessions/sqlitelog"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func previewBatchReply(path string, n int, fault string) []byte {
	calls := make([]any, 0, n)
	for i := 0; i < n; i++ {
		id, name, args := fmt.Sprintf("call_%d", i), "lookup", `{"name":"Ada"}`
		if i%2 == 1 {
			name = "other"
		}
		if i == n-1 {
			switch fault {
			case "duplicate":
				id = "call_0"
			case "schema":
				args = `{"extra":true}`
			case "credential":
				args = `{"name":"\u0067` + canary[1:] + `"}`
			}
		}
		if path == "/responses" {
			calls = append(calls, map[string]any{"type": "function_call", "call_id": id, "name": name, "arguments": args})
		} else {
			calls = append(calls, map[string]any{"id": id, "type": "function", "function": map[string]string{"name": name, "arguments": args}})
		}
	}
	if path == "/responses" {
		calls = append([]any{map[string]any{"type": "reasoning", "id": "reason", "encrypted_content": "private-reasoning"}}, calls...)
		raw, _ := json.Marshal(map[string]any{"model": "gpt-test", "status": "completed", "output": calls})
		return raw
	}
	raw, _ := json.Marshal(map[string]any{"model": "gpt-test", "choices": []any{map[string]any{"finish_reason": "tool_calls", "message": map[string]any{"role": "assistant", "tool_calls": calls}}}})
	return raw
}

func TestCopilotBatchJournalAndReplay(t *testing.T) {
	for _, path := range []string{"/chat/completions", "/responses"} {
		for _, fault := range []string{"none", "six", "duplicate", "schema", "credential", "result", "extra round"} {
			t.Run(path+"/"+fault, func(t *testing.T) {
				sends, tools := 0, 0
				offline := false
				n := 5
				if fault == "six" {
					n = 6
				}
				p, _ := toolFixture(t, func(w http.ResponseWriter, r *http.Request) {
					if offline {
						t.Error("offline replay contacted provider")
					}
					if r.URL.Path == "/models" {
						io.WriteString(w, toolCatalog(path, true))
						return
					}
					sends++
					if sends == 1 {
						w.Write(previewBatchReply(path, n, fault))
						return
					}
					var body map[string]json.RawMessage
					_ = json.NewDecoder(r.Body).Decode(&body)
					if path == "/responses" {
						var input []map[string]json.RawMessage
						_ = json.Unmarshal(body["input"], &input)
						// Two initial messages, one opaque reasoning item, five calls, five outputs.
						if len(input) != 13 {
							t.Errorf("continuation length %d", len(input))
						} else {
							if string(input[2]["encrypted_content"]) != `"private-reasoning"` {
								t.Error("reasoning lost")
							}
							for i := 0; i < 5; i++ {
								if string(input[8+i]["call_id"]) != fmt.Sprintf(`"call_%d"`, i) {
									t.Error("output correlation lost")
								}
							}
						}
					} else {
						var messages []map[string]json.RawMessage
						_ = json.Unmarshal(body["messages"], &messages)
						if len(messages) != 8 {
							t.Error("lost Chat batch results")
						}
					}
					if fault == "extra round" {
						w.Write(previewBatchReply(path, 1, "none"))
						return
					}
					if path == "/responses" {
						io.WriteString(w, previewResponsesText("Hello Ada"))
					} else {
						io.WriteString(w, `{"model":"gpt-test","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"Hello Ada"}}]}`)
					}
				})
				definition := api.ToolDefinition{Name: "lookup", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}, "required": []any{"name"}, "additionalProperties": false}}
				other := definition
				other.Name = "other"
				host, err := localtools.NewTrustedHost([]api.ToolDefinition{definition, other}, func(_ context.Context, _ controller.ToolCallContext, c api.ToolCall) (api.ToolResult, error) {
					if offline {
						t.Error("replay called executor")
					}
					if c.ID != fmt.Sprintf("call_%d", tools) {
						t.Error("nonserial dispatch")
					}
					tools++
					answer := "Ada"
					if fault == "result" && tools == 2 {
						answer = canary
					}
					return api.ToolResult{ID: c.ID, Output: map[string]any{"name": answer}}, nil
				}, func(...string) error { return nil })
				if err != nil {
					t.Fatal(err)
				}
				root, err := filepath.EvalSymlinks(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				dir := filepath.Join(root, "journals")
				runner := localpreview.NewRunner(localpreview.RunnerOptions{JournalDir: func() (string, error) { return dir, nil }, Access: p.PreviewClient, ResolveTools: func(context.Context, []string) (*localtools.Host, error) { return host, nil }})
				a := &agentfile.Agent{Identity: agentfile.Identity{ID: "batch"}, Revision: "sha256:" + strings.Repeat("a", 64), Model: "gpt-test", Instructions: "Look up names", Tools: []string{"lookup", "other"}, Examples: []agentfile.Example{{ID: "hello", Input: "Ada", Assertions: []runtime.EvaluationAssertion{{ID: "text", Type: "contains", Value: "Hello Ada"}}}}}
				out, err := runner.Run(t.Context(), agentpreview.Request{Agent: a, Examples: a.Examples, Route: selected()})
				if fault == "none" {
					if err != nil || out.State != agentpreview.Complete || sends != 2 || tools != 5 || out.Examples[0].Completion.ToolCalls != 5 {
						t.Fatalf("batch failed: %+v %v sends/tools=%d/%d", out, err, sends, tools)
					}
					offline = true
					inspection, err := localpreview.Inspect(t.Context(), dir, out.Journal)
					if err != nil || len(inspection.Sessions) != 1 || inspection.Sessions[0].Outcome != "completed" || inspection.Sessions[0].ToolCalls != 5 || inspection.ModelCalls != 0 || sends != 2 || tools != 5 {
						t.Fatalf("effectful/incomplete replay: %+v %v", inspection, err)
					}
				} else {
					wantTools, wantSends := 0, 1
					if fault == "result" {
						wantTools = 2
					}
					if fault == "extra round" {
						wantTools, wantSends = 5, 2
					}
					if err == nil || out.State == agentpreview.Complete || tools != wantTools || sends != wantSends {
						t.Fatalf("fault continued: %v sends/tools=%d/%d", err, sends, tools)
					}
				}
				runs, err := localpreview.ListRuns(dir)
				if err != nil || len(runs) != 1 {
					t.Fatalf("journal missing: %v", err)
				}
				journal, _ := localpreview.JournalPath(dir, runs[0])
				slashed := filepath.ToSlash(journal)
				if !strings.HasPrefix(slashed, "/") {
					slashed = "/" + slashed
				}
				u := url.URL{Scheme: "file", Path: slashed}
				store, err := sqlitelog.Open(u.String() + "?mode=rw")
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				page, err := store.ListSessions(sqlitelog.ListOptions{})
				if err != nil || len(page.Sessions) != 1 {
					t.Fatalf("session missing: %v", err)
				}
				records, err := store.Session(page.Sessions[0].UID).Read(1)
				if err != nil {
					t.Fatal(err)
				}
				for _, r := range records {
					b, _ := json.Marshal(r.Event)
					if strings.Contains(string(b), canary) || strings.Contains(string(b), "private-reasoning") {
						t.Fatal("credential/private continuation journaled")
					}
					if fault != "none" && fault != "result" && fault != "extra round" && (r.Event.Kind == api.EventOutput || r.Event.Kind == api.EventToolCall) {
						t.Fatal("invalid batch appended before refusal")
					}
				}
			})
		}
	}
}
