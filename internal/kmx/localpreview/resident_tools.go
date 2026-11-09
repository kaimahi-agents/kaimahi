package localpreview

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/version"
)

// SendToolConversation sends one resident turn with at most one bounded batch
// of controller-mediated tool calls. The host owns all execution authority;
// only its checked definitions, calls and results enter model requests.
func SendToolConversation(ctx context.Context, provider, model, baseURL string, transport http.RoundTripper, instructions string, history []ConversationMessage, input, sessionUID, invocationID string, host *localtools.Host, check func(...string) error) (string, error) {
	if host == nil || sessionUID == "" || invocationID == "" {
		return "", errRunnerExecution
	}
	if provider == "fake" && model == FakeModel && transport == nil {
		baseURL, transport = fakeBaseURL, fakeTransport{}
	}
	definitions := host.Definitions()
	if len(definitions) == 0 || host.Check(instructions, input, sessionUID, invocationID) != nil {
		return "", errRunnerExecution
	}
	messages := []api.Message{*api.TextMessage("system", instructions)}
	for i, message := range history {
		want := "user"
		if i%2 == 1 {
			want = "assistant"
		}
		if message.Role != want || host.Check(message.Content) != nil {
			return "", errRunnerExecution
		}
		messages = append(messages, *api.TextMessage(message.Role, message.Content))
	}
	messages = append(messages, *api.TextMessage("user", input))
	client := newModelClient(route{model: model, baseURL: strings.TrimSuffix(baseURL, "/"), transport: transport, safeErrors: provider != "fake"})
	client.check = check
	reply, err := client.residentToolModel(ctx, api.ModelRequest{Model: model, Messages: messages, Tools: definitions})
	if err != nil {
		return "", err
	}
	if text, ok := assistantText(reply.Message); ok {
		return text, nil
	}
	if reply.Message.Role != "assistant" || len(reply.Message.Parts) == 0 || len(reply.Message.Parts) > localtools.MaxCalls {
		return "", errRunnerExecution
	}
	messages = append(messages, reply.Message)
	for _, part := range reply.Message.Parts {
		if part.ToolCall == nil {
			return "", errRunnerExecution
		}
		call := *part.ToolCall
		call.Mediation = api.MediationControllerMediated
		call.IdempotencyKey = invocationID + ":" + call.ID
		result, executeErr := host.Execute(ctx, controller.ToolCallContext{SessionUID: sessionUID}, call)
		if executeErr != nil {
			return "", executeErr
		}
		messages = append(messages, api.Message{Role: "tool", Parts: []api.Part{{ToolResult: &result}}})
	}
	reply, err = client.residentToolModel(ctx, api.ModelRequest{Model: model, Messages: messages, Tools: definitions})
	if err != nil {
		return "", err
	}
	text, ok := assistantText(reply.Message)
	if !ok {
		return "", errRunnerExecution
	}
	return text, nil
}

func assistantText(message api.Message) (string, bool) {
	if message.Role != "assistant" || len(message.Parts) != 1 || message.Parts[0].Text == nil {
		return "", false
	}
	return message.Parts[0].Text.Text, true
}

func residentWirePayloadPaths(messages, callAt int) [][]string {
	if callAt < 0 {
		return nil
	}
	paths := [][]string{{"messages", strconv.Itoa(callAt), "tool_calls", "*", "function", "arguments"}}
	for i := callAt + 1; i < messages; i++ {
		paths = append(paths, []string{"messages", strconv.Itoa(i), "content"})
	}
	return paths
}

