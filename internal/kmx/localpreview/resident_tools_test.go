package localpreview

import (
	"bytes"
	"context"
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
