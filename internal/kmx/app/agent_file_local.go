package app

import (
	"context"
	"io"
	"net/http"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// NewLocalFileRunner journals fake and keyless local previews on embedded Sessions.
// Copilot agent or judge routes retain the direct, explicitly unjournaled runner
// until its credential-owning transport is integrated with the journal backend.
// Construction does not resolve paths, create files or access credentials.
func NewLocalFileRunner() agentpreview.Runner {
	return &localFileRunner{
		journaled: localpreview.NewRunner(localpreview.RunnerOptions{JournalDir: config.LocalPreviewJournalDir, Judge: localFilePreviewJudge, Tools: localtools.NewMCPHost}),
		direct:    NewDirectFileRunner(),
	}
}

type localFileRunner struct{ journaled, direct agentpreview.Runner }

func (r *localFileRunner) Run(ctx context.Context, request agentpreview.Request) (agentpreview.Outcome, error) {
	if request.Route.Provider == "copilot" || request.JudgeRoute != nil && request.JudgeRoute.Provider == "copilot" {
		if request.Agent != nil && len(request.Agent.Tools) > 0 || request.ToolBindings != "" {
			return agentpreview.Outcome{State: agentpreview.Failed}, agentpreview.ErrToolsUnavailable
		}
		return r.direct.Run(ctx, request)
	}
	return r.journaled.Run(ctx, request)
}

// This private factory consumes only the adapter's prevalidated fake/keyless
// local routes. Reuse the native two-sample evaluator without canonicalizing an
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
	return client.evaluate, nil
}
