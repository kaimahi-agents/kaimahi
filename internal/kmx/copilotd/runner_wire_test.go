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
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
	"github.com/microsoft/agent-host-protocol/clients/go/ahp"
	"github.com/microsoft/agent-host-protocol/clients/go/ahptypes"
)

type wireFixture struct {
	Customizations  string
	ActualModel     string
	DelayDiscovery  bool
	Lifecycle       string
	Sends, Disposed atomic.Int64
}

func wireRunner(t *testing.T, pending, cleanupFails bool, fixtures ...*wireFixture) (agentpreview.Runner, *atomic.Int64, *atomic.Int64) {
	t.Helper()
	t.Setenv("COPILOT_GITHUB_TOKEN", "fixture-private-token")
	t.Setenv("COPILOTD_CLIENT_TOKEN", "fixture-connection-token")
	created, closed := new(atomic.Int64), new(atomic.Int64)
	fixture := &wireFixture{}
	if len(fixtures) > 0 {
		fixture = fixtures[0]
	}
	customizations := json.RawMessage(`[{"type":"agent","name":"Reader","uri":"copilot-agent:/reader","tools":["bash"]}]`)
	if fixture.Customizations != "" {
		customizations = json.RawMessage(fixture.Customizations)
	}
	lifecycle := fixture.Lifecycle
	if lifecycle == "" {
		lifecycle = "ready"
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, e := websocket.Accept(w, r, nil)
		if e != nil {
			return
		}
		defer conn.CloseNow()
		ctx := r.Context()
		state := ahptypes.ChatState{Resource: "ahp-chat:/a"}
		sessionReads := 0
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
						sessionReads++
						ready := map[string]any{"lifecycle": lifecycle, "chats": []any{map[string]any{"resource": "ahp-chat:/a"}}}
						if !fixture.DelayDiscovery || sessionReads > 1 {
							ready["customizations"] = customizations
						}
						s = ready
					}
					result = map[string]any{"snapshot": map[string]any{"resource": p.Channel, "fromSeq": seq - 1, "state": s}}
				case "disposeSession":
					fixture.Disposed.Add(1)
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
				fixture.Sends.Add(1)
				action(string(p.Action))
				actual := fixture.ActualModel
				if actual == "" {
					actual = "model"
				}
				action(fmt.Sprintf(`{"type":"chat/usage","turnId":%q,"usage":{"model":%q}}`, a.TurnId, actual))
				if fixture.ActualModel != "" {
					continue
				}
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
func TestRunnerWireModelMismatchReason(t *testing.T) {
	r, created, closed := wireRunner(t, false, false, &wireFixture{ActualModel: "actual-wrong"})
	out, err := r.Run(t.Context(), wireRequest())
	if !errors.Is(err, agentpreview.ErrModelUnavailable) || len(out.Examples) != 1 || created.Load() != 1 || closed.Load() != 1 {
		t.Fatalf("err=%v out=%+v", err, out)
	}
	c := out.Examples[0].Completion
	if c.Model != "model" || c.State == agentpreview.Complete || len(c.Native.ActualModels) != 1 || c.Native.ActualModels[0] != "actual-wrong" {
		t.Fatalf("%+v", c)
	}
}

func TestRunnerWireDelayedDiscovery(t *testing.T) {
	r, _, _ := wireRunner(t, false, false, &wireFixture{DelayDiscovery: true})
	if _, err := r.Run(t.Context(), wireRequest()); err != nil {
		t.Fatal(err)
	}
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
		if ex.Completion.Answer != "marker" || ex.Completion.ToolCalls != 7 || ex.Completion.Native.HostVersion != "1.0.5" || ex.Completion.Native.Selection != "name" {
			t.Fatalf("%+v", ex)
		}
	}
}
func TestRunnerWireSessionIsPublicIdentity(t *testing.T) {
	r, _, _ := wireRunner(t, false, false)
	req := wireRequest()
	req.SessionCreated = func(_, uri string) error {
		if secretshapes.Match(uri) != nil {
			return errors.New("credential-shaped session identity")
		}
		return nil
	}
	if _, err := r.Run(t.Context(), req); err != nil {
		t.Fatal(err)
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

func TestRunnerWireSelectionRefusalStopsCases(t *testing.T) {
	for _, tc := range []struct {
		name, customizations string
		want                 error
	}{
		{"missing", `[]`, ErrAgentNotDiscovered},
		{"tools mismatch", `[{"type":"agent","name":"Reader","uri":"copilot-agent:/reader","tools":["read"]}]`, ErrAgentMetadataMismatch},
		{"missing metadata", `[{"type":"agent","name":"Reader","uri":"copilot-agent:/reader"}]`, ErrAgentMetadataMismatch},
		{"duplicate", `[{"type":"agent","name":"Reader","uri":"copilot-agent:/reader","tools":["bash"]},{"type":"agent","name":"Reader","uri":"copilot-agent:/reader","tools":["other"]}]`, ErrAgentAmbiguous},
		{"disabled", `[{"type":"agent","name":"Reader","uri":"copilot-agent:/reader","tools":["bash"],"enabled":false}]`, ErrAgentNotDiscovered},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := &wireFixture{Customizations: tc.customizations}
			r, created, closed := wireRunner(t, false, false, fixture)
			out, err := r.Run(t.Context(), wireRequest())
			if !errors.Is(err, tc.want) || len(out.Examples) != 1 || created.Load() != 1 || closed.Load() != 1 || fixture.Sends.Load() != 0 || fixture.Disposed.Load() != 1 {
				t.Fatalf("err=%v examples=%d created=%d closed=%d sends=%d disposed=%d", err, len(out.Examples), created.Load(), closed.Load(), fixture.Sends.Load(), fixture.Disposed.Load())
			}
		})
	}
}

func TestRunnerWireSourceAgentAuthoredModel(t *testing.T) {
	fixture := &wireFixture{Customizations: `[{"type":"agent","name":"Reader","uri":"copilot-agent:/reader","model":"authored-model","description":"Read files","tools":["bash"]}]`}
	r, _, _ := wireRunner(t, false, false, fixture)
	req := wireRequest()
	source := *req.Agent
	source.Model = "authored-model"
	source.Identity.Purpose = "Read files"
	req.SourceAgent = &source
	req.Agent.Model = "model"
	out, err := r.Run(t.Context(), req)
	if err != nil || out.State != agentpreview.Complete || fixture.Sends.Load() != 2 {
		t.Fatalf("state=%s err=%v sends=%d", out.State, err, fixture.Sends.Load())
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
