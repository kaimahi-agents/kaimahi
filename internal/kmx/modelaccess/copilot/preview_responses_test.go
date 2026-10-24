package copilot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func previewProtocolCatalog(paths ...string) string {
	b, _ := json.Marshal(map[string]any{"data": []any{map[string]any{"id": "gpt-test", "capabilities": map[string]string{"type": "chat"}, "policy": map[string]string{"state": "enabled"}, "supported_endpoints": paths}}})
	return string(b)
}
func previewResponsesText(parts ...string) string {
	content := []any{}
	for _, part := range parts {
		content = append(content, map[string]string{"type": "output_text", "text": part})
	}
	b, _ := json.Marshal(map[string]any{"id": "reply", "model": "gpt-test", "status": "completed", "output": []any{map[string]any{"type": "message", "role": "assistant", "content": content}}})
	return string(b)
}

func TestPreviewResponsesPinsAdvertisedProtocol(t *testing.T) {
	for _, initial := range []string{"/chat/completions", "/responses"} {
		t.Run(initial, func(t *testing.T) {
			var phase, sends, gh atomic.Int32
			p, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer "+canary {
					t.Error("credential snapshot changed")
				}
				if r.URL.Path == "/models" {
					paths := []string{initial}
					if phase.Load() == 1 {
						paths = []string{"/chat/completions", "/responses"}
					}
					if phase.Load() == 2 {
						if initial == "/responses" {
							paths = []string{"/chat/completions"}
						} else {
							paths = []string{"/responses"}
						}
					}
					io.WriteString(w, previewProtocolCatalog(paths...))
					return
				}
				sends.Add(1)
				if r.URL.Path != initial {
					t.Error("protocol changed or fell back")
				}
				if r.Header.Get("Idempotency-Key") != "" || r.Header.Get("X-Idempotency-Key") != "" || r.Header.Get("Proxy-Authorization") != "" {
					t.Error("replay/proxy header forwarded")
				}
				var body map[string]json.RawMessage
				if json.NewDecoder(r.Body).Decode(&body) != nil {
					t.Error("invalid request")
				}
				if string(body["stream"]) != "false" || string(body["model"]) != `"gpt-test"` {
					t.Error("stream or model changed")
				}
				if initial == "/responses" {
					if body["messages"] != nil || body["max_completion_tokens"] != nil || string(body["max_output_tokens"]) != "1024" || string(body["store"]) != "false" {
						t.Error("Responses request not normalized")
					}
					var messages []map[string]string
					if json.Unmarshal(body["input"], &messages) != nil || len(messages) != 1 || messages[0]["content"] != "hello" {
						t.Error("messages lost in translation")
					}
					io.WriteString(w, previewResponsesText("hel", "lo"))
				} else {
					io.WriteString(w, `{"model":"gpt-test","choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`)
				}
				w.Header().Set("X-Private", canary)
			})
			upstream := p.http.Transport
			p.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.Method == http.MethodPost {
					if req.GetBody != nil {
						t.Error("inference is replayable")
					}
					if initial == "/responses" && (req.Header.Get("Accept-Encoding") != "" || req.Header.Get("Content-Encoding") != "") {
						t.Error("caller encoding headers retained")
					}
				}
				return upstream.RoundTrip(req)
			})
			p.runStoredGH = func(context.Context) ([]byte, error) { gh.Add(1); return []byte(canary), nil }
			a := previewAccess(t, p)
			rt, err := a.Client(selected())
			if err != nil {
				t.Fatal(err)
			}
			for n := 0; n < 2; n++ {
				phase.Store(int32(n))
				req := chatRequest(t, strings.TrimSuffix(previewBody, "}")+`,"max_completion_tokens":1024}`)
				req.Header.Set("Idempotency-Key", "do-not-replay")
				req.Header.Set("X-Idempotency-Key", "do-not-replay")
				req.Header.Set("Proxy-Authorization", "do-not-forward")
				if initial == "/responses" {
					req.Header.Set("Accept-Encoding", "gzip")
					req.Header.Set("Content-Encoding", "gzip")
				}
				resp, err := rt.RoundTrip(req)
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(resp.Body)
				resp.Body.Close()
				var reply struct {
					Model   string
					Choices []struct{ Message struct{ Content string } }
				}
				if err != nil || json.Unmarshal(body, &reply) != nil || reply.Model != "gpt-test" || len(reply.Choices) != 1 || reply.Choices[0].Message.Content != "hello" {
					t.Fatal("translated completion changed")
				}
				if resp.Header.Get("X-Private") != "" || resp.Request.Header.Get("Authorization") != "" || resp.Request.URL.String() != endpoint+"/chat/completions" {
					t.Fatal("upstream metadata escaped")
				}
			}
			phase.Store(2)
			if _, err := rt.RoundTrip(chatRequest(t, previewBody)); !errors.Is(err, ErrModelUnavailable) {
				t.Fatalf("missing pinned protocol: %v", err)
			}
			phase.Store(0)
			if _, err := rt.RoundTrip(chatRequest(t, previewBody)); !errors.Is(err, ErrModelUnavailable) || sends.Load() != 2 || gh.Load() != 1 {
				t.Fatalf("snapshot resumed/re-resolved: sends=%d gh=%d err=%v", sends.Load(), gh.Load(), err)
			}
		})
	}
}

