package copilotd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess/copilot"
	"github.com/microsoft/agent-host-protocol/clients/go/ahp"
	"github.com/microsoft/agent-host-protocol/clients/go/ahptypes"
)

func wireRunner(t *testing.T, pending, cleanupFails bool) (agentpreview.Runner, *atomic.Int64, *atomic.Int64) {
	t.Helper()
	t.Setenv("COPILOT_GITHUB_TOKEN", "fixture-private-token")
	t.Setenv("COPILOTD_CLIENT_TOKEN", "fixture-connection-token")
	created, closed := new(atomic.Int64), new(atomic.Int64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, e := websocket.Accept(w, r, nil)
		if e != nil {
			return
		}
		defer conn.CloseNow()
		ctx := r.Context()
		state := ahptypes.ChatState{Resource: "ahp-chat:/a"}
		seq := int64(1)
		send := func(v any) { b, _ := json.Marshal(v); conn.Write(ctx, websocket.MessageText, b) }
		action := func(raw string) {
			var a ahptypes.StateAction
			if json.Unmarshal([]byte(raw), &a) != nil {
				panic("invalid fixture")
			}
			ahp.ApplyActionToChat(&state, a)
			send(map[string]any{"jsonrpc": "2.0", "method": "action", "params": ahptypes.ActionEnvelope{Channel: "ahp-chat:/a", ServerSeq: seq, Action: a}})
			seq++
		}
		for {
			_, b, e := conn.Read(ctx)
			if e != nil {
				return
			}
			var m struct {
				ID     *int64
				Method string
				Params json.RawMessage
			}
			json.Unmarshal(b, &m)
			if m.ID != nil {
				var result any = map[string]any{}
				switch m.Method {
				case "initialize":
					result = map[string]any{"protocolVersion": "1.0.0", "serverInfo": map[string]any{"name": "fixture", "version": "1.0.5"}, "snapshots": []any{map[string]any{"resource": "ahp-root://", "fromSeq": 0, "state": map[string]any{"agents": []any{map[string]any{"provider": "copilot", "protectedResources": []any{map[string]any{"resource": "https://api.github.com"}}}}}}}}
				case "createSession":
					created.Add(1)
				case "subscribe":
					var p struct{ Channel string }
					json.Unmarshal(m.Params, &p)
					var s any = state
					if strings.HasPrefix(p.Channel, "ahp-session:") {
						s = map[string]any{"lifecycle": "ready", "chats": []any{map[string]any{"resource": "ahp-chat:/a"}}, "customizations": []any{map[string]any{"type": "agent", "id": "reader", "name": "Reader", "uri": "copilot-agent:/reader"}}}
					}
					result = map[string]any{"snapshot": map[string]any{"resource": p.Channel, "fromSeq": seq - 1, "state": s}}
				case "disposeSession":
					if cleanupFails {
						send(map[string]any{"jsonrpc": "2.0", "id": *m.ID, "error": map[string]any{"code": -32000, "message": "cannot dispose"}})
						continue
					}
				}
				send(map[string]any{"jsonrpc": "2.0", "id": *m.ID, "result": result})
				continue
			}
			if m.Method == "dispatchAction" {
				var p struct{ Action json.RawMessage }
				json.Unmarshal(m.Params, &p)
				var a struct{ Type, TurnId string }
				json.Unmarshal(p.Action, &a)
				if a.Type != "chat/turnStarted" {
					continue
				}
				action(string(p.Action))
				action(fmt.Sprintf(`{"type":"chat/usage","turnId":%q,"usage":{"model":"model"}}`, a.TurnId))
				calls := 7
				if pending {
					calls = 1
				}
				for i := 0; i < calls; i++ {
					action(fmt.Sprintf(`{"type":"chat/toolCallStart","turnId":%q,"toolCallId":"call%d","toolName":"bash","displayName":"Bash"}`, a.TurnId, i))
					extra := `,"confirmed":"not-needed"`
					if pending {
						extra = ""
					}
					action(fmt.Sprintf(`{"type":"chat/toolCallReady","turnId":%q,"toolCallId":"call%d","invocationMessage":"Run","toolInput":"echo marker"%s}`, a.TurnId, i, extra))
					if !pending {
						action(fmt.Sprintf(`{"type":"chat/toolCallComplete","turnId":%q,"toolCallId":"call%d","result":{"success":true,"content":[]}}`, a.TurnId, i))
					}
				}
				if !pending {
					action(fmt.Sprintf(`{"type":"chat/usage","turnId":%q,"usage":{"model":"model"}}`, a.TurnId))
					action(fmt.Sprintf(`{"type":"chat/responsePart","turnId":%q,"part":{"kind":"markdown","id":"answer","content":"marker"}}`, a.TurnId))
					action(fmt.Sprintf(`{"type":"chat/turnComplete","turnId":%q,"duration":1}`, a.TurnId))
				}
			}
		}
	}))
	t.Cleanup(server.Close)
	host := "ws" + strings.TrimPrefix(server.URL, "http")
	provider := copilot.New(nil)
	timeout := time.Second
	if pending {
		timeout = 100 * time.Millisecond
	}
	return NewRunner(Options{Host: host, ExampleTimeout: timeout, Access: func(ctx context.Context, observe func(ahptypes.ActionEnvelope) error) (copilot.HostAccess, error) {
		return provider.CopilotdClient(ctx, host, observe)
	}, Snapshot: func(context.Context, func(...string) error) (agentpreview.SourceSnapshot, error) {
		return agentpreview.SourceSnapshot{Directory: "/fixture", ContentRevision: "sha256:" + strings.Repeat("a", 64), Close: func(context.Context) error { closed.Add(1); return nil }}, nil
	}}), created, closed
}
func wireRequest() agentpreview.Request {
	return agentpreview.Request{Agent: &agentfile.Agent{Title: "Reader", Revision: "revision", Tools: []string{"bash"}}, Route: modelaccess.Route{Provider: "copilot", Model: "model"}, Examples: []agentfile.Example{{ID: "first", Input: "read"}, {ID: "second", Input: "read"}}}
}
func TestRunnerWireRoundsCleanupUncertainty(t *testing.T) {
	r, created, closed := wireRunner(t, false, true)
	req := wireRequest()
	early := 0
	req.SessionCreated = func(_, uri string) error {
		early++
		if !strings.HasPrefix(uri, "ahp-session:/") {
			t.Fatal(uri)
		}
		return nil
	}
	out, err := r.Run(t.Context(), req)
	if err != nil || out.State != agentpreview.Complete || len(out.Examples) != 2 || created.Load() != 2 || closed.Load() != 2 || early != 2 || len(out.Notices) != 2 {
		t.Fatalf("out=%+v err=%v created=%d closed=%d early=%d", out, err, created.Load(), closed.Load(), early)
	}
	for _, ex := range out.Examples {
		if ex.Completion.Answer != "marker" || ex.Completion.ToolCalls != 7 || ex.Completion.Native.HostVersion != "1.0.5" {
			t.Fatalf("%+v", ex)
		}
	}
}
func TestRunnerWireChecksFormattedSession(t *testing.T) {
	r, _, _ := wireRunner(t, false, false)
	t.Setenv("COPILOTD_CLIENT_TOKEN", "Native")
	called := false
	req := wireRequest()
	req.SessionCreated = func(string, string) error { called = true; return nil }
	_, err := r.Run(t.Context(), req)
	if !errors.Is(err, copilot.ErrCredentialContent) || called {
		t.Fatalf("err=%v called=%v", err, called)
	}
}

func TestRunnerWireDeadlineStopsCases(t *testing.T) {
	r, created, closed := wireRunner(t, true, false)
	out, err := r.Run(t.Context(), wireRequest())
	if !errors.Is(err, context.DeadlineExceeded) || len(out.Examples) != 1 || created.Load() != 1 || closed.Load() != 1 {
		t.Fatalf("%+v %v %d %d", out, err, created.Load(), closed.Load())
	}
	n := out.Examples[0].Completion.Native
	if n.PendingTool != "bash" || n.PendingInput != "echo marker" {
		t.Fatalf("%+v", n)
	}
}
