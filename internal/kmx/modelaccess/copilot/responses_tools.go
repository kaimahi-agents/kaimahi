package copilot

import (
	"encoding/json"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
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
	calls  []responseChatCall
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
	if pending == nil || len(messages) != 3+len(pending.calls) || conversationKey(fields, messages[:2]) != pending.key {
		return nil, "", ErrMalformed
	}
	var calls []responseChatCall
	if json.Unmarshal(messages[2]["tool_calls"], &calls) != nil || len(calls) != len(pending.calls) {
		return nil, "", ErrMalformed
	}
	input := []any{messages[0], messages[1]}
	for _, item := range pending.output {
		input = append(input, item)
	}
	for i, c := range calls {
		want := pending.calls[i]
		var id, output string
		if c.Type != "function" || c.ID != want.ID || c.Function.Name != want.Function.Name || !sameArguments(c.Function.Arguments, want.Function.Arguments) || json.Unmarshal(messages[3+i]["tool_call_id"], &id) != nil || id != want.ID || json.Unmarshal(messages[3+i]["content"], &output) != nil {
			return nil, "", ErrMalformed
		}
		input = append(input, map[string]string{"type": "function_call_output", "call_id": id, "output": output})
	}
	var next map[string]json.RawMessage
	_ = json.Unmarshal(encoded, &next)
	next["input"], _ = json.Marshal(input)
	encoded, err = json.Marshal(next)
	return encoded, "", err
}

func (t *previewTransport) rememberResponses(body []byte, key string) error {
	t.continuation = nil
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
	seen := map[string]bool{}
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
		if item.CallID == "" || seen[item.CallID] || item.Name == "" || len(seen) >= localtools.MaxCalls {
			return ErrMalformed
		}
		// Arguments are themselves JSON text: inspect decoded keys/values too.
		if err := t.access.checkJSON([]byte(item.Arguments)); err != nil {
			return err
		}
		if found == nil {
			found = &responsesContinuation{key: key, output: reply.Output}
		}
		call := responseChatCall{ID: item.CallID, Type: "function"}
		call.Function.Name = item.Name
		call.Function.Arguments = item.Arguments
		found.calls = append(found.calls, call)
		seen[item.CallID] = true
	}
	t.continuation = found
	return nil
}
