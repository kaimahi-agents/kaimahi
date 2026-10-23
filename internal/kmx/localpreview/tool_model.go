package localpreview

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
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
// the fixed one-read conversation, not arbitrary tool definitions or histories.
func (c *modelClient) ToolModel(ctx context.Context, r api.ModelRequest) (api.ModelResponse, error) {
	fail := func() (api.ModelResponse, error) { return api.ModelResponse{}, errRunnerExecution }
	if r.Model != c.route.model || len(r.Params) != 0 || r.ToolChoice != nil || !reflect.DeepEqual(r.Tools, []api.ToolDefinition{localtools.Definition()}) || (len(r.Messages) != 2 && len(r.Messages) != 4) {
		return fail()
	}
	messages := make([]toolWireMessage, 0, len(r.Messages))
	for i, m := range r.Messages {
		w := toolWireMessage{Role: m.Role}
		if len(m.Parts) != 1 {
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
			if m.Role != "assistant" || p.ToolCall == nil || p.Text != nil || p.ToolResult != nil || p.File != nil || p.Data != nil || p.Reasoning != nil || !validModelCall(*p.ToolCall) {
				return fail()
			}
			w.ToolCalls = []toolWireCall{{ID: p.ToolCall.ID, Type: "function", Function: toolWireFunction{Name: localtools.Operation, Arguments: "{}"}}}
		case 3:
			result := p.ToolResult
			if m.Role != "tool" || result == nil || p.Text != nil || p.ToolCall != nil || p.File != nil || p.Data != nil || p.Reasoning != nil || result.ID != r.Messages[2].Parts[0].ToolCall.ID || result.IsError || result.Error != "" || result.Output == nil || result.OutputURI != "" || result.OutputDigest != "" || len(result.Content) != 0 {
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
	definition := localtools.Definition()
	body := struct {
		Model    string            `json:"model"`
		Messages []toolWireMessage `json:"messages"`
		Tools    []any             `json:"tools"`
		Stream   bool              `json:"stream"`
	}{Model: r.Model, Messages: messages, Tools: []any{map[string]any{"type": "function", "function": map[string]any{"name": definition.Name, "description": definition.Description, "parameters": definition.InputSchema}}}}
	raw, err := json.Marshal(body)
	if err != nil || len(raw) > MaxRequestBytes {
		return fail()
	}
	if c.check != nil {
		if err = c.check(string(raw)); err != nil {
			return fail()
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.route.baseURL+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return fail()
	}
	req.Header.Set("Content-Type", "application/json")
	c.sends.Add(1)
	response, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return api.ModelResponse{}, ctx.Err()
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
		if err = c.check(string(raw)); err != nil {
			return fail()
		}
	}
	result, err := parseToolReply(raw, r.Model, len(r.Messages) == 2)
	if err != nil {
		return fail()
	}
	if c.check != nil {
		fields := []string{result.Message.Text()}
		for _, part := range result.Message.Parts {
			if part.ToolCall != nil {
				fields = append(fields, part.ToolCall.ID, part.ToolCall.Tool)
			}
		}
		if c.check(fields...) != nil {
			return fail()
		}
	}
	return result, nil
}

func validModelCall(c api.ToolCall) bool {
	return modelCallID.MatchString(c.ID) && c.Tool == localtools.Operation && len(c.Args) == 0 && c.Mediation == "" && c.IdempotencyKey == ""
}

func parseToolReply(raw []byte, model string, allowCall bool) (api.ModelResponse, error) {
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
	if d.Decode(&calls) != nil || len(calls) != 1 || calls[0].Type != "function" || calls[0].Function.Description != "" || calls[0].Function.Parameters != nil {
		return fail()
	}
	var args map[string]any
	if json.Unmarshal([]byte(calls[0].Function.Arguments), &args) != nil || args == nil || len(args) != 0 {
		return fail()
	}
	call := api.ToolCall{ID: calls[0].ID, Tool: calls[0].Function.Name, Args: args}
	if !validModelCall(call) {
		return fail()
	}
	return api.ModelResponse{Message: api.Message{Role: "assistant", Parts: []api.Part{{ToolCall: &call}}}}, nil
}
