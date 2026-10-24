package copilot

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
	"sync"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// Read drives upstream consumption, so downstream backpressure and Close do not
// leave a background decoder running after the caller abandons an inference.
type responsesStreamBody struct {
	upstream            io.ReadCloser
	reader              *bufio.Reader
	pending             []byte
	id, model           string
	created             int64
	text, refused, done bool
	closeOnce           sync.Once
	closeErr            error
}

func newResponsesStreamBody(body io.ReadCloser) io.ReadCloser {
	return &responsesStreamBody{upstream: body, reader: bufio.NewReader(body)}
}

func (b *responsesStreamBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for len(b.pending) == 0 && !b.done {
		data, err := b.event()
		if err != nil {
			return 0, err
		}
		if err := b.convert(data); err != nil {
			return 0, err
		}
	}
	if len(b.pending) == 0 {
		return 0, io.EOF
	}
	n := copy(p, b.pending)
	b.pending = b.pending[n:]
	return n, nil
}

func (b *responsesStreamBody) Close() error {
	b.closeOnce.Do(func() { b.closeErr = b.upstream.Close() })
	if b.closeErr != nil {
		return safeError(b.closeErr)
	}
	return nil
}

func (b *responsesStreamBody) event() ([]byte, error) {
	var data []string
	for {
		line, err := b.reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				return nil, ErrMalformed // EOF is not a completed response.
			}
			return nil, safeError(err)
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if line == "" && len(data) != 0 {
			return []byte(strings.Join(data, "\n")), nil
		}
		if value, ok := strings.CutPrefix(line, "data:"); ok {
			data = append(data, strings.TrimPrefix(value, " "))
		}
	}
}

func (b *responsesStreamBody) identity(reply responsesReply) {
	if reply.ID != "" {
		b.id = reply.ID
	}
	if reply.Model != "" {
		b.model = reply.Model
	}
	if reply.CreatedAt != 0 {
		b.created = reply.CreatedAt
	}
}

func (b *responsesStreamBody) convert(data []byte) error {
	if !runtime.JSONKeysUnique(data) {
		return ErrMalformed
	}
	if _, err := requestModel(data); err != nil {
		return err
	}
	var event struct {
		Type     string          `json:"type"`
		Delta    *string         `json:"delta"`
		Response json.RawMessage `json:"response"`
	}
	if json.Unmarshal(data, &event) != nil {
		return ErrMalformed
	}
	switch event.Type {
	case "response.created", "response.in_progress":
		var reply responsesReply
		if _, err := requestModel(event.Response); err != nil {
			return err
		}
		if json.Unmarshal(event.Response, &reply) != nil {
			return ErrMalformed
		}
		b.identity(reply)
	case "response.output_text.delta", "response.refusal.delta":
		if b.model == "" || event.Delta == nil {
			return ErrMalformed
		}
		key := "content"
		if event.Type == "response.refusal.delta" {
			b.refused = true
			key = "refusal"
		} else {
			b.text = true
		}
		return b.chunk(map[string]any{key: *event.Delta}, nil, nil)
	case "response.completed", "response.incomplete":
		var reply responsesReply
		if _, err := requestModel(event.Response); err != nil {
			return err
		}
		if json.Unmarshal(event.Response, &reply) != nil {
			return ErrMalformed
		}
		if event.Type == "response.completed" && reply.Status != "completed" || event.Type == "response.incomplete" && reply.Status != "incomplete" {
			return ErrMalformed
		}
		b.identity(reply)
		completion, err := reply.completion()
		if err != nil {
			return err
		}
		choice := completion["choices"].([]any)[0].(map[string]any)
		delta := choice["message"].(map[string]any)
		if b.text {
			delete(delta, "content")
		}
		if b.refused {
			delete(delta, "refusal")
		}
		if err := b.chunk(delta, choice["finish_reason"], completion["usage"]); err != nil {
			return err
		}
		b.pending = append(b.pending, []byte("data: [DONE]\n\n")...)
		b.done = true
	case "response.failed", "error":
		return ErrUnavailable
	}
	return nil
}

func (b *responsesStreamBody) chunk(delta map[string]any, finish, usage any) error {
	value := map[string]any{"id": b.id, "object": "chat.completion.chunk", "created": b.created, "model": b.model,
		"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}}
	if usage != nil {
		value["usage"] = usage
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return ErrMalformed
	}
	b.pending = append(b.pending, []byte("data: ")...)
	b.pending = append(b.pending, encoded...)
	b.pending = append(b.pending, '\n', '\n')
	return nil
}
