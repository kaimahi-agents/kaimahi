package localpreview

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/agent-primitives/sessions/api"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

const (
	// MaxRequestBytes bounds one encoded chat-completions request.
	MaxRequestBytes = 1 << 20
	// MaxReplyBytes bounds one model response body and therefore the answer.
	MaxReplyBytes = 1 << 20
)

// modelClient sends exactly one Chat Completions request per Model call to the
// resolved route. It never retries, follows redirects, offers tools, executes
// tool calls or substitutes another model. Its errors are fixed *Refusal text
// or context errors, because the Sessions controller journals them verbatim.
type modelClient struct {
	route route
	http  *http.Client
	sends atomic.Int64
}

func newModelClient(r route) *modelClient {
	transport := r.transport
	if transport == nil {
		// Standard proxy and TLS verification behavior; no shared mutation.
		transport = http.DefaultTransport.(*http.Transport).Clone()
	}
	return &modelClient{route: r, http: &http.Client{
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
}

// Model satisfies controller.ModelFunc.
func (c *modelClient) Model(ctx context.Context, req api.ModelRequest) (api.ModelResponse, error) {
	if req.Model != c.route.model {
		return api.ModelResponse{}, refuse(RefusedRequest, "the harness requested a model other than the selected one",
			"select the agent's exact Model.Preferred; there is no model fallback")
	}
	if len(req.Tools) > 0 || req.ToolChoice != nil {
		return api.ModelResponse{}, refuse(RefusedTools, "the request offered tools", "local preview is tool-free; remove tool requests")
	}
	if len(req.Params) > 0 {
		return api.ModelResponse{}, refuse(RefusedRequest, "the request carried sampling parameters", "local preview sends the chat request unchanged")
	}
	body := chatRequest{Model: req.Model, Stream: false, Messages: make([]chatMessage, 0, len(req.Messages))}
	for _, message := range req.Messages {
		var text strings.Builder
		for _, part := range message.Parts {
			if part.Text == nil {
				return api.ModelResponse{}, refuse(RefusedRequest, "the request contained non-text content", "local preview sends text only")
			}
			text.WriteString(part.Text.Text)
		}
		switch message.Role {
		case "system", "user", "assistant":
		default:
			return api.ModelResponse{}, refuse(RefusedRequest, "the request contained an unsupported message role", "local preview sends system, user and assistant text only")
		}
		body.Messages = append(body.Messages, chatMessage{Role: message.Role, Content: text.String()})
	}
	encoded, err := json.Marshal(body)
	if err != nil || len(encoded) > MaxRequestBytes {
		return api.ModelResponse{}, refuse(RefusedRequest, "the model request exceeds 1 MiB", "shorten the instructions or input")
	}
	text, err := c.send(ctx, encoded)
	if err != nil {
		return api.ModelResponse{}, err
	}
	return api.ModelResponse{Message: *api.TextMessage("assistant", text)}, nil
}

func (c *modelClient) send(ctx context.Context, body []byte) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.route.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", refuse(RefusedEndpoint, "the model request could not be built", "check the endpoint base URL")
	}
	request.Header.Set("Content-Type", "application/json")
	if c.route.key != "" {
		request.Header.Set("Authorization", "Bearer "+c.route.key)
	}
	c.sends.Add(1)
	response, err := c.http.Do(request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		// Transport errors can quote URLs or proxy diagnostics; report a fixed class.
		return "", refuse(RefusedUnreachable, "the model endpoint was unreachable", "check that the endpoint is running and reachable; nothing was retried")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		remedy := "check the endpoint and model name; the response body is not shown"
		switch response.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			remedy = "check the credential named by the endpoint key environment variable; the response body is not shown"
		case http.StatusNotFound:
			remedy = "check the endpoint base URL and that Model.Preferred is installed or served; the response body is not shown"
		}
		return "", refuse(RefusedHTTPStatus, fmt.Sprintf("the model endpoint returned HTTP %d", response.StatusCode), remedy)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, MaxReplyBytes+1))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		return "", refuse(RefusedUnreachable, "the model response could not be read", "check the endpoint; nothing was retried")
	}
	if len(raw) > MaxReplyBytes {
		return "", refuse(RefusedOversized, "the model response exceeds 1 MiB", "ask for a shorter answer or use a model that stops sooner")
	}
	content, err := parseReply(raw)
	if err != nil {
		return "", err
	}
	// The answer is journaled and returned; never let the endpoint copy the
	// credential it received into either.
	if c.route.key != "" && strings.Contains(content, c.route.key) {
		return "", refuse(RefusedCredentialEcho, "the model reply contained the endpoint credential, so it was withheld and not journaled",
			"fix the endpoint so its replies never include the Authorization header")
	}
	return content, nil
}

// parseReply accepts exactly one completed assistant text message. A tool or
// function call in any case spelling is refused and never executed. A reply
// that repeats a key at any depth, including a case-insensitive alias, is
// malformed: encoding/json keeps the last occurrence, which would let a later
// null tool_calls or "stop" finish_reason shadow an earlier one.
func parseReply(raw []byte) (string, error) {
	malformed := refuse(RefusedMalformed, "the model reply is not one completed chat-completions text message", "use an OpenAI-compatible chat-completions endpoint")
	if !agentruntime.JSONKeysUnique(raw) {
		return "", malformed
	}
	var envelope struct {
		Choices []map[string]json.RawMessage `json:"choices"`
	}
	if json.Unmarshal(raw, &envelope) != nil || len(envelope.Choices) != 1 {
		return "", malformed
	}
	var finish string
	var message map[string]json.RawMessage
	for key, value := range envelope.Choices[0] {
		switch {
		case strings.EqualFold(key, "finish_reason"):
			if json.Unmarshal(value, &finish) != nil {
				return "", malformed
			}
		case strings.EqualFold(key, "message"):
			if message != nil || json.Unmarshal(value, &message) != nil {
				return "", malformed
			}
		}
	}
	toolCall := strings.EqualFold(finish, "tool_calls") || strings.EqualFold(finish, "function_call")
	var role, content string
	var contentSet, roleSet bool
	for key, value := range message {
		present := len(bytes.TrimSpace(value)) > 0 && !bytes.Equal(bytes.TrimSpace(value), []byte("null")) && !bytes.Equal(bytes.TrimSpace(value), []byte("[]"))
		switch {
		case strings.EqualFold(key, "tool_calls"), strings.EqualFold(key, "function_call"):
			toolCall = toolCall || present
		case strings.EqualFold(key, "refusal"):
			if present {
				return "", refuse(RefusedMalformed, "the model refused to answer", "revise the instructions or input")
			}
		case strings.EqualFold(key, "role"):
			if roleSet || json.Unmarshal(value, &role) != nil {
				return "", malformed
			}
			roleSet = true
		case strings.EqualFold(key, "content"):
			if contentSet || (present && json.Unmarshal(value, &content) != nil) {
				return "", malformed
			}
			contentSet = present
		}
	}
	if toolCall {
		return "", refuse(RefusedToolCall, "the model replied with a tool call, which was not executed",
			"local preview is tool-free; remove tool use from the instructions or choose a model that answers in text")
	}
	if finish == "length" {
		return "", refuse(RefusedTruncated, "the model reply was truncated", "ask for a shorter answer")
	}
	if message == nil || role != "assistant" || !contentSet || finish != "stop" {
		return "", malformed
	}
	return content, nil
}
