package localpreview

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
)

type residentRoundTrip func(*http.Request) (*http.Response, error)

func (f residentRoundTrip) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestResidentProviderCredentialNeverCrossesToolBoundary(t *testing.T) {
	const key = "private-provider-credential"
	for _, location := range []string{"arguments", "result"} {
		t.Run(location, func(t *testing.T) {
			definition := api.ToolDefinition{Name: "check", Description: "Check.", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}, "additionalProperties": false}}
			executions, sends := 0, 0
			host, err := localtools.NewTrustedHost([]api.ToolDefinition{definition}, func(_ context.Context, _ controller.ToolCallContext, call api.ToolCall) (api.ToolResult, error) {
				executions++
				return api.ToolResult{ID: call.ID, Output: map[string]any{"value": key}}, nil
			}, func(...string) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			check := func(values ...string) error {
				for _, v := range values {
					if strings.Contains(v, key) {
						return errors.New("credential withheld")
					}
				}
				return nil
			}
			transport := residentRoundTrip(func(req *http.Request) (*http.Response, error) {
				sends++
				args := `{}`
				if location == "arguments" {
					raw, _ := json.Marshal(map[string]string{"text": key})
					args = string(raw)
				}
				body, _ := json.Marshal(map[string]any{"model": "safe-model", "choices": []any{map[string]any{"finish_reason": "tool_calls", "message": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "call", "type": "function", "function": map[string]any{"name": "check", "arguments": args}}}}}}})
				return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body)), Header: http.Header{}, Request: req}, nil
			})
			answer, err := SendToolConversation(t.Context(), "copilot", "safe-model", "https://model.example", transport, "Use the tool.", nil, "go", "session", strings.Repeat("a", 24), host, check)
			want := 0
			if location == "result" {
				want = 1
			}
			if err == nil || answer != "" || strings.Contains(err.Error(), key) || executions != want || sends != 1 {
				t.Fatalf("exec=%d sends=%d answer=%q err=%v", executions, sends, answer, err)
			}
		})
	}
}

func TestResidentToolConversationComposesBrokerContentCheck(t *testing.T) {
	definition := api.ToolDefinition{Name: "check", Description: "Check.", InputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}}
	host, err := localtools.NewTrustedHost([]api.ToolDefinition{definition}, func(context.Context, controller.ToolCallContext, api.ToolCall) (api.ToolResult, error) {
		return api.ToolResult{}, errors.New("unexpected tool call")
	}, func(values ...string) error {
		for _, value := range values {
			if secretshapes.Match(value) != nil {
				return errors.New("unsafe content")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	secret := "ghp_" + strings.Repeat("a", 36)
	transport := residentRoundTrip(func(request *http.Request) (*http.Response, error) {
		body := `{"model":"safe-model","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"` + secret + `"}}]}`
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewBufferString(body)), Request: request}, nil
	})
	answer, err := SendToolConversation(t.Context(), "compatible", "safe-model", "https://model.example", transport, "Answer safely.", nil, "hello", "session", strings.Repeat("a", 24), host, nil)
	if err == nil || answer != "" || strings.Contains(err.Error(), secret) {
		t.Fatalf("credential-shaped response escaped: answer=%q err=%v", answer, err)
	}
}
