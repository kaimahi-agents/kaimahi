package copilot

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestResponsesRejectsMissingOrNullText(t *testing.T) {
	for _, field := range []string{"", `,"text":null`} {
		payload := `{"model":"responses-only","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text"` + field + `}]}]}`
		t.Run("json/"+field, func(t *testing.T) {
			response := &http.Response{Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload))}
			defer response.Body.Close()
			if err := adaptResponsesResponse(response, false); !errors.Is(err, ErrMalformed) {
				t.Fatalf("malformed text accepted: %v", err)
			}
		})
		t.Run("sse/"+field, func(t *testing.T) {
			body := newResponsesStreamBody(io.NopCloser(strings.NewReader(`data: {"type":"response.completed","response":` + payload + "}\n\n")))
			defer body.Close()
			data, err := io.ReadAll(body)
			if !errors.Is(err, ErrMalformed) || strings.Contains(string(data), "[DONE]") {
				t.Fatalf("malformed text became a completed stream: %v", err)
			}
		})
	}
	for _, field := range []string{"", `,"delta":null`} {
		t.Run("delta/"+field, func(t *testing.T) {
			body := newResponsesStreamBody(io.NopCloser(strings.NewReader("data: {\"type\":\"response.created\",\"response\":{\"model\":\"responses-only\"}}\n\n" + `data: {"type":"response.output_text.delta"` + field + "}\n\n" + `data: {"type":"response.completed","response":{"model":"responses-only","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":""}]}]}}` + "\n\n")))
			defer body.Close()
			if _, err := io.ReadAll(body); !errors.Is(err, ErrMalformed) {
				t.Fatalf("missing/null delta accepted: %v", err)
			}
		})
	}
}

func TestResponsesPreservesGenuineEmptyStreamText(t *testing.T) {
	stream := "data: {\"type\":\"response.created\",\"response\":{\"model\":\"responses-only\"}}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"\"}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"model\":\"responses-only\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"\"}]}]}}\n\n"
	body := newResponsesStreamBody(io.NopCloser(strings.NewReader(stream)))
	defer body.Close()
	data, err := io.ReadAll(body)
	if err != nil || !strings.Contains(string(data), `"content":""`) || !strings.Contains(string(data), "[DONE]") {
		t.Fatalf("genuine empty output changed: %v", err)
	}
}

func TestResponsesDoesNotInventEmptyAnswer(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		genuineEmpty bool
	}{
		{"no-message", `[]`, false},
		{"reasoning-only", `[{"type":"reasoning","summary":[{"type":"summary_text","text":"internal"}]}]`, false},
		{"genuine-empty-output-text", `[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":""}]}]`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, store := fixture(t, func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path == "/models" {
					io.WriteString(w, catalog)
					return
				}
				io.WriteString(w, `{"model":"responses-only","status":"completed","output":`+tc.output+`}`)
			})
			store.token = canary
			route := selected()
			route.Model = "responses-only"
			client, err := p.Client(context.Background(), route)
			if err != nil {
				t.Fatal(err)
			}
			response, err := client.RoundTrip(chatRequest(t, `{"model":"responses-only","messages":[{"role":"user","content":"hello"}],"stream":false}`))
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			var result struct {
				Choices []struct {
					Message struct {
						Content *string `json:"content"`
					} `json:"message"`
				} `json:"choices"`
			}
			if err := json.NewDecoder(response.Body).Decode(&result); err != nil || len(result.Choices) != 1 {
				t.Fatal("invalid converted reply")
			}
			content := result.Choices[0].Message.Content
			if (content != nil) != tc.genuineEmpty {
				t.Fatalf("fabricated/missing answer: contentPresent=%t genuineEmpty=%t", content != nil, tc.genuineEmpty)
			}
			if content != nil && *content != "" {
				t.Fatal("genuine empty text changed")
			}
		})
	}
}
