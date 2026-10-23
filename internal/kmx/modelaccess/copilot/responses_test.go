package copilot

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"testing"
)

func TestResponsesJSONTransport(t *testing.T) {
	posts := 0
	p, store := fixture(t, func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/models":
			io.WriteString(w, catalog)
		case "/responses":
			posts++
			var body map[string]any
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			want := map[string]any{
				"model": "responses-only", "stream": false, "store": false,
				"max_output_tokens": float64(1024),
				"input": []any{
					map[string]any{"role": "system", "content": "Return a greeting."},
					map[string]any{"role": "user", "content": "Ada"},
				},
				"metadata": map[string]any{"purpose": "unit"},
			}
			if !reflect.DeepEqual(body, want) {
				t.Errorf("Responses payload: got %#v, want %#v", body, want)
			}
			if req.Header.Get("Authorization") != "Bearer "+canary {
				t.Error("missing private bearer")
			}
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"resp_test","object":"response","created_at":1900000000,"model":"responses-only","status":"completed","output":[{"type":"reasoning","summary":[{"type":"summary_text","text":"not an answer"}]},{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Hello, "},{"type":"output_text","text":"Ada"}]}],"usage":{"input_tokens":11,"output_tokens":4,"total_tokens":15}}`)
		default:
			t.Errorf("wrong protocol endpoint %s", req.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	store.token = canary
	route := selected()
	route.Model = "responses-only"
	client, err := p.Client(context.Background(), route)
	if err != nil {
		t.Fatal(err)
	}
	req := chatRequest(t, `{"model":"responses-only","messages":[{"role":"system","content":"Return a greeting."},{"role":"user","content":"Ada"}],"max_completion_tokens":1024,"stream":false,"metadata":{"purpose":"unit"}}`)
	response, err := client.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var result struct {
		Model   string `json:"model"`
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.Model != "responses-only" || len(result.Choices) != 1 || result.Choices[0].Message.Role != "assistant" || result.Choices[0].Message.Content != "Hello, Ada" || result.Choices[0].FinishReason != "stop" {
		t.Fatalf("not an attributable Chat-shaped completion: %+v", result)
	}
	if result.Usage.PromptTokens != 11 || result.Usage.CompletionTokens != 4 || result.Usage.TotalTokens != 15 {
		t.Fatalf("usage lost: %+v", result.Usage)
	}
	if posts != 1 || response.Request != req || response.Request.Header.Get("Authorization") != "" {
		t.Fatal("send count or authenticated request leaked")
	}
}
