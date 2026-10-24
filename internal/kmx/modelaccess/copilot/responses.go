package copilot

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// The owned preview/judge callers remain Chat-shaped; protocol details do not
// become route settings or author-controlled endpoint fields.
func encodeResponsesRequest(body []byte) ([]byte, bool, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil {
		return nil, false, ErrMalformed
	}
	var stream bool
	if value, ok := fields["stream"]; ok && json.Unmarshal(value, &stream) != nil {
		return nil, false, ErrMalformed
	}
	if messages, ok := fields["messages"]; ok {
		fields["input"] = messages
		delete(fields, "messages")
	}
	if budget, ok := fields["max_completion_tokens"]; ok {
		fields["max_output_tokens"] = budget
		delete(fields, "max_completion_tokens")
		delete(fields, "max_tokens")
	} else if budget, ok := fields["max_tokens"]; ok {
		fields["max_output_tokens"] = budget
		delete(fields, "max_tokens")
	}
	fields["store"] = json.RawMessage("false")
	encoded, err := json.Marshal(fields)
	if err != nil {
		return nil, false, ErrMalformed
	}
	return encoded, stream, nil
}

type responsesReply struct {
	ID        string `json:"id"`
	CreatedAt int64  `json:"created_at"`
	Model     string `json:"model"`
	Status    string `json:"status"`
	Output    []struct {
		Type      string `json:"type"`
		Role      string `json:"role"`
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
		Content   []struct {
			Type    string  `json:"type"`
			Text    *string `json:"text"`
			Refusal string  `json:"refusal"`
		} `json:"content"`
	} `json:"output"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
}

func (r responsesReply) completion() (map[string]any, error) {
	var answer, refusal strings.Builder
	refused, hasText := false, false
	var tools []any
	for _, item := range r.Output {
		if item.Type == "function_call" {
			tools = append(tools, map[string]any{"id": item.CallID, "type": "function", "function": map[string]string{"name": item.Name, "arguments": item.Arguments}})
		}
		if item.Type != "message" || item.Role != "assistant" {
			continue
		}
		for _, part := range item.Content {
			switch part.Type {
			case "output_text":
				if part.Text == nil {
					return nil, ErrMalformed
				}
				hasText = true
				answer.WriteString(*part.Text)
			case "refusal":
				refused = true
				refusal.WriteString(part.Refusal)
			}
		}
	}
	finish := ""
	if r.Status == "completed" {
		finish = "stop"
		if len(tools) != 0 {
			finish = "tool_calls"
		}
	} else if r.Status == "incomplete" {
		finish = "length"
	}
	message := map[string]any{"role": "assistant"}
	if hasText {
		message["content"] = answer.String()
	}
	if refused {
		message["refusal"] = refusal.String()
	}
	if len(tools) != 0 {
		message["tool_calls"] = tools
	}
	return map[string]any{
		"id": r.ID, "object": "chat.completion", "created": r.CreatedAt,
		"model": r.Model,
		"choices": []any{map[string]any{
			"index": 0, "message": message,
			"finish_reason": finish,
		}},
		"usage": map[string]int{"prompt_tokens": r.Usage.InputTokens, "completion_tokens": r.Usage.OutputTokens, "total_tokens": r.Usage.TotalTokens},
	}, nil
}

func adaptResponsesResponse(response *http.Response, stream bool) error {
	if stream {
		response.Body = newResponsesStreamBody(response.Body)
		response.ContentLength = -1
		response.Header.Set("Content-Type", "text/event-stream")
		response.Header.Del("Content-Length")
		response.Header.Del("Content-Encoding")
		return nil
	}
	// This is the same cap as the existing direct preview/judge consumer.
	body, err := readBounded(response.Body, responseLimit)
	if err != nil {
		return err
	}
	// Keep duplicate/case-variant model keys from being laundered into a clean
	// Chat reply: the direct caller's attribution checks still own the decision.
	if !runtime.JSONKeysUnique(body) {
		return ErrMalformed
	}
	if _, err := requestModel(body); err != nil {
		return err
	}
	var reply responsesReply
	if json.Unmarshal(body, &reply) != nil {
		return ErrMalformed
	}
	if reply.Status == "failed" || reply.Status == "cancelled" {
		return ErrUnavailable
	}
	completion, err := reply.completion()
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(completion)
	if err != nil {
		return ErrMalformed
	}
	if err := response.Body.Close(); err != nil {
		return safeError(err)
	}
	response.Body = io.NopCloser(bytes.NewReader(encoded))
	response.ContentLength = int64(len(encoded))
	response.Header.Set("Content-Type", "application/json")
	response.Header.Del("Content-Encoding")
	response.Header.Set("Content-Length", strconv.Itoa(len(encoded)))
	return nil
}