func TestPreviewResponsesChecksDiscardedAndJoinedContent(t *testing.T) {
	for _, kind := range []string{"metadata", "escaped-key", "duplicate", "joined-text", "joined-refusal", "discarded-request"} {
		for _, secret := range []string{canary, previewBearer} {
			t.Run(fmt.Sprintf("%s/exchanged=%t", kind, secret == previewBearer), func(t *testing.T) {
				var sends, exchanges atomic.Int32
				response := previewResponsesText("hello")
				switch kind {
				case "metadata":
					response = strings.TrimSuffix(response, "}") + `,"metadata":{"private":"` + secret + `"}}`
				case "escaped-key":
					response = strings.TrimSuffix(response, "}") + `,"metadata":{"` + encoded(secret) + `":"ignored"}}`
				case "duplicate":
					response = strings.TrimSuffix(response, "}") + `,"metadata":{"text":"` + encoded(secret) + `","text":"safe"}}`
				case "joined-text":
					response = previewResponsesText(secret[:len(secret)/2], secret[len(secret)/2:])
				case "joined-refusal":
					response = `{"model":"gpt-test","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"refusal","refusal":"` + secret[:len(secret)/2] + `"},{"type":"refusal","refusal":"` + secret[len(secret)/2:] + `"}]}]}`
				}
				p, s := fixture(t, func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/models":
						if r.Header.Get("Authorization") == "Bearer "+canary {
							w.WriteHeader(401)
							io.WriteString(w, `{}`)
						} else {
							io.WriteString(w, previewProtocolCatalog("/responses"))
						}
					case "/exchange":
						exchanges.Add(1)
						fmt.Fprintf(w, `{"token":%q,"expires_at":%d}`, previewBearer, time.Now().Add(10*time.Minute).Unix())
					case "/responses":
						sends.Add(1)
						io.WriteString(w, response)
					default:
						t.Error("unexpected endpoint")
					}
				})
				s.token = canary
				a := previewAccess(t, p)
				body := previewBody
				if kind == "discarded-request" {
					body = strings.TrimSuffix(body, "}") + `,"max_tokens":"` + encoded(secret) + `","max_completion_tokens":1024}`
				}
				resp, err := previewSend(t, a, body)
				if !errors.Is(err, ErrCredentialContent) || resp != nil {
					t.Fatalf("unchecked %s escaped: %v", kind, err)
				}
				requirePrivate(t, err)
				want := int32(1)
				if kind == "discarded-request" {
					want = 0
				}
				if _, err := previewSend(t, a, previewBody); !errors.Is(err, ErrCredentialContent) || sends.Load() != want || exchanges.Load() != 1 || s.reads != 1 {
					t.Fatalf("refusal not latched: %v sends=%d", err, sends.Load())
				}
			})
		}
	}
}

