package app

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
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
	checkedAccess := func(ctx context.Context, routes []modelaccess.Route) (modelaccess.PreviewAccess, error) {
		capability, err := access(ctx, routes)
		if capability != nil {
			capability = previewDisplayAccess{capability}
		}
		return capability, err
	}
	return localpreview.NewRunner(localpreview.RunnerOptions{JournalDir: config.LocalPreviewJournalDir, Judge: localFilePreviewJudge, JudgeReply: localFileJudgeReply, Access: checkedAccess, Tools: localtools.NewMCPHost})
}

// The App owns display normalization, while the provider alone knows the keys.
// Check those same views before Sessions retains any authored or returned text.
// This also covers local-agent replies when only the judge uses Copilot.
type previewDisplayAccess struct{ modelaccess.PreviewAccess }

func (a previewDisplayAccess) CheckContent(fields ...string) error {
	for _, field := range fields {
		views, _, ok := suiteDiagnosticViews(field, len(field))
		if !ok {
			return agentpreview.ErrCredentialContent
		}
		if err := a.PreviewAccess.CheckContent(views...); err != nil {
			return err
		}
	}
	return nil
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
