package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

type foundryTestCredential struct{ calls int }

func (c *foundryTestCredential) GetToken(context.Context, policy.TokenRequestOptions) (azcore.AccessToken, error) {
	c.calls++
	return azcore.AccessToken{Token: "test-private-token", ExpiresOn: time.Now().Add(time.Hour)}, nil
}

type foundryRoundTrip func(*http.Request) (*http.Response, error)

func (f foundryRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestFoundryCredentialReuseAndNativeTools(t *testing.T) {
	credential := &foundryTestCredential{}
	c := &foundryChatClient{config: foundryChatConfig{Endpoint: "https://example.openai.azure.com", Deployment: "deployment-name"}, credential: credential}
	c.http = &http.Client{Transport: foundryRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/openai/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer test-private-token" {
			t.Fatal("wrong endpoint/auth")
		}
		raw, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(raw), `"model":"deployment-name"`) || !strings.Contains(string(raw), `"type":"function"`) {
			t.Fatalf("invalid request: %s", raw)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","content":null,"tool_calls":[{"id":"call-one","type":"function","function":{"name":"read","arguments":"{}"}}]}}]}`))}, nil
	})}
	for i := 0; i < 2; i++ {
		message, err := c.complete(t.Context(), []foundryMessage{{Role: "user", Content: "hello"}}, []copilotTool{{Name: "read", Parameters: map[string]any{"type": "object"}}})
		if err != nil || len(message.ToolCalls) != 1 {
			t.Fatalf("reply=%v err=%v", message, err)
		}
	}
	if credential.calls != 1 {
		t.Fatal("token acquired on each model request")
	}
}

func TestFoundryTurnRejectsCredentialMaterial(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		want    string
	}{
		{name: "raw token", content: "Your token is test-private-token"},
		{name: "ANSI inside token", content: "Your token is test-\x1b[31mprivate\x1b[0m-token"},
		{name: "control character inside token", content: "Your token is test-private-\x00token"},
		{name: "safe answer", content: "A \x1b[32msafe\x1b[0m answer", want: "A safe answer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			content, err := json.Marshal(tc.content)
			if err != nil {
				t.Fatal(err)
			}
			client := &foundryChatClient{config: foundryChatConfig{Endpoint: "https://example.openai.azure.com", Deployment: "chat"}, credential: &foundryTestCredential{}}
			client.http = &http.Client{Transport: foundryRoundTrip(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":` + string(content) + `}}]}`))}, nil
			})}
			b := &orkaChatBackend{app: &App{foundryClient: client}}
			answer, err := b.foundryTurn(t.Context(), "prompt", "question", nil, newChatRenderer(io.Discard))
			if tc.want != "" {
				if err != nil || answer != tc.want {
					t.Fatalf("answer=%q err=%v", answer, err)
				}
				return
			}
			if answer != "" || err == nil || err.Error() != "refusing credential material in model response" {
				t.Fatalf("credential response was not safely rejected: answer=%q err=%v", answer, err)
			}
		})
	}
}

func TestFoundryConfigRejectsTokenRedirectDestinations(t *testing.T) {
	for _, endpoint := range []string{"http://example.openai.azure.com", "https://example.com", "https://user:pass@example.openai.azure.com", "https://example.openai.azure.com/other", "https://example.openai.azure.com?key=secret", "https://example.openai.azure.com:443"} {
		if (foundryChatConfig{Endpoint: endpoint, Deployment: "chat"}).validate() == nil {
			t.Fatal(endpoint)
		}
	}
	for _, endpoint := range []string{"https://example.openai.azure.com", "https://example.services.ai.azure.com/openai/v1/"} {
		if err := (foundryChatConfig{Endpoint: endpoint, Deployment: "chat"}).validate(); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("KMX_HOME", t.TempDir())
	want := foundryChatConfig{Endpoint: "https://example.openai.azure.com", Deployment: "chat"}
	if err := saveFoundryChatConfig(want); err != nil {
		t.Fatal(err)
	}
	got, err := loadFoundryChatConfig()
	if err != nil || got != want {
		t.Fatalf("saved=%v err=%v", got, err)
	}
}

func TestFoundryHTTPFailureDoesNotReplay(t *testing.T) {
	calls := 0
	c := &foundryChatClient{config: foundryChatConfig{Endpoint: "https://example.openai.azure.com", Deployment: "chat"}, credential: &foundryTestCredential{}}
	c.http = &http.Client{Transport: foundryRoundTrip(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 429, Body: io.NopCloser(strings.NewReader("private details"))}, nil
	})}
	_, err := c.complete(t.Context(), nil, nil)
	if err == nil || strings.Contains(err.Error(), "private details") || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestFoundryTurnRejectsUnregisteredAndMalformedToolCalls(t *testing.T) {
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("urn:test:tool", map[string]any{"type": "object", "required": []any{"resource"}, "properties": map[string]any{"resource": map[string]any{"type": "string"}}}); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("urn:test:tool")
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range []string{`{"id":"one","type":"function","function":{"name":"unregistered","arguments":"{}"}}`, `{"id":"one","type":"function","function":{"name":"read","arguments":"{}"}}`} {
		client := &foundryChatClient{config: foundryChatConfig{Endpoint: "https://example.openai.azure.com", Deployment: "chat"}, credential: &foundryTestCredential{}}
		client.http = &http.Client{Transport: foundryRoundTrip(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[` + call + `]}}]}`))}, nil
		})}
		b := &orkaChatBackend{app: &App{foundryClient: client}}
		if _, err := b.foundryTurn(t.Context(), "prompt", "question", []copilotTool{{Name: "read", schema: schema}}, newChatRenderer(io.Discard)); err == nil {
			t.Fatal("invalid tool executed")
		}
	}
}