func TestPreviewResponsesChecksIntroducedKeysAndSize(t *testing.T) {
	for _, oversized := range []bool{false, true} {
		t.Run(fmt.Sprint(oversized), func(t *testing.T) {
			sends := 0
			p, s := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/models" {
					io.WriteString(w, previewProtocolCatalog("/responses"))
					return
				}
				sends++
				io.WriteString(w, strings.TrimSuffix(previewResponsesText("hello"), "}")+`,"metadata":"`+strings.Repeat("x", responseLimit)+`"}`)
			})
			s.token = "store"
			if oversized {
				s.token = canary
			}
			a := previewAccess(t, p)
			_, err := previewSend(t, a, previewBody)
			want, wantSends := ErrCredentialContent, 0
			if oversized {
				want, wantSends = ErrMalformed, 1
			}
			if !errors.Is(err, want) {
				t.Fatalf("wrong bounded refusal: %v", err)
			}
			if _, err = previewSend(t, a, previewBody); !errors.Is(err, want) || sends != wantSends {
				t.Fatal("failed request resumed")
			}
		})
	}
}

func TestPreviewResponsesReplayDoesNotResolveCredentials(t *testing.T) {
	var gh, catalogs, sends, factories atomic.Int32
	offline := false
	p, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if offline {
			t.Error("replay reached provider")
		}
		if r.URL.Path == "/models" {
			catalogs.Add(1)
			io.WriteString(w, previewProtocolCatalog("/responses"))
			return
		}
		sends.Add(1)
		io.WriteString(w, previewResponsesText("hello"))
	})
	p.runStoredGH = func(context.Context) ([]byte, error) {
		gh.Add(1)
		if offline {
			t.Error("replay resolved gh")
		}
		return []byte(canary), nil
	}
	dir := filepath.Join(t.TempDir(), "local-preview")
	runner := localpreview.NewRunner(localpreview.RunnerOptions{JournalDir: func() (string, error) { return dir, nil }, Access: func(ctx context.Context, routes []modelaccess.Route) (modelaccess.PreviewAccess, error) {
		factories.Add(1)
		if offline {
			t.Error("replay established access")
		}
		return p.PreviewClient(ctx, routes)
	}})
	agent := &agentfile.Agent{Identity: agentfile.Identity{ID: "replay", Purpose: "Reply"}, Revision: "sha256:" + strings.Repeat("a", 64), Model: "gpt-test", Instructions: "Say hello", Examples: []agentfile.Example{{ID: "hello", Input: "hi", Assertions: []runtime.EvaluationAssertion{{ID: "text", Type: "contains", Value: "hello"}}}}}
	out, err := runner.Run(t.Context(), agentpreview.Request{Agent: agent, Examples: agent.Examples, Route: selected()})
	if err != nil || out.State != agentpreview.Complete {
		t.Fatalf("preview: %v", err)
	}
	offline = true
	before := [4]int32{gh.Load(), catalogs.Load(), sends.Load(), factories.Load()}
	replay, err := localpreview.Inspect(t.Context(), dir, out.Journal)
	if err != nil || len(replay.Sessions) != 1 || replay.Sessions[0].Answer != "hello" || replay.ModelCalls != 0 || before != ([4]int32{gh.Load(), catalogs.Load(), sends.Load(), factories.Load()}) {
		t.Fatalf("not offline replay: %v", err)
	}
	if before[0] != 1 || before[2] != 1 || before[3] != 1 {
		t.Fatalf("setup/inference counts: %v", before)
	}
}
