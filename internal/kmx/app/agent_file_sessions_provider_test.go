package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess/copilot"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

type previewProviderWire func(*http.Request) (*http.Response, error)

func (f previewProviderWire) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSessionsProviderCredentialsNeverEnterJournalLogsOrResults(t *testing.T) {
	for _, echo := range []string{"", "source", "bearer", "judge-bearer"} {
		t.Run(echo, func(t *testing.T) {
			t.Setenv("KMX_HOME", t.TempDir())
			t.Setenv("COPILOT_GITHUB_TOKEN", "opaque-source-token")
			const source = "opaque-source-token"
			const bearer = "opaque-exchanged-token"
			chats, exchanges, catalogs := 0, 0, 0
			old := http.DefaultTransport
			t.Cleanup(func() { http.DefaultTransport = old })
			http.DefaultTransport = previewProviderWire(func(req *http.Request) (*http.Response, error) {
				status, body := 200, ""
				switch req.URL.Path {
				case "/models":
					catalogs++
					if req.Header.Get("Authorization") != "Bearer "+bearer {
						status = 401
						body = `{"error":{"code":"unsupported_token"}}`
					} else {
						body = `{"data":[{"id":"gpt-test","capabilities":{"type":"chat"},"policy":{"state":"enabled"},"supported_endpoints":["/chat/completions"]}]}`
					}
				case "/copilot_internal/v2/token":
					exchanges++
					body = `{"token":"` + bearer + `","expires_at":4102444800}`
				case "/chat/completions":
					chats++
					if req.Header.Get("Authorization") != "Bearer "+bearer {
						t.Error("wrong upstream credential")
					}
					var payload directChat
					if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
						t.Error(err)
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
					b, _ := json.Marshal(map[string]any{"model": payload.Model, "choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"role": "assistant", "content": answer}}}})
					body = string(b)
				default:
					t.Fatalf("unexpected provider request %s", req.URL.Path)
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
			})
			opt := directOptions(t, "gpt-test", true)
			route := copilotPreviewRoute()
			opt.Route = &route
			opt.ShowContent = true
			var logs strings.Builder
			opt.Progress = func(s string) error { logs.WriteString(s); return nil }
			// Use the actual credential-owning implementation, not a scripted Runner.
			runner := newLocalFileRunner(copilot.New(nil).PreviewClient)
			result, err := (&App{FileRunner: runner}).PreviewAgentFile(context.Background(), opt)
			if echo == "" {
				if err != nil || !result.FullPass || chats != 6 {
					t.Fatalf("safe preview: %+v / %v chats=%d", result, err, chats)
				}
			} else {
				want := 1
				if echo == "judge-bearer" {
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
			if exchanges != 1 || replay.ModelCalls != 0 {
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
