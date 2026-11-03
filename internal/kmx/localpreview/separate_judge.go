package localpreview

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
)

// JudgeTransport exposes the embedded runner's validated keyless local/fake
// route policy to a separate judge, without embedded Sessions or journals.
func JudgeTransport(selection modelaccess.Route) (baseURL string, transport http.RoundTripper, err error) {
	r, err := runnerRoute(selection)
	return r.baseURL, r.transport, err
}

// SeparateJudgeTransport guards a native runner's tool-free judge samples. It
// sends the fixed two-message, nonstreaming request the shared judge builds and
// nothing else; the caller's scorer still validates the reply. Failures latch for
// one invocation, so an unavailable or refused response cannot be resent.
// Reply lets the scorer's owner validate model and score shape before another send.
type SeparateJudgeTransport struct {
	Next       http.RoundTripper
	SafeErrors bool
	Check      func(...string) error
	Validate   func(context.Context) error
	Report     func(string) error
	Reply      func([]byte, string, string) error
	failure    error
}

// Err is the first latched failure, if any.
func (t *SeparateJudgeTransport) Err() error { return t.failure }

func (t *SeparateJudgeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return t.Sample(req, "", "")
}

func (t *SeparateJudgeTransport) Sample(req *http.Request, model, version string) (response *http.Response, err error) {
	if t.failure != nil {
		return nil, t.failure
	}
	defer func() {
		if err != nil {
			t.failure = err
		}
	}()
	ctx := req.Context()
	if err = t.Validate(ctx); err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(req.Body, MaxRequestBytes+1))
	req.Body.Close()
	if err != nil || len(raw) > MaxRequestBytes || !json.Valid(raw) {
		return nil, errRunnerExecution
	}
	var fields map[string]json.RawMessage
	var body struct {
		Model    string        `json:"model"`
		Stream   bool          `json:"stream"`
		Messages []chatMessage `json:"messages"`
	}
	if json.Unmarshal(raw, &fields) != nil || json.Unmarshal(raw, &body) != nil || body.Stream || len(body.Messages) != 2 || body.Messages[0].Role != "system" || body.Messages[1].Role != "user" || model != "" && body.Model != model {
		return nil, errRunnerExecution
	}
	for key := range fields {
		switch key {
		case "model", "max_completion_tokens", "stream", "messages":
		default:
			return nil, errRunnerExecution // never tools, functions or continuation fields
		}
	}
	if err = t.Check(body.Model, version, body.Messages[0].Content, body.Messages[1].Content, string(raw)); err != nil {
		return nil, err
	}
	if t.Report != nil {
		if err = t.Report("Judge started"); err != nil {
			return nil, err
		}
	}
	next := req.Clone(ctx)
	next.Body = io.NopCloser(bytes.NewReader(raw))
	next.GetBody = nil
	next.ContentLength = int64(len(raw))
	resp, err := t.Next.RoundTrip(next)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || t.SafeErrors {
			return nil, err
		}
		return nil, refuse(RefusedUnreachable, "the judge endpoint was unreachable", "check the endpoint; nothing was retried")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errRunnerExecution
	}
	captured, err := io.ReadAll(io.LimitReader(resp.Body, (64<<10)+1))
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil || len(captured) > 64<<10 {
		return nil, errRunnerExecution
	}
	if json.Valid(captured) {
		err = checkJSONContent(captured, t.Check)
	} else {
		err = t.Check(string(captured))
	}
	if err != nil {
		return nil, err
	}
	if t.Reply != nil {
		if err = t.Reply(captured, body.Model, version); err != nil {
			return nil, err
		}
	}
	if t.Report != nil {
		if err = t.Report("Judge finished"); err != nil {
			return nil, err
		}
	}
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(captured)), ContentLength: int64(len(captured)), Request: req}, nil
}

var _ http.RoundTripper = (*SeparateJudgeTransport)(nil)
