package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/fileaccess"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Record on the wire, not in the SDK's asynchronously dispatched handler:
// the final response may resolve CallTool before that handler runs.
type progressTransport struct {
	sdk.Transport
	notifications chan *sdk.ProgressNotificationParams
}

func (p progressTransport) Connect(ctx context.Context) (sdk.Connection, error) {
	connection, err := p.Transport.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return progressConnection{connection, p.notifications}, nil
}

type progressConnection struct {
	sdk.Connection
	notifications chan *sdk.ProgressNotificationParams
}

func (p progressConnection) Read(ctx context.Context) (jsonrpc.Message, error) {
	message, err := p.Connection.Read(ctx)
	if request, ok := message.(*jsonrpc.Request); ok && request.Method == "notifications/progress" {
		var params sdk.ProgressNotificationParams
		if decodeErr := json.Unmarshal(request.Params, &params); decodeErr != nil {
			return nil, decodeErr
		}
		p.notifications <- &params
	}
	return message, err
}

func connectProgressFixture(t *testing.T, ops map[string]Operation, setup ...func(*sdk.Server)) (*sdk.ClientSession, <-chan *sdk.ProgressNotificationParams) {
	t.Helper()
	s, err := NewServer("test-engine", ops)
	if err != nil {
		t.Fatal(err)
	}
	for _, configure := range setup {
		configure(s)
	}
	a, b := sdk.NewInMemoryTransports()
	ss, err := s.Connect(t.Context(), a, nil)
	if err != nil {
		t.Fatal(err)
	}
	notifications := make(chan *sdk.ProgressNotificationParams, 20)
	c := sdk.NewClient(&sdk.Implementation{Name: "fixture", Version: "1"}, nil)
	cs, err := c.Connect(t.Context(), progressTransport{b, notifications}, nil)
	if err != nil {
		ss.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close(); ss.Close() })
	return cs, notifications
}

func TestPreviewCaseProgressPreservesFailureRetention(t *testing.T) {
	t.Setenv("KMX_HOME", previewStateHome(t))
	files, _, source := canonicalFiles(t)
	source = strings.Replace(source, "value: Hello, Ada", "value: absent", 1)
	saved, err := files.Create(t.Context(), "greeter.agent.md", []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	cs, notifications := connectProgressFixture(t, Operations(files, &app.App{FileRunner: app.NewLocalFileRunner()}, nil))
	for _, progress := range []bool{false, true} {
		params := &sdk.CallToolParams{Name: "test", Arguments: map[string]any{"schemaVersion": 1, "path": saved.Path}}
		if progress {
			params.SetProgressToken("retained")
		}
		result, err := cs.CallTool(t.Context(), params)
		if err != nil {
			t.Fatal(err)
		}
		var preview app.FilePreviewResult
		encoded, _ := json.Marshal(result.StructuredContent)
		if err := json.Unmarshal(encoded, &preview); err != nil {
			t.Fatal(err)
		}
		if result.IsError || preview.ReasonCode != "assertion_failed" || preview.Retention.Status != "retained" || len(preview.Retention.Diagnostics) != 1 || preview.Retention.Diagnostics[0].CaseID != "greeting" || !strings.HasPrefix(preview.Retention.Diagnostics[0].ID, "f-") {
			t.Fatalf("progress changed retention: %+v", preview)
		}
		if progress {
			select {
			case n := <-notifications:
				if n.Progress != 1 || n.Message != `{"caseId":"greeting","verdict":"fail","reason":"Assertion not satisfied.","failedAssertionId":"greeting-text"}` {
					t.Fatalf("retained case progress: %+v", n)
				}
			default:
				t.Fatal("missing retained-case progress")
			}
		}
		select {
		case n := <-notifications:
			t.Fatalf("unexpected retained-case progress: %+v", n)
		default:
		}
	}
}

func TestPreviewCaseProgressProtocol(t *testing.T) {
	home := previewStateHome(t)
	t.Setenv("KMX_HOME", home)
	// Make failure retention unavailable so per-run opaque diagnostic IDs do
	// not obscure byte parity. Journal storage and case execution remain real.
	directory, err := fileaccess.EnsurePrivateDirectory(t.Context(), home)
	if err != nil {
		t.Fatal(err)
	}
	if err := directory.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "eval-failures"), []byte("unavailable"), 0600); err != nil {
		t.Fatal(err)
	}
	files, _, source := canonicalFiles(t)
	source = strings.Split(source, "## Examples")[0] + `## Examples
` + "```yaml\n" + `examples:
  - id: first
    input: Ada
    assertions:
      - id: text
        type: contains
        value: Hello
  - id: second
    input: Grace
    assertions:
      - id: missing
        type: contains
        value: absent
      - id: text
        type: contains
        value: Hello
` + "```\n"
	saved, err := files.Create(t.Context(), "greeter.agent.md", []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	cs, notifications := connectProgressFixture(t, Operations(files, &app.App{FileRunner: app.NewLocalFileRunner()}, nil))
	args := map[string]any{"schemaVersion": 1, "path": saved.Path, "showContent": true}
	baseline, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "test", Arguments: args})
	if err != nil || baseline.IsError {
		t.Fatalf("baseline: %+v %v", baseline, err)
	}
	baselineBytes, _ := json.Marshal(baseline)
	for _, token := range []any{"cases", 0, ""} {
		params := &sdk.CallToolParams{Name: "test", Arguments: args}
		params.SetProgressToken(token)
		result, err := cs.CallTool(t.Context(), params)
		if err != nil {
			t.Fatal(err)
		}
		resultBytes, _ := json.Marshal(result)
		if !bytes.Equal(resultBytes, baselineBytes) {
			t.Fatalf("progress changed result bytes:\n%s\n%s", baselineBytes, resultBytes)
		}
		for i, want := range []string{`{"caseId":"first","verdict":"pass","reason":""}`, `{"caseId":"second","verdict":"fail","reason":"Assertion not satisfied.","failedAssertionId":"missing"}`} {
			select {
			case n := <-notifications:
				gotToken, _ := json.Marshal(n.ProgressToken)
				wantToken, _ := json.Marshal(token)
				if !bytes.Equal(gotToken, wantToken) || n.Progress != float64(i+1) || n.Message != want {
					t.Fatalf("notification %d: %+v; want %s", i, n, want)
				}
			default:
				t.Fatalf("missing case %d progress before final result", i+1)
			}
		}
	}
	for _, op := range []string{"test", "run"} {
		params := &sdk.CallToolParams{Name: op, Arguments: args}
		if op == "run" {
			params.SetProgressToken("run-must-not-notify")
		}
		if _, err := cs.CallTool(t.Context(), params); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case n := <-notifications:
		t.Fatalf("unexpected progress: %+v", n)
	default:
	}
}
