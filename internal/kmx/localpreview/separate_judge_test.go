package localpreview

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
)

type sendCounter struct {
	sends atomic.Int64
	reply func(*http.Request) (*http.Response, error)
	last  string
}

func (s *sendCounter) RoundTrip(r *http.Request) (*http.Response, error) {
	s.sends.Add(1)
	raw, _ := io.ReadAll(r.Body)
	s.last = string(raw)
	return s.reply(r)
}

func judgeReply(status int, body string) func(*http.Request) (*http.Response, error) {
	return func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	}
}

const judgeBody = `{"model":"judge","max_completion_tokens":1024,"stream":false,"messages":[{"role":"system","content":"policy"},{"role":"user","content":"{\"answer\":\"hello\"}"}]}`

func separateJudge(next http.RoundTripper, check func(...string) error) *SeparateJudgeTransport {
	if check == nil {
		check = func(...string) error { return nil }
	}
	return &SeparateJudgeTransport{Next: next, Check: check, Validate: func(ctx context.Context) error { return ctx.Err() }}
}

func judgeRequest(t *testing.T, ctx context.Context, body string) *http.Request {
	t.Helper()
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1/chat/completions", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSeparateJudgeSendsOneToolFreeRequestAndReturnsBody(t *testing.T) {
	next := &sendCounter{reply: judgeReply(200, `{"model":"judge"}`)}
	resp, err := separateJudge(next, nil).Sample(judgeRequest(t, testContext(t), judgeBody), "judge", "")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	if string(raw) != `{"model":"judge"}` || next.sends.Load() != 1 || next.last != judgeBody {
		t.Fatalf("body=%s sends=%d sent=%s", raw, next.sends.Load(), next.last)
	}
}

func TestSeparateJudgeRefusesAnythingButTheFixedToolFreeShape(t *testing.T) {
	for name, body := range map[string]string{
		"tools":     strings.Replace(judgeBody, `"stream":false`, `"stream":false,"tools":[]`, 1),
		"streaming": strings.Replace(judgeBody, `"stream":false`, `"stream":true`, 1),
		"messages":  strings.Replace(judgeBody, `{"role":"user"`, `{"role":"assistant","content":"x"},{"role":"user"`, 1),
		"model":     strings.Replace(judgeBody, `"judge"`, `"other"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			next := &sendCounter{reply: judgeReply(200, `{}`)}
			judge := separateJudge(next, nil)
			if _, err := judge.Sample(judgeRequest(t, testContext(t), body), "judge", ""); err == nil || next.sends.Load() != 0 {
				t.Fatalf("err=%v sends=%d", err, next.sends.Load())
			}
		})
	}
}

func TestSeparateJudgeLatchesUnavailableAndKeepsContextCauses(t *testing.T) {
	for name, tc := range map[string]struct {
		reply func(*http.Request) (*http.Response, error)
		is    error
	}{
		"status":    {judgeReply(503, `{}`), nil},
		"transport": {func(*http.Request) (*http.Response, error) { return nil, errors.New("PRIVATE RAW TRANSPORT") }, nil},
		"deadline":  {func(*http.Request) (*http.Response, error) { return nil, context.DeadlineExceeded }, context.DeadlineExceeded},
	} {
		t.Run(name, func(t *testing.T) {
			next := &sendCounter{reply: tc.reply}
			judge := separateJudge(next, nil)
			_, first := judge.Sample(judgeRequest(t, testContext(t), judgeBody), "judge", "")
			_, second := judge.Sample(judgeRequest(t, testContext(t), judgeBody), "judge", "")
			if first == nil || second == nil || next.sends.Load() != 1 || judge.Err() == nil || strings.Contains(first.Error(), "PRIVATE") {
				t.Fatalf("first=%v second=%v sends=%d", first, second, next.sends.Load())
			}
			if tc.is != nil && (!errors.Is(first, tc.is) || !errors.Is(judge.Err(), tc.is)) {
				t.Fatalf("lost cause: %v", first)
			}
		})
	}
}

func TestSeparateJudgeValidatesBeforeEverySend(t *testing.T) {
	next := &sendCounter{reply: judgeReply(200, `{}`)}
	judge := separateJudge(next, nil)
	stale := errors.New("provider invalid")
	judge.Validate = func(context.Context) error { return stale }
	if _, err := judge.Sample(judgeRequest(t, testContext(t), judgeBody), "judge", ""); !errors.Is(err, stale) || next.sends.Load() != 0 {
		t.Fatalf("err=%v sends=%d", err, next.sends.Load())
	}
}

func TestSeparateJudgeChecksOutgoingAndReturnedContent(t *testing.T) {
	refused := errors.New("credential")
	check := func(fields ...string) error {
		for _, f := range fields {
			if strings.Contains(f, "SECRET") {
				return refused
			}
		}
		return nil
	}
	next := &sendCounter{reply: judgeReply(200, `{"choices":[{"message":{"content":"{\"reasoning\":\"echo S\\u0045CRET\"}"}}]}`)}
	judge := separateJudge(next, check)
	if _, err := judge.Sample(judgeRequest(t, testContext(t), strings.Replace(judgeBody, "hello", "SECRET", 1)), "judge", ""); !errors.Is(err, refused) || next.sends.Load() != 0 {
		t.Fatalf("outgoing err=%v sends=%d", err, next.sends.Load())
	}
	judge = separateJudge(next, check)
	if _, err := judge.Sample(judgeRequest(t, testContext(t), judgeBody), "judge", ""); !errors.Is(err, refused) {
		t.Fatalf("returned content accepted: %v", err)
	}
	if _, err := judge.Sample(judgeRequest(t, testContext(t), judgeBody), "judge", ""); !errors.Is(err, refused) || next.sends.Load() != 1 {
		t.Fatalf("resent after refused response: %v sends=%d", err, next.sends.Load())
	}
}

// A reply the scorer cannot parse is the scorer's unknown result, not a transport failure.
func TestSeparateJudgeLeavesMalformedReplyToScorer(t *testing.T) {
	next := &sendCounter{reply: judgeReply(200, `not json`)}
	judge := separateJudge(next, nil)
	for i := 0; i < 2; i++ {
		if _, err := judge.Sample(judgeRequest(t, testContext(t), judgeBody), "judge", ""); err != nil {
			t.Fatal(err)
		}
	}
	if judge.Err() != nil || next.sends.Load() != 2 {
		t.Fatalf("err=%v sends=%d", judge.Err(), next.sends.Load())
	}
}

func TestJudgeTransportSharesEmbeddedRoutePolicy(t *testing.T) {
	base, transport, err := JudgeTransport(modelaccess.Route{Provider: "fake", Model: FakeModel, Auth: "none"})
	if err != nil || transport == nil {
		t.Fatalf("fake route: %v", err)
	}
	for _, route := range []modelaccess.Route{
		{Provider: "compatible", Model: "m", Endpoint: "https://example.com/v1", Auth: "none"},
		{Provider: "copilot", Model: "m", Endpoint: modelaccess.CopilotEndpoint, Auth: "copilot"},
	} {
		if _, _, err := JudgeTransport(route); err == nil {
			t.Fatalf("route accepted: %+v", route)
		}
	}
	base, transport, err = JudgeTransport(modelaccess.Route{Provider: "compatible", Model: "m", Endpoint: "http://127.0.0.1:9/v1/", Auth: "none"})
	if err != nil || base != "http://127.0.0.1:9/v1" || transport == nil {
		t.Fatalf("local route: %q %v", base, err)
	}
}
