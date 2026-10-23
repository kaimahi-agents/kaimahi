package localpreview

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/agent-primitives/sessions/api"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
)

// runnerJudgeTransport puts each native judge sample through a fresh tool-free
// Session without changing the scorer, wire policy or version-qualified identity.
// The failure latch is invocation-local: even a scorer that asks for another
// sample after an unavailable response cannot send again.
type runnerJudgeTransport struct {
	next       http.RoundTripper
	safeErrors bool
	request    agentpreview.Request
	failure    *error
	executor   *Executor
	check      func(...string) error
	validate   func(context.Context) error
	reply      func([]byte, string, string) error
	samples    int
}

func (t *runnerJudgeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return t.Sample(req, "", "")
}

// Sample is consumed only by the file-preview native judge adapter. The native
// evaluator supplies its authored version for validation before journaling.
func (t *runnerJudgeTransport) Sample(req *http.Request, model, version string) (response *http.Response, err error) {
	if *t.failure != nil {
		return nil, *t.failure
	}
	defer func() {
		if err != nil {
			*t.failure = err
		}
	}()
	ctx := req.Context()
	if err = t.validate(ctx); err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(req.Body, MaxRequestBytes+1))
	req.Body.Close()
	if err != nil || len(raw) > MaxRequestBytes || !json.Valid(raw) {
		return nil, errRunnerExecution
	}
	var body struct {
		Model    string        `json:"model"`
		Stream   bool          `json:"stream"`
		Messages []chatMessage `json:"messages"`
	}
	if json.Unmarshal(raw, &body) != nil || body.Stream || len(body.Messages) != 2 || body.Messages[0].Role != "system" || body.Messages[1].Role != "user" || model != "" && body.Model != model {
		return nil, errRunnerExecution
	}
	if err = t.check(body.Model, version, body.Messages[0].Content, body.Messages[1].Content); err != nil {
		return nil, err
	}
	if err = t.request.Report("Judge started"); err != nil {
		return nil, err
	}
	if t.executor == nil || *t.executor == nil {
		return nil, errRunnerExecution
	}
	t.samples++
	var captured []byte
	var answer string
	turn := Turn{Judge: true, AgentID: t.request.Agent.Identity.ID, Revision: t.request.Agent.Revision, ExampleID: fmt.Sprintf("judge-sample-%d", t.samples), Model: body.Model, Instructions: body.Messages[0].Content, Input: body.Messages[1].Content}
	turn.Call = func(callCtx context.Context, call api.ModelRequest) (api.ModelResponse, error) {
		if call.Model != turn.Model || len(call.Tools) != 0 || len(call.Params) != 0 {
			return api.ModelResponse{}, errRunnerExecution
		}
		if err := t.validate(callCtx); err != nil {
			return api.ModelResponse{}, err
		}
		next := req.Clone(callCtx)
		next.Body = io.NopCloser(bytes.NewReader(raw))
		next.GetBody = nil
		next.ContentLength = int64(len(raw))
		resp, err := t.next.RoundTrip(next)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return api.ModelResponse{}, context.Canceled
			}
			if errors.Is(err, context.DeadlineExceeded) {
				return api.ModelResponse{}, context.DeadlineExceeded
			}
			if t.safeErrors {
				return api.ModelResponse{}, err
			}
			return api.ModelResponse{}, refuse(RefusedUnreachable, "the judge endpoint was unreachable", "check the endpoint; nothing was retried")
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return api.ModelResponse{}, errRunnerExecution
		}
		captured, err = io.ReadAll(io.LimitReader(resp.Body, (64<<10)+1))
		if err != nil || len(captured) > 64<<10 {
			return api.ModelResponse{}, errRunnerExecution
		}
		if err = checkJSONContent(captured, t.check); err != nil {
			return api.ModelResponse{}, err
		}
		if t.reply != nil {
			if err = t.reply(captured, body.Model, version); err != nil {
				return api.ModelResponse{}, err
			}
		}
		var envelope struct {
			Model string `json:"model"`
		}
		if json.Unmarshal(captured, &envelope) != nil {
			return api.ModelResponse{}, errRunnerExecution
		}
		answer, err = parseReply(captured, envelope.Model)
		if err != nil {
			return api.ModelResponse{}, err
		}
		if t.reply == nil && envelope.Model != body.Model {
			return api.ModelResponse{}, errRunnerExecution
		}
		return api.ModelResponse{Message: *api.TextMessage("assistant", answer)}, nil
	}
	execution, err := (*t.executor).Execute(ctx, turn)
	if err != nil {
		return nil, runnerJudgeContextError(err)
	}
	if !execution.Journaled || execution.SessionUID == "" || execution.Answer != answer || captured == nil {
		return nil, errRunnerExecution
	}
	if err = t.request.Report("Judge finished"); err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(captured)), ContentLength: int64(len(captured)), Request: req}, nil
}

func runnerJudgeContextError(err error) error {
	if interrupted(err) {
		return runnerContextError(err)
	}
	return err
}

// Inspect decoded text as well as raw JSON: a response may escape a credential
// in content, metadata or the nested JSON rationale. Depth is bounded by the
// already bounded response; excessive nesting refuses instead of skipping checks.
func checkJSONContent(raw []byte, check func(...string) error) error {
	var walk func(any, int) error
	walk = func(value any, depth int) error {
		if depth > 32 {
			return errRunnerExecution
		}
		switch v := value.(type) {
		case string:
			if err := check(v); err != nil {
				return err
			}
			var nested any
			if json.Unmarshal([]byte(v), &nested) == nil {
				return walk(nested, depth+1)
			}
		case []any:
			for _, item := range v {
				if err := walk(item, depth+1); err != nil {
					return err
				}
			}
		case map[string]any:
			for key, item := range v {
				if err := check(key); err != nil {
					return err
				}
				if err := walk(item, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := check(string(raw)); err != nil {
		return err
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return errRunnerExecution
	}
	return walk(value, 0)
}

var _ http.RoundTripper = (*runnerJudgeTransport)(nil)
