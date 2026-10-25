package copilot

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestResponsesToolTerminalStream(t *testing.T) {
	prefix := `data: {"type":"response.created","response":{"id":"r1","model":"gpt-test"}}` + "\n\n" +
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call_1","name":"lookup","arguments":""}}` + "\n\n" +
		`data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"partial"}` + "\n\n"
	for _, complete := range []bool{false, true} {
		body := prefix
		if complete {
			body += `data: {"type":"response.completed","response":` + responsesToolReply + "}\n\n"
		}
		stream := newResponsesStreamBody(io.NopCloser(strings.NewReader(body)))
		raw, err := io.ReadAll(stream)
		stream.Close()
		if !complete {
			if !errors.Is(err, ErrMalformed) || strings.Contains(string(raw), "tool_calls") {
				t.Fatal("partial stream exported a call")
			}
			continue
		}
		if err != nil || !strings.Contains(string(raw), `"tool_calls"`) || !strings.Contains(string(raw), `"id":"call_1"`) || !strings.Contains(string(raw), `"finish_reason":"tool_calls"`) || !strings.Contains(string(raw), "[DONE]") {
			t.Fatalf("terminal call lost: %s / %v", raw, err)
		}
		if strings.Contains(string(raw), "partial") || strings.Contains(string(raw), "private-reasoning") {
			t.Fatal("incomplete arguments or private reasoning escaped")
		}
	}
}
