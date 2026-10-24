package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess/copilot"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

type previewProviderWire func(*http.Request) (*http.Response, error)

func (f previewProviderWire) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSessionsProviderCredentialsNeverEnterJournalLogsOrResults(t *testing.T) {
	for _, protocol := range []string{"chat", "responses", "chat-agent", "responses-agent"} {
		for _, echo := range []string{"", "source", "bearer", "judge-bearer", "source-control", "bearer-control", "source-ansi", "bearer-ansi", "judge-bearer-control"} {
			t.Run(protocol+"/"+echo, func(t *testing.T) {
				agentPath, judgePath := "/chat/completions", "/chat/completions"
				if protocol == "responses" || protocol == "responses-agent" {
					agentPath = "/responses"
				}
				if protocol == "responses" || protocol == "chat-agent" {
					judgePath = "/responses"
				}
				offline, accessCalls := false, 0
				t.Setenv("KMX_HOME", t.TempDir())
				t.Setenv("COPILOT_GITHUB_TOKEN", "opaque-source-token")
				const source = "opaque-source-token"
				const bearer = "opaque-exchanged-token"
				chats, exchanges, catalogs := 0, 0, 0
				old := http.DefaultTransport
				t.Cleanup(func() { http.DefaultTransport = old })
				http.DefaultTransport = previewProviderWire(func(req *http.Request) (*http.Response, error) {
					if offline {
						t.Error("replay contacted provider")
					}
					status, body := 200, ""
					switch req.URL.Path {
					case "/models":
						catalogs++
						if req.Header.Get("Authorization") != "Bearer "+bearer {
							status = 401
							body = `{"error":{"code":"unsupported_token"}}`
						} else {
							body = fmt.Sprintf(`{"data":[{"id":"gpt-test","capabilities":{"type":"chat"},"policy":{"state":"enabled"},"supported_endpoints":[%q]},{"id":"judge-test","capabilities":{"type":"chat"},"policy":{"state":"enabled"},"supported_endpoints":[%q]}]}`, agentPath, judgePath)
						}
					case "/copilot_internal/v2/token":
						exchanges++
						body = `{"token":"` + bearer + `","expires_at":4102444800}`
					case "/chat/completions", "/responses":
						chats++
						if req.Header.Get("Authorization") != "Bearer "+bearer {
							t.Error("wrong upstream credential")
						}
						var payload struct {
							directChat
							Input           []directMessage
							Store           *bool
							MaxOutputTokens int `json:"max_output_tokens"`
						}
						if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
							t.Error(err)
						}
						if req.URL.Path == "/responses" {
							wantBudget := 0
							if payload.Model == "judge-test" {
								wantBudget = 1024
							}
							if payload.Store == nil || *payload.Store || payload.MaxOutputTokens != wantBudget || payload.Stream || len(payload.Messages) != 0 {
								t.Error("wrong Responses wire parameters")
							}
							payload.Messages = payload.Input
						}
						wantPath := agentPath
						if payload.Model == "judge-test" {
							wantPath = judgePath
						}
						if req.URL.Path != wantPath {
							t.Error("mixed model protocol substituted")
						}
						// The HTTP boundary sees credentials in headers only, never harness messages.
						for _, m := range payload.Messages {
							if strings.Contains(m.Content, source) || strings.Contains(m.Content, bearer) {
								t.Error("credential entered harness/model content")
							}
						}
						answer := "Hello, PRIVATE INPUT"
						judge := payload.Messages[0].Content == runtime.EvaluationJudgePolicy
						if judge {
							answer = `{"score":95,"reasoning":"good"}`
						}
						if echo == "source" {
							answer = source
						}
						if echo == "bearer" {
							answer = bearer
						}
						if echo == "judge-bearer" && judge {
							answer = `{"score":95,"reasoning":"` + bearer + `"}`
						}
						switch echo {
						case "source-control":
							answer = strings.Replace(source, "-", "-\x00", 1)
						case "bearer-control":
							answer = strings.Replace(bearer, "-", "-\x01", 1)
						case "source-ansi":
							answer = strings.Replace(source, "-", "-\x1b[31m", 1)
						case "bearer-ansi":
							answer = strings.Replace(bearer, "-", "-\x1b]0;title\x07", 1)
						case "judge-bearer-control":
							if judge {
								b, _ := json.Marshal(map[string]any{"score": 95, "reasoning": strings.Replace(bearer, "-", "-\x00", 1)})
								answer = string(b)
							}
						}
						var envelope map[string]any
						if req.URL.Path == "/responses" {
							envelope = map[string]any{"model": payload.Model, "status": "completed", "output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]string{"type": "output_text", "text": answer}}}}}
						} else {
							envelope = map[string]any{"model": payload.Model, "choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"role": "assistant", "content": answer}}}}
						}
						b, _ := json.Marshal(envelope)
						body = string(b)
					default:
						t.Fatalf("unexpected provider request %s", req.URL.Path)
					}
					return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
				})
				useJudge := !strings.Contains(echo, "-control") && !strings.Contains(echo, "-ansi") || strings.HasPrefix(echo, "judge-")
				opt := directOptions(t, "gpt-test", useJudge)
				route := copilotPreviewRoute()
				opt.Route = &route
				if useJudge {
					data, err := os.ReadFile(opt.File)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(opt.File, []byte(strings.ReplaceAll(string(data), "judgeModel: gpt-test", "judgeModel: judge-test")), 0600); err != nil {
						t.Fatal(err)
					}
					judge := route
					judge.Model = "judge-test"
					opt.JudgeRoute = &judge
				}
				opt.ShowContent = true
				var logs strings.Builder
				opt.Progress = func(s string) error { logs.WriteString(s); return nil }
				// Use the actual credential-owning implementation, not a scripted Runner.
				owner := copilot.New(nil)
				runner := newLocalFileRunner(func(ctx context.Context, routes []modelaccess.Route) (modelaccess.PreviewAccess, error) {
					accessCalls++
					if offline {
						t.Error("replay established credential access")
					}
					return owner.PreviewClient(ctx, routes)
				})
				result, err := (&App{FileRunner: runner}).PreviewAgentFile(context.Background(), opt)
				if echo == "" {
					if err != nil || !result.FullPass || chats != 6 {
						t.Fatalf("safe preview: %+v / %v chats=%d", result, err, chats)
					}
				} else {
					want := 1
					if strings.HasPrefix(echo, "judge-") {
						want = 2
					}
					if err == nil || result.FullPass || chats != want {
						t.Fatalf("unsafe reply: %+v / %v chats=%d", result, err, chats)
					}
				}
				encoded, _ := json.Marshal(result)
				captured := string(encoded) + logs.String()
				if err != nil {
					captured += err.Error()
				}
				dir, _ := config.LocalPreviewJournalDir()
				ids, _ := localpreview.ListRuns(dir)
				if len(ids) != 1 {
					t.Fatal(ids)
				}
				offline = true
				beforeReplay := [4]int{catalogs, exchanges, chats, accessCalls}
				replay, replayErr := localpreview.Inspect(t.Context(), dir, ids[0])
				if replayErr != nil {
					t.Fatal(replayErr)
				}
				b, _ := json.Marshal(replay)
				captured += string(b)
				paths, _ := filepath.Glob(filepath.Join(dir, "*"))
				for _, path := range paths {
					if data, err := os.ReadFile(path); err == nil {
						captured += string(data)
					}
				}
				if strings.Contains(captured, source) || strings.Contains(captured, bearer) {
					t.Fatal("credential leaked into retained or public content")
				}
				if echo == "" && len(replay.Sessions) != 6 {
					t.Fatal("agent/judge Session inventory changed")
				}
				if exchanges != 1 || replay.ModelCalls != 0 || accessCalls != 1 || beforeReplay != ([4]int{catalogs, exchanges, chats, accessCalls}) {
					t.Fatalf("exchange/replay calls %d/%d", exchanges, replay.ModelCalls)
				}
				before := catalogs
				_, _ = localpreview.Inspect(t.Context(), dir, ids[0])
				if catalogs != before {
					t.Fatal("replay contacted provider")
				}
			})
		}
	}
}
