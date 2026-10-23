package copilot

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

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
