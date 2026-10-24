package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

type modelCatalogOnlyTransport struct {
	t     *testing.T
	calls int
}

func (f *modelCatalogOnlyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	f.calls++
	if req.Method != "GET" || req.URL.String() != modelaccess.CopilotEndpoint+"/models" {
		f.t.Error("unknown model attempted inference")
		return nil, errors.New("unexpected inference")
	}
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"choice-b","capabilities":{"type":"chat"},"policy":{"state":"enabled"},"supported_endpoints":["/responses"]},{"id":"choice-a","capabilities":{"type":"chat"},"policy":{"state":"enabled"},"supported_endpoints":["/chat/completions"]}]}`)), Request: req}, nil
}
func TestFilePreviewUnknownModelListsAvailableChoices(t *testing.T) {
	for _, journaled := range []bool{false, true} {
		t.Run(fmt.Sprint(journaled), func(t *testing.T) {
			t.Setenv("COPILOT_GITHUB_TOKEN", "model-choice-test-token")
			previous := http.DefaultTransport
			transport := &modelCatalogOnlyTransport{t: t}
			http.DefaultTransport = transport
			t.Cleanup(func() { http.DefaultTransport = previous })
			path := previewFile(t)
			route := modelaccess.Route{Provider: "copilot", Endpoint: modelaccess.CopilotEndpoint, Auth: "copilot"}
			runner := NewDirectFileRunner()
			if journaled {
				runner = NewLocalFileRunner()
			}
			result, err := (&App{FileRunner: runner}).PreviewAgentFile(t.Context(), FilePreviewOptions{File: path, Operation: "test", Model: "unknown-model", Route: &route})
			if err == nil || result.ReasonCode != "copilot_model_unavailable" || result.Reason != "Available Copilot models: choice-a, choice-b" || transport.calls != 1 || result.FullPass {
				t.Fatalf("one-line choices lost: %+v / %v", result, err)
			}
		})
	}
}

func TestRuntimeModelReachesBothPreviewRunners(t *testing.T) {
	for _, direct := range []bool{false, true} {
		for _, omitted := range []bool{false, true} {
			t.Run(fmt.Sprintf("direct=%t/omitted=%t", direct, omitted), func(t *testing.T) {
				t.Setenv("KMX_HOME", t.TempDir())
				want := "picked-model"
				if omitted {
					want = "gpt-5.6-terra"
				}
				sends := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					sends++
					var body directChat
					if json.NewDecoder(r.Body).Decode(&body) != nil || body.Model != want {
						t.Error("runner did not receive effective model")
					}
					previewEnvelope(w, want, "Hello, PRIVATE INPUT")
				}))
				defer server.Close()
				opt := directOptions(t, "authored-model", false)
				opt.Model = "picked-model"
				if omitted {
					data, err := os.ReadFile(opt.File)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(opt.File, []byte(strings.Replace(string(data), "## Model\n- **Preferred:** authored-model\n\n", "", 1)), 0600); err != nil {
						t.Fatal(err)
					}
					opt.Model = ""
				}
				opt.Route = &modelaccess.Route{Provider: "compatible", Endpoint: server.URL, Auth: "none"}
				runner := NewLocalFileRunner()
				if direct {
					runner = NewDirectFileRunner()
				}
				result, err := (&App{FileRunner: runner}).PreviewAgentFile(t.Context(), opt)
				if err != nil || !result.FullPass || result.ModelRoute.Model != want || sends != 2 {
					t.Fatalf("real runner model copy: %+v / %v sends=%d", result, err, sends)
				}
			})
		}
	}
}

func TestFilePreviewRejectsInvalidRuntimeModelBeforeRunner(t *testing.T) {
	for _, model := range []string{"auto", "https://private.invalid", "gpt-*", "ghp_" + strings.Repeat("a", 36)} {
		t.Run(fmt.Sprintf("length-%d", len(model)), func(t *testing.T) {
			runner := &scriptedPreview{verdict: runtime.EvaluationPass}
			path := previewFile(t)
			route := modelaccess.Route{Provider: "copilot", Endpoint: modelaccess.CopilotEndpoint, Auth: "copilot"}
			result, err := (&App{FileRunner: runner}).PreviewAgentFile(t.Context(), FilePreviewOptions{File: path, Operation: "test", Model: model, Route: &route})
			encoded, _ := json.Marshal(result)
			if err == nil || result.ReasonCode != "invalid_options" || runner.calls != 0 || result.File.Path != "greeter.agent.md" || strings.Contains(string(encoded), model) {
				t.Fatalf("runtime option was executed, echoed, or blamed on source: code=%s calls=%d", result.ReasonCode, runner.calls)
			}
		})
	}
}

func TestFilePreviewRuntimeModelPrecedence(t *testing.T) {
	for _, tc := range []struct{ name, authored, override, want string }{
		{"override", "authored-model", "picked-model", "picked-model"},
		{"authored", "authored-model", "", "authored-model"},
		{"default", "", "", "gpt-5.6-terra"},
		{"override-without-authored", "", "picked-model", "picked-model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := previewFile(t)
			source, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			text := string(source)
			if tc.authored == "" {
				text = strings.Replace(text, "## Model\n- **Preferred:** fake\n\n", "", 1)
			} else {
				text = strings.Replace(text, "**Preferred:** fake", "**Preferred:** "+tc.authored, 1)
			}
			if err := os.WriteFile(path, []byte(text), 0600); err != nil {
				t.Fatal(err)
			}
			parsed, err := agentfile.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			originalModel, revision := parsed.Model, parsed.Revision
			runner := &scriptedPreview{verdict: runtime.EvaluationPass}
			application := &App{FileRunner: runner}
			route := modelaccess.Route{Provider: "compatible", Endpoint: "http://127.0.0.1:11434", Auth: "none"}
			result, err := application.PreviewAgentSnapshot(t.Context(), parsed, FilePreviewOptions{File: path, Operation: "test", Model: tc.override, Route: &route})
			if err != nil || !result.FullPass || result.ModelRoute.Model != tc.want {
				t.Fatalf("selection: %+v error=%v", result, err)
			}
			if runner.request.Agent == parsed || runner.request.Agent.Model != tc.want || runner.request.Route.Model != tc.want {
				t.Fatal("runner did not receive the isolated effective-model snapshot")
			}
			if parsed.Model != originalModel || parsed.Revision != revision || runner.request.Agent.Revision != revision || runner.request.Agent.Instructions != parsed.Instructions {
				t.Fatal("runtime choice mutated authored identity/instructions")
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != text {
				t.Fatal("runtime choice rewrote source")
			}
		})
	}
}
