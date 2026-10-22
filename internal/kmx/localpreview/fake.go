package localpreview

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// The fake model understands only these documented instruction directives, so
// an instruction edit really changes its answer and it cannot pass every check:
//
//	Reply with exactly `TEXT`.                      -> TEXT
//	Reply with exactly `TEXT` followed by the input  -> TEXT + input
//	Call the tool `NAME`                             -> a tool-call reply (refused, never executed)
//
// The first matching directive wins. Without one, the fake answers with fixed
// guidance text that names the supported directive.
var (
	fakeReply = regexp.MustCompile("(?i)reply with exactly\\s+`([^`\\n]*)`(\\s+followed by the input)?")
	fakeTool  = regexp.MustCompile("(?i)call the tool\\s+`([A-Za-z0-9_.-]{1,64})`")
	fakeQuote = regexp.MustCompile("`([^`\\n]+)`")
)

// FakeNoDirectiveAnswer is the fake model's answer when the instructions have
// no supported directive.
const FakeNoDirectiveAnswer = "fake model: no supported directive found; add \"Reply with exactly `TEXT`\" to the instructions"

// FakeAnswer returns the deterministic fake answer for instructions and input,
// and whether the fake replies with a tool call instead.
func FakeAnswer(instructions, input string) (answer string, toolCall bool) {
	reply := fakeReply.FindStringSubmatchIndex(instructions)
	tool := fakeTool.FindStringIndex(instructions)
	if tool != nil && (reply == nil || tool[0] < reply[0]) {
		return "", true
	}
	if reply == nil {
		return FakeNoDirectiveAnswer, false
	}
	answer = instructions[reply[2]:reply[3]]
	if reply[4] >= 0 {
		answer += input
	}
	return answer, false
}

// fakeJudgeScore is the fake judge: every backtick-quoted phrase in the rubric
// must appear in the answer (score 100) or it fails (score 0). A rubric with no
// quoted phrase cannot be judged and yields a malformed reply, which the shared
// judge reports as unknown.
func fakeJudgeScore(rubric, answer string) (int, bool) {
	phrases := fakeQuote.FindAllStringSubmatch(rubric, -1)
	if len(phrases) == 0 {
		return 0, false
	}
	for _, phrase := range phrases {
		if !strings.Contains(answer, phrase[1]) {
			return 0, true
		}
	}
	return 100, true
}

// fakeTransport serves Chat Completions in memory. It never dials, so the fake
// route stays offline. Agent requests use the instruction directives above;
// requests carrying the shared judge policy are answered by the fake judge.
type fakeTransport struct{}

func (fakeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Body != nil {
		defer request.Body.Close()
	}
	respond := func(code int, body any) (*http.Response, error) {
		encoded, _ := json.Marshal(body)
		return &http.Response{
			StatusCode: code, Status: http.StatusText(code), Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
			Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(encoded)),
			ContentLength: int64(len(encoded)), Request: request,
		}, nil
	}
	if err := request.Context().Err(); err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(request.Body, MaxRequestBytes+1))
	if request.Method != http.MethodPost || !strings.HasSuffix(request.URL.Path, "/chat/completions") || err != nil || len(raw) > MaxRequestBytes {
		return respond(http.StatusBadRequest, map[string]any{"error": map[string]string{"message": "fake: bad request"}})
	}
	var chat struct {
		Model    string        `json:"model"`
		Messages []chatMessage `json:"messages"`
	}
	if json.Unmarshal(raw, &chat) != nil || len(chat.Messages) != 2 || chat.Messages[0].Role != "system" || chat.Messages[1].Role != "user" {
		return respond(http.StatusBadRequest, map[string]any{"error": map[string]string{"message": "fake: expected one system and one user message"}})
	}
	message := map[string]any{"role": "assistant"}
	finish := "stop"
	if chat.Messages[0].Content == agentruntime.EvaluationJudgePolicy {
		var judged struct{ Input, Answer, Rubric string }
		if json.Unmarshal([]byte(chat.Messages[1].Content), &judged) != nil {
			return respond(http.StatusBadRequest, map[string]any{"error": map[string]string{"message": "fake: bad judge request"}})
		}
		if score, ok := fakeJudgeScore(judged.Rubric, judged.Answer); ok {
			content, _ := json.Marshal(map[string]any{"score": score, "reasoning": "fake judge: quoted rubric phrases checked"})
			message["content"] = string(content)
		} else {
			message["content"] = "fake judge: quote the expected phrase in the rubric with backticks"
		}
	} else {
		if chat.Model != FakeModel {
			return respond(http.StatusNotFound, map[string]any{"error": map[string]string{"message": "fake: unknown model"}})
		}
		answer, toolCall := FakeAnswer(chat.Messages[0].Content, chat.Messages[1].Content)
		message["content"] = answer
		if toolCall {
			name := fakeTool.FindStringSubmatch(chat.Messages[0].Content)[1]
			message["content"] = nil
			message["tool_calls"] = []any{map[string]any{"id": "fake-call", "type": "function", "function": map[string]string{"name": name, "arguments": "{}"}}}
			finish = "tool_calls"
		}
	}
	return respond(http.StatusOK, map[string]any{
		"id": "fake", "object": "chat.completion", "model": chat.Model,
		"choices": []any{map[string]any{"index": 0, "finish_reason": finish, "message": message}},
	})
}