func (c *modelClient) residentToolModel(ctx context.Context, request api.ModelRequest) (api.ModelResponse, error) {
	fail := func() (api.ModelResponse, error) { return api.ModelResponse{}, errRunnerExecution }
	definitions, err := localtools.CopyDefinitions(request.Tools)
	if err != nil || request.Model != c.route.model || len(request.Params) != 0 || request.ToolChoice != nil || len(request.Messages) < 2 {
		return fail()
	}
	wire := make([]toolWireMessage, 0, len(request.Messages))
	callAt := -1
	var calls []api.ToolCall
	for i, message := range request.Messages {
		encoded := toolWireMessage{Role: message.Role}
		if callAt < 0 && len(message.Parts) == 1 && message.Parts[0].Text != nil {
			want := "system"
			if i > 0 {
				if i%2 == 1 {
					want = "user"
				} else {
					want = "assistant"
				}
			}
			part := message.Parts[0]
			if message.Role != want || part.ToolCall != nil || part.ToolResult != nil || part.File != nil || part.Data != nil || part.Reasoning != nil || part.Text.Text == "" {
				return fail()
			}
			encoded.Content = part.Text.Text
			wire = append(wire, encoded)
			continue
		}
		if callAt < 0 {
			if message.Role != "assistant" || i == 0 || i%2 != 0 || len(message.Parts) == 0 || len(message.Parts) > localtools.MaxCalls {
				return fail()
			}
			callAt = i
			seen := map[string]bool{}
			for _, part := range message.Parts {
				if part.ToolCall == nil || part.Text != nil || part.ToolResult != nil || part.File != nil || part.Data != nil || part.Reasoning != nil || !validModelCall(*part.ToolCall, definitions...) || seen[part.ToolCall.ID] {
					return fail()
				}
				seen[part.ToolCall.ID] = true
				call := *part.ToolCall
				calls = append(calls, call)
				args := call.Args
				if args == nil {
					args = map[string]any{}
				}
				raw, _ := json.Marshal(args)
				encoded.ToolCalls = append(encoded.ToolCalls, toolWireCall{ID: call.ID, Type: "function", Function: toolWireFunction{Name: call.Tool, Arguments: string(raw)}})
			}
			wire = append(wire, encoded)
			continue
		}
		resultIndex := i - callAt - 1
		if resultIndex < 0 || resultIndex >= len(calls) || message.Role != "tool" || len(message.Parts) != 1 || message.Parts[0].ToolResult == nil {
			return fail()
		}
		part, result := message.Parts[0], message.Parts[0].ToolResult
		if part.Text != nil || part.ToolCall != nil || part.File != nil || part.Data != nil || part.Reasoning != nil || result.ID != calls[resultIndex].ID || result.IsError || result.Error != "" || result.Output == nil || result.OutputURI != "" || result.OutputDigest != "" || len(result.Content) != 0 {
			return fail()
		}
		raw, marshalErr := json.Marshal(result.Output)
		if marshalErr != nil || len(raw) > 32*1024 {
			return fail()
		}
		encoded.Content, encoded.ToolCallID = string(raw), result.ID
		wire = append(wire, encoded)
	}
	if callAt >= 0 && len(request.Messages) != callAt+1+len(calls) {
		return fail()
	}
	body := struct {
		Model    string            `json:"model"`
		Messages []toolWireMessage `json:"messages"`
		Tools    []any             `json:"tools"`
		Stream   bool              `json:"stream"`
	}{Model: request.Model, Messages: wire}
	for _, definition := range definitions {
		body.Tools = append(body.Tools, map[string]any{"type": "function", "function": map[string]any{"name": definition.Name, "description": definition.Description, "parameters": definition.InputSchema}})
	}
	raw, err := json.Marshal(body)
	if err != nil || len(raw) > MaxRequestBytes {
		return fail()
	}
	if c.check != nil && checkJSONContent(raw, c.check, residentWirePayloadPaths(len(wire), callAt)...) != nil {
		return fail()
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.route.baseURL+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return fail()
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	version.SetUserAgent(httpRequest)
	if c.route.key != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+c.route.key)
	}
	c.sends.Add(1)
	httpResponse, err := c.http.Do(httpRequest)
	if err != nil {
		if ctx.Err() != nil {
			return api.ModelResponse{}, ctx.Err()
		}
		if c.route.safeErrors {
			var wrapped *url.Error
			if errors.As(err, &wrapped) {
				return api.ModelResponse{}, wrapped.Err
			}
		}
		return fail()
	}
	defer httpResponse.Body.Close()
	if httpResponse.StatusCode != http.StatusOK {
		return fail()
	}
	raw, err = io.ReadAll(io.LimitReader(httpResponse.Body, MaxReplyBytes+1))
	if err != nil || len(raw) > MaxReplyBytes || !utf8.Valid(raw) {
		return fail()
	}
	if c.check != nil && checkJSONContent(raw, c.check, wireReplyPayloadPaths...) != nil {
		return fail()
	}
	return parseToolReply(raw, request.Model, callAt < 0, definitions...)
}
