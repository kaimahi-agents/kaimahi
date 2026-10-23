package copilot

import (
	"errors"
	"io"
	"strings"
	"testing"
)

type fragmentedResponsesBody struct {
	*strings.Reader
	closes int
	err    error
}

func (b *fragmentedResponsesBody) Read(p []byte) (int, error) {
	if len(p) > 7 {
		p = p[:7]
	}
	if b.Reader.Len() == 0 && b.err != nil {
		return 0, b.err
	}
	return b.Reader.Read(p)
}
func (b *fragmentedResponsesBody) Close() error { b.closes++; return nil }

func TestResponsesStreamFragmentationAndClose(t *testing.T) {
	upstream := &fragmentedResponsesBody{Reader: strings.NewReader(responsesStream)}
	body := newResponsesStreamBody(upstream)
	out, err := io.ReadAll(body)
	if err != nil || !strings.Contains(string(out), "[DONE]") || strings.Contains(string(out), "not an answer") {
		t.Fatalf("fragmented stream: %v", err)
	}
	body.Close()
	body.Close()
	if upstream.closes != 1 {
		t.Fatalf("upstream closes=%d", upstream.closes)
	}
}

func TestResponsesStreamFailureNeverCompletes(t *testing.T) {
	created := "data: {\"type\":\"response.created\",\"response\":{\"model\":\"responses-only\",\"status\":\"in_progress\"}}\n\n"
	for _, tc := range []struct {
		name, wire    string
		readErr, want error
	}{
		{"truncated", created + "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n", nil, ErrMalformed},
		{"failed", created + "data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"message\":\"private diagnostic\"}}}\n\n", nil, ErrUnavailable},
		{"cancelled-read", created, errors.New(canary), ErrUnavailable},
		{"ambiguous-model", created + "data: {\"type\":\"response.completed\",\"response\":{\"model\":\"other\",\"model\":\"responses-only\",\"status\":\"completed\"}}\n\n", nil, ErrMalformed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := &fragmentedResponsesBody{Reader: strings.NewReader(tc.wire), err: tc.readErr}
			body := newResponsesStreamBody(upstream)
			out, err := io.ReadAll(body)
			if !errors.Is(err, tc.want) || strings.Contains(string(out), "[DONE]") || strings.Contains(string(out), `"finish_reason":"stop"`) {
				t.Fatalf("false completion: %v", err)
			}
			safe(t, err)
			body.Close()
			if upstream.closes != 1 {
				t.Fatal("upstream not closed")
			}
		})
	}
}

func TestResponsesStreamDoesNotAddScannerLineLimit(t *testing.T) {
	long := strings.Repeat("x", 70*1024)
	wire := "data: {\"type\":\"response.created\",\"response\":{\"model\":\"responses-only\"}}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"" + long + "\"}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"model\":\"responses-only\",\"status\":\"completed\",\"output\":[]}}\n\n"
	body := newResponsesStreamBody(io.NopCloser(strings.NewReader(wire)))
	defer body.Close()
	out, err := io.ReadAll(body)
	if err != nil || !strings.Contains(string(out), long) || !strings.Contains(string(out), "[DONE]") {
		t.Fatalf("long frame: %v", err)
	}
}
