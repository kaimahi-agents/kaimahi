package app

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess/copilot"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// NewLocalFileRunner journals file previews on embedded Sessions, including
// Copilot's provider-owned credential preflight and one Session per judge sample.
// Construction does not resolve paths, create files or access credentials.
func NewLocalFileRunner() agentpreview.Runner {
	return newLocalFileRunner(copilot.New(nil).PreviewClient)
}

func newLocalFileRunner(access func(context.Context, []modelaccess.Route) (modelaccess.PreviewAccess, error)) agentpreview.Runner {
	return localpreview.NewRunner(localpreview.RunnerOptions{JournalDir: config.LocalPreviewJournalDir, Judge: localFilePreviewJudge, JudgeReply: localFileJudgeReply, Access: access})
}

// This private factory consumes only the adapter's prevalidated model routes
// and provider-owned transports. Reuse the native two-sample evaluator without canonicalizing an
// exact localhost/custom-path selection. Standalone LocalPreviewJudge and suite
// endpoint policy remain unchanged; no credential facility is bypassed here.
func localFilePreviewJudge(base, keyEnv string, transport http.RoundTripper, cases []agentruntime.EvaluationCase) (agentruntime.EvaluationJudge, error) {
	if keyEnv != "" || transport == nil {
		return nil, agentpreview.ErrModelUnavailable
	}
	for _, c := range cases {
		if agentruntime.ValidateEvaluationAssertions(c.Assertions) != nil {
			return nil, agentpreview.ErrModelUnavailable
		}
	}
	client := &evaluationJudgeClient{baseURL: base, out: io.Discard, httpClient: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	if journaled, ok := transport.(interface {
		Sample(*http.Request, string, string) (*http.Response, error)
	}); ok {
		client.sampleRoundTrip = journaled.Sample
	}
	return client.evaluate, nil
}

func localFileJudgeReply(body []byte, model, version string) error {
	if _, _, _, ok := parseEvaluationJudgeResponse(body, model, version, ""); !ok {
		return errors.New("preview judge response malformed")
	}
	return nil
}
