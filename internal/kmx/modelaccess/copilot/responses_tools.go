package copilot

import (
	"encoding/json"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

type responseChatCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// Only the transport sees provider continuation items. They never enter a
// Sessions model response or journal and cannot authorize a resumed execution.
type responsesContinuation struct {
	key    string
	call   responseChatCall
	output []json.RawMessage
}

func conversationKey(fields map[string]json.RawMessage, messages []map[string]json.RawMessage) string {
	b, _ := json.Marshal(map[string]any{"messages": messages, "tools": fields["tools"]})
	return string(b)
}

func sameArguments(a, b string) bool {
	var x, y any
	if !runtime.JSONNumbersExact([]byte(a)) || !runtime.JSONNumbersExact([]byte(b)) {
		return false
	}
	if json.Unmarshal([]byte(a), &x) != nil || json.Unmarshal([]byte(b), &y) != nil {
		return false
	}
	xb, _ := json.Marshal(x)
	yb, _ := json.Marshal(y)
	return string(xb) == string(yb)
}

func (t *previewTransport) responsesRequest(body []byte) ([]byte, string, error) {
	pending := t.continuation
	t.continuation = nil // Consume before any send; an ambiguous failure never retries.
	var fields map[string]json.RawMessage
	var messages []map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil || json.Unmarshal(fields["messages"], &messages) != nil {
		return nil, "", ErrMalformed
	}
	encoded, _, err := encodeResponsesRequest(body)
	if err != nil {
		return nil, "", err
	}
	if len(messages) == 0 {
		return encoded, "", nil
	}
	var role string
	_ = json.Unmarshal(messages[len(messages)-1]["role"], &role)
	if role != "tool" {
		return encoded, conversationKey(fields, messages), nil
	}
	if len(messages) != 4 || pending == nil || conversationKey(fields, messages[:2]) != pending.key {
		return nil, "", ErrMalformed
	}
	var calls []responseChatCall
	var id, output string
	if json.Unmarshal(messages[2]["tool_calls"], &calls) != nil || len(calls) != 1 || calls[0].Type != "function" || calls[0].ID != pending.call.ID || calls[0].Function.Name != pending.call.Function.Name || !sameArguments(calls[0].Function.Arguments, pending.call.Function.Arguments) || json.Unmarshal(messages[3]["tool_call_id"], &id) != nil || id != pending.call.ID || json.Unmarshal(messages[3]["content"], &output) != nil {
		return nil, "", ErrMalformed
	}
	input := []any{messages[0], messages[1]}
	for _, item := range pending.output {
		input = append(input, item)
	}
	input = append(input, map[string]string{"type": "function_call_output", "call_id": id, "output": output})
	var next map[string]json.RawMessage
	_ = json.Unmarshal(encoded, &next)
	next["input"], _ = json.Marshal(input)
	encoded, err = json.Marshal(next)
	return encoded, "", err
}

func (t *previewTransport) rememberResponses(body []byte, key string) error {
	if key == "" {
		return nil
	}
	var reply struct {
		Status string            `json:"status"`
		Output []json.RawMessage `json:"output"`
	}
	if json.Unmarshal(body, &reply) != nil {
		return ErrMalformed
	}
	if reply.Status != "completed" {
		return nil
	}
	var found *responsesContinuation
	for _, raw := range reply.Output {
		var item struct {
			Type, Name, Arguments string
			CallID                string `json:"call_id"`
		}
		if json.Unmarshal(raw, &item) != nil {
			return ErrMalformed
		}
		if item.Type != "function_call" {
			continue
		}
		if found != nil || item.CallID == "" || item.Name == "" {
			return ErrMalformed
		}
		// Arguments are themselves JSON text: inspect decoded keys/values too.
		if err := t.access.checkJSON([]byte(item.Arguments)); err != nil {
			return err
		}
		found = &responsesContinuation{key: key, output: reply.Output}
		found.call.ID = item.CallID
		found.call.Type = "function"
		found.call.Function.Name = item.Name
		found.call.Function.Arguments = item.Arguments
	}
	t.continuation = found
	return nil
}
