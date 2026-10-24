package copilot

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

const responsesStream = "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_stream\",\"model\":\"responses-only\",\"status\":\"in_progress\"}}\n\n" +
	"data: {\"type\":\"response.reasoning_text.delta\",\"delta\":\"not an answer\"}\n\n" +
	"data: {\"type\":\"response.output_text.delta\",\"delta\":\"Hello, \"}\n\n" +
	"data: {\"type\":\"response.output_text.delta\",\"delta\":\"Ada\"}\n\n" +
	"data: {\"type\":\"response.output_text.done\",\"text\":\"Hello, Ada\"}\n\n" +
	"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_stream\",\"model\":\"responses-only\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"Hello, Ada\"}]}]}}\n\n"

func TestResponsesSSETransport(t *testing.T) {
	posts := 0
	p, store := fixture(t, func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/models" {
			io.WriteString(w, catalog)
			return
		}
		if req.URL.Path != "/responses" {
			t.Error("wrong endpoint")
		}
		posts++
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, responsesStream)
	})
	store.token = canary
	route := selected()
	route.Model = "responses-only"
	client, err := p.Client(context.Background(), route)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.RoundTrip(chatRequest(t, `{"model":"responses-only","messages":[{"role":"user","content":"Ada"}],"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	var answer strings.Builder
	done, stopped := false, false
	for {
		line, err := reader.ReadString('\n')
		if strings.HasPrefix(line, "data: ") {
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
			if payload == "[DONE]" {
				done = true
				continue
			}
			var chunk struct {
				Model   string `json:"model"`
				Choices []struct {
					Delta struct {
						Content string `json:"content"`
					} `json:"delta"`
					Finish *string `json:"finish_reason"`
				} `json:"choices"`
			}
			if json.Unmarshal([]byte(payload), &chunk) != nil || chunk.Model != "responses-only" || len(chunk.Choices) != 1 {
				t.Fatalf("not a Chat-shaped chunk: %s", payload)
			}
			answer.WriteString(chunk.Choices[0].Delta.Content)
			if chunk.Choices[0].Finish != nil && *chunk.Choices[0].Finish == "stop" {
				stopped = true
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if answer.String() != "Hello, Ada" || !done || !stopped || posts != 1 {
		t.Fatalf("stream: answer=%q done=%t stopped=%t posts=%d", answer.String(), done, stopped, posts)
	}
}
