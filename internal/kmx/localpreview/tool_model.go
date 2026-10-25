package localpreview

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"unicode/utf8"

	"github.com/agent-primitives/sessions/api"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

type toolWireCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function toolWireFunction `json:"function"`
}
type toolWireFunction struct {
	Name        string         `json:"name"`
	Arguments   string         `json:"arguments,omitempty"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}
type toolWireMessage struct {
	Role       string         `json:"role"`
	Content    any            `json:"content"`
	ToolCalls  []toolWireCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

var modelCallID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ToolModel is separate from the strict text-only converter. It accepts only
// one bounded batch using host-owned definitions, never arbitrary histories.
func (c *modelClient) ToolModel(ctx context.Context, r api.ModelRequest) (api.ModelResponse, error) {
	fail := func() (api.ModelResponse, error) { return api.ModelResponse{}, errRunnerExecution }
	defs, defsErr := localtools.CopyDefinitions(r.Tools)
	if r.Model != c.route.model || len(r.Params) != 0 || r.ToolChoice != nil || defsErr != nil || (len(r.Messages) != 2 && (len(r.Messages) < 4 || len(r.Messages) > 3+localtools.MaxCalls)) {
		return fail()
	}
	messages := make([]toolWireMessage, 0, len(r.Messages))
	for i, m := range r.Messages {
		w := toolWireMessage{Role: m.Role}
		if len(m.Parts) == 0 || i != 2 && len(m.Parts) != 1 {
			return fail()
		}
		p := m.Parts[0]
		switch i {
		case 0, 1:
			role := "system"
			if i == 1 {
				role = "user"
			}
			if m.Role != role || p.Text == nil || p.ToolCall != nil || p.ToolResult != nil || p.File != nil || p.Data != nil || p.Reasoning != nil {
				return fail()
			}
			w.Content = p.Text.Text
		case 2:
			if m.Role != "assistant" || len(m.Parts) > localtools.MaxCalls || len(r.Messages) != 3+len(m.Parts) {
				return fail()
			}
			seen := map[string]bool{}
			for _, p := range m.Parts {
				if p.ToolCall == nil || p.Text != nil || p.ToolResult != nil || p.File != nil || p.Data != nil || p.Reasoning != nil || !validModelCall(*p.ToolCall, defs...) || seen[p.ToolCall.ID] {
					return fail()
				}
				seen[p.ToolCall.ID] = true
				args := p.ToolCall.Args
				if args == nil {
					args = map[string]any{}
				}
				raw, _ := json.Marshal(args)
				w.ToolCalls = append(w.ToolCalls, toolWireCall{ID: p.ToolCall.ID, Type: "function", Function: toolWireFunction{Name: p.ToolCall.Tool, Arguments: string(raw)}})
			}
		default:
			result := p.ToolResult
			if m.Role != "tool" || result == nil || p.Text != nil || p.ToolCall != nil || p.File != nil || p.Data != nil || p.Reasoning != nil || result.ID != r.Messages[2].Parts[i-3].ToolCall.ID || result.IsError || result.Error != "" || result.Output == nil || result.OutputURI != "" || result.OutputDigest != "" || len(result.Content) != 0 {
				return fail()
			}
			raw, err := json.Marshal(result.Output)
			if err != nil || len(raw) > 32*1024 {
				return fail()
			}
			w.Content = string(raw)
			w.ToolCallID = result.ID
		}
		messages = append(messages, w)
	}
	tools := make([]any, 0, len(defs))
	for _, d := range defs {
		tools = append(tools, map[string]any{"type": "function", "function": map[string]any{"name": d.Name, "description": d.Description, "parameters": d.InputSchema}})
	}
	body := struct {
		Model    string            `json:"model"`
		Messages []toolWireMessage `json:"messages"`
		Tools    []any             `json:"tools"`
		Stream   bool              `json:"stream"`
	}{Model: r.Model, Messages: messages, Tools: tools}
	raw, err := json.Marshal(body)
	if err != nil || len(raw) > MaxRequestBytes {
		return fail()
	}
	if c.check != nil {
		if err = checkJSONContent(raw, c.check); err != nil {
			return api.ModelResponse{}, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.route.baseURL+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return fail()
	}
	req.Header.Set("Content-Type", "application/json")
	if err := ctx.Err(); err != nil {
		return api.ModelResponse{}, err
	}
	if c.route.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.route.key)
	}
	c.sends.Add(1)
	response, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return api.ModelResponse{}, ctx.Err()
		}
		if c.route.safeErrors {
			var wrapped *url.Error
			if errors.As(err, &wrapped) {
				return api.ModelResponse{}, wrapped.Err
			}
			return api.ModelResponse{}, err
		}
		return fail()
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fail()
	}
	raw, err = io.ReadAll(io.LimitReader(response.Body, MaxReplyBytes+1))
	if err != nil || len(raw) > MaxReplyBytes || !utf8.Valid(raw) {
		return fail()
	}
	if c.check != nil {
		if err = checkJSONContent(raw, c.check); err != nil {
			return api.ModelResponse{}, err
		}
	}
	result, err := parseToolReply(raw, r.Model, len(r.Messages) == 2, defs...)
	if err != nil {
		return fail()
	}
	if c.check != nil {
		if err := localtools.CheckJSON(result, c.check); err != nil {
			return api.ModelResponse{}, err
		}
	}
	return result, nil
}

func validModelCall(c api.ToolCall, defs ...api.ToolDefinition) bool {
	if defs == nil {
		defs = []api.ToolDefinition{localtools.Definition()}
	}
	return modelCallID.MatchString(c.ID) && localtools.ValidateArguments(defs, c.Tool, c.Args) == nil && c.Mediation == "" && c.IdempotencyKey == ""
}

func parseToolReply(raw []byte, model string, allowCall bool, defs ...api.ToolDefinition) (api.ModelResponse, error) {
	fail := func() (api.ModelResponse, error) { return api.ModelResponse{}, errRunnerExecution }
	if !runtime.JSONKeysUnique(raw) {
		return fail()
	}
	var envelope struct {
		Model   string `json:"model"`
		Choices []struct {
			Finish  string                     `json:"finish_reason"`
			Message map[string]json.RawMessage `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.Model != model || len(envelope.Choices) != 1 {
		return fail()
	}
	choice := envelope.Choices[0]
	m := choice.Message
	for key, value := range m {
		switch key {
		case "role", "content", "tool_calls":
		case "refusal":
			if !bytes.Equal(value, []byte("null")) {
				return fail()
			}
		default:
			return fail()
		}
	}
	var role, text string
	if json.Unmarshal(m["role"], &role) != nil || role != "assistant" {
		return fail()
	}
	if choice.Finish == "stop" {
		if v := m["tool_calls"]; len(v) > 0 && !bytes.Equal(v, []byte("null")) && !bytes.Equal(v, []byte("[]")) {
			return fail()
		}
		if json.Unmarshal(m["content"], &text) != nil || text == "" || len(text) > 256*1024 || !utf8.ValidString(text) {
			return fail()
		}
		return api.ModelResponse{Message: *api.TextMessage("assistant", text)}, nil
	}
	if !allowCall || choice.Finish != "tool_calls" {
		return fail()
	}
	if v := m["content"]; len(v) > 0 && !bytes.Equal(v, []byte("null")) && !bytes.Equal(v, []byte(`""`)) {
		return fail()
	}
	var calls []toolWireCall
	d := json.NewDecoder(bytes.NewReader(m["tool_calls"]))
	d.DisallowUnknownFields()
	if d.Decode(&calls) != nil || len(calls) == 0 || len(calls) > localtools.MaxCalls {
		return fail()
	}
	message := api.Message{Role: "assistant"}
	seen := map[string]bool{}
	for _, c := range calls {
		if c.Type != "function" || c.Function.Description != "" || c.Function.Parameters != nil || seen[c.ID] {
			return fail()
		}
		var args map[string]any
		if len(c.Function.Arguments) > 8*1024 || !runtime.JSONKeysUnique([]byte(c.Function.Arguments)) || !runtime.JSONNumbersExact([]byte(c.Function.Arguments)) || json.Unmarshal([]byte(c.Function.Arguments), &args) != nil || args == nil {
			return fail()
		}
		call := api.ToolCall{ID: c.ID, Tool: c.Function.Name, Args: args}
		if !validModelCall(call, defs...) {
			return fail()
		}
		seen[c.ID] = true
		message.Parts = append(message.Parts, api.Part{ToolCall: &call})
	}
	return api.ModelResponse{Message: message}, nil
}
