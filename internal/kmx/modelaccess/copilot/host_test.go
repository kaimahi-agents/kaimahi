package copilot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/microsoft/agent-host-protocol/clients/go/ahptypes"
)

// These tests catch leaked URL credentials, wrong auth resources, unchecked
// decoded strings, dropped observations, and snapshots surviving invalidation.
func hostServer(t *testing.T, notification string, reject bool) (string, *atomic.Int64) {
	t.Helper()
	calls := new(atomic.Int64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("tkn") != "connection/private+key" {
			t.Error("connection token missing")
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for {
			_, b, err := conn.Read(t.Context())
			if err != nil {
				return
			}
			var req ahptypes.JsonRpcRequest
			if json.Unmarshal(b, &req) != nil {
				t.Error("bad request")
				return
			}
			calls.Add(1)
			result := `{}`
			switch req.Method {
			case "initialize":
				result = `{"protocolVersion":"` + ahptypes.SupportedProtocolVersions()[0] + `","serverSeq":0,"snapshots":[{"resource":"ahp-root://","fromSeq":0,"state":{"agents":[{"provider":"copilot","protectedResources":[{"resource":"https://api.github.com"},{"resource":"https://other.invalid"}]}]}}]}`
			case "authenticate":
				var a ahptypes.AuthenticateParams
				json.Unmarshal(req.Params, &a)
				if a.Token != "github_private_key" || a.Resource != "https://api.github.com" {
					t.Error("wrong GitHub auth")
				}
			case "ping":
				if notification != "" {
					conn.Write(t.Context(), websocket.MessageText, []byte(notification))
				}
			default:
				t.Error("unexpected RPC")
				return
			}
			response := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":%s}`, req.ID, result)
			if reject && req.Method == "authenticate" {
				response = fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"error":{"code":-32007,"message":"private provider diagnostic"}}`, req.ID)
			}
			if conn.Write(t.Context(), websocket.MessageText, []byte(response)) != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	return "ws" + strings.TrimPrefix(server.URL, "http"), calls
}
func hostEnv(t *testing.T) {
	t.Helper()
	t.Setenv("COPILOT_GITHUB_TOKEN", "github_private_key")
	t.Setenv("COPILOTD_CLIENT_TOKEN", "connection/private+key")
}
func TestHostEndpointAndTokenBoundary(t *testing.T) {
	for _, url := range []string{"ws://localhost:1", "ws://127.0.0.1:0", "ws://127.0.0.1:65536", "ws://127.0.0.1:abc", "ws://user@127.0.0.1:1", "ws://127.0.0.1:1?x=1", "ws://127.0.0.1:1#x", "http://127.0.0.1:1", "ws://192.0.2.1:1"} {
		t.Run(url, func(t *testing.T) {
			hostEnv(t)
			a, err := New(nil).CopilotdClient(t.Context(), url, nil)
			if !errors.Is(err, ErrHostConnection) || a != nil {
				t.Fatalf("unsafe endpoint accepted: %v", err)
			}
		})
	}
	hostEnv(t)
	for _, token := range []string{"", " ", "line\nbreak"} {
		t.Setenv("COPILOTD_CLIENT_TOKEN", token)
		_, err := New(nil).CopilotdClient(t.Context(), "ws://127.0.0.1:1", nil)
		if !errors.Is(err, ErrHostToken) {
			t.Fatalf("invalid token: %v", err)
		}
	}
}
func TestHostDialFailureRetainsOpaqueChecker(t *testing.T) {
	hostEnv(t)
	a, err := New(nil).CopilotdClient(t.Context(), "ws://127.0.0.1:1", nil)
	if a == nil || !errors.Is(err, ErrHostConnection) {
		t.Fatalf("missing checker: %v", err)
	}
	for _, v := range []any{a, fmt.Sprintf("%#v", a), fmt.Sprintf("%+v", a), err} {
		if strings.Contains(fmt.Sprint(v), "private") {
			t.Fatal("secret formatted")
		}
	}
	for _, key := range []string{"github_private_key", "connection/private+key", "connection%2Fprivate%2Bkey", encoded("github_private_key")} {
		if !errors.Is(a.CheckContent(key), ErrCredentialContent) {
			t.Fatal("encoded credential unchecked")
		}
	}
	if a.Client() != nil {
		t.Fatal("failed client exposed")
	}
}
func TestHostEndpointCannotCarryKnownCredentials(t *testing.T) {
	hostEnv(t)
	endpoint, calls := hostServer(t, "", false)
	a, err := New(nil).CopilotdClient(t.Context(), endpoint+"/%67ithub_private_key", nil)
	if a != nil {
		defer a.Close(t.Context())
	}
	if !errors.Is(err, ErrCredentialContent) || calls.Load() != 0 {
		t.Fatalf("credential-bearing endpoint used: %v", err)
	}
}

func TestHostSetupAndGeneration(t *testing.T) {
	hostEnv(t)
	endpoint, calls := hostServer(t, "", false)
	p := New(nil)
	a, err := p.CopilotdClient(t.Context(), endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close(t.Context())
	if calls.Load() != 2 {
		t.Fatal("setup not initialize then authenticate")
	}
	if err := a.Client().Ping(t.Context()); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.generation++
	p.mu.Unlock()
	if !errors.Is(a.Validate(t.Context()), ErrAuthRevoked) {
		t.Fatal("generation remained valid")
	}
}
func TestHostIncomingCheckedBeforeFanout(t *testing.T) {
	action := `{"jsonrpc":"2.0","method":"action","params":{"channel":"ahp-root://","serverSeq":1,"action":{"type":"root/agentsChanged","agents":[]}}}`
	for _, tt := range []struct {
		name, frame string
		want        error
		observed    int64
	}{
		{"action", action, nil, 1},
		{"escaped", `{"jsonrpc":"2.0","id":99,"result":{"unknown":"` + encoded("github_private_key") + `"}}`, ErrCredentialContent, 0},
		{"duplicate", `{"jsonrpc":"2.0","id":99,"result":{"x":1,"x":2}}`, ErrHostConnection, 0},
		{"malformedAction", `{"jsonrpc":"2.0","method":"action","params":{}}`, ErrHostConnection, 0},
		{"wrongVersion", `{"jsonrpc":"1.0","method":"action","params":{"channel":"ahp-root://","serverSeq":1,"action":{"type":"root/agentsChanged","agents":[]}}}`, ErrHostConnection, 0},
		{"ambiguous", `{"jsonrpc":"2.0","id":99,"result":{},"error":{"code":1,"message":"no"}}`, ErrHostConnection, 0},
		{"missingID", `{"jsonrpc":"2.0","result":{}}`, ErrHostConnection, 0},
		{"observer", action, ErrHostConnection, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			hostEnv(t)
			endpoint, _ := hostServer(t, tt.frame, false)
			var observed atomic.Int64
			a, err := New(nil).CopilotdClient(t.Context(), endpoint, func(ahptypes.ActionEnvelope) error {
				observed.Add(1)
				if tt.name == "observer" {
					return errors.New("private provider diagnostic")
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close(t.Context())
			_ = a.Client().Ping(t.Context())
			if !errors.Is(a.Validate(t.Context()), tt.want) {
				t.Fatalf("validation = %v", a.Validate(t.Context()))
			}
			if observed.Load() != tt.observed {
				t.Fatal("observation lost or unchecked")
			}
		})
	}
}
func TestHostAuthRefusalAndCancellation(t *testing.T) {
	hostEnv(t)
	endpoint, _ := hostServer(t, "", true)
	a, err := New(nil).CopilotdClient(t.Context(), endpoint, nil)
	if a == nil || !errors.Is(err, ErrAuthRevoked) || strings.Contains(err.Error(), "private provider") {
		t.Fatalf("unsafe refusal: %v", err)
	}
	endpoint, _ = hostServer(t, "", false)
	ctx, cancel := context.WithCancel(t.Context())
	a, err = New(nil).CopilotdClient(ctx, endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	defer a.Close(t.Context())
	if !errors.Is(a.Validate(t.Context()), context.Canceled) {
		t.Fatal("owner cancellation ignored")
	}
}
func TestHostAbsentTokenAndExpiry(t *testing.T) {
	hostEnv(t)
	os.Unsetenv("COPILOTD_CLIENT_TOKEN")
	// A tokenless dial failure is a connection failure, not an env-token failure.
	a, err := New(nil).CopilotdClient(t.Context(), "ws://[::1]:1", nil)
	if a == nil || !errors.Is(err, ErrHostConnection) {
		t.Fatal(err)
	}
	hostEnv(t)
	endpoint, _ := hostServer(t, "", false)
	p := New(nil)
	now := time.Now()
	p.now = func() time.Time { return now }
	a, err = p.CopilotdClient(t.Context(), endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close(t.Context())
	now = now.Add(6 * time.Minute)
	if !errors.Is(a.Validate(t.Context()), ErrAuthRevoked) {
		t.Fatal("expired host access usable")
	}
}
