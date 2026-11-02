package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/copilotd"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess/copilot"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// errNativeJudge marks a judge that could not run, so recovery wording names the
// separate judge rather than the native host or its observed model.
var errNativeJudge = errors.New("separate judge unavailable")

const nativeJudgeNotice = "Agent execution uses the native copilotd host; llmJudge assertions use a separate tool-free judge request, not the host."

// nativeJudge scores a native answer with the shared evaluator over a guarded
// HTTP transport, never a host turn. The default route is the agent's route.
type nativeJudge struct {
	check    func(...string) error
	validate func(context.Context) error
	evaluate agentruntime.EvaluationJudge
	guard    *localpreview.SeparateJudgeTransport
}

func (j *nativeJudge) CheckContent(fields ...string) error { return j.check(fields...) }
func (j *nativeJudge) Validate(ctx context.Context) error  { return j.validate(ctx) }
func (j *nativeJudge) Evaluate(ctx context.Context, c agentruntime.EvaluationCase, e agentruntime.EvaluationEvidence) ([]agentruntime.EvaluationAssertionResult, error) {
	rows := j.evaluate(ctx, c, e)
	if err := j.guard.Err(); err != nil {
		return nil, nativeJudgeFailure(err)
	}
	return rows, nil
}

// newNativeJudge adapts the same provider the host authenticates with: access is
// that provider's PreviewClient. It is consulted only for a Copilot judge route.
// A nonnil capability returned with an error is content-check-only.
func newNativeJudge(access func(context.Context, []modelaccess.Route) (modelaccess.PreviewAccess, error)) func(context.Context, agentpreview.Request, func(...string) error) (copilotd.JudgeAccess, error) {
	return func(ctx context.Context, req agentpreview.Request, hostCheck func(...string) error) (copilotd.JudgeAccess, error) {
		selection := req.Route
		if req.JudgeRoute != nil {
			selection = *req.JudgeRoute
		}
		j := &nativeJudge{check: hostCheck, validate: func(ctx context.Context) error { return ctx.Err() }}
		var base string
		var transport http.RoundTripper
		safeErrors := false
		if selection.Provider == "copilot" {
			if selection.Validate() != nil || access == nil {
				return j, fmt.Errorf("%w: %w", errNativeJudge, agentpreview.ErrModelUnavailable)
			}
			capability, err := access(ctx, []modelaccess.Route{selection})
			if capability != nil {
				// Both credentials are checked with the same display-normalized views.
				judgeCheck := nativeDisplayCheck(capability.CheckContent)
				j.check = func(fields ...string) error {
					if err := hostCheck(fields...); err != nil {
						return err
					}
					return judgeCheck(fields...)
				}
				j.validate = capability.Validate
			}
			if err == nil && capability != nil {
				transport, err = capability.Client(selection)
				if err == nil && transport == nil {
					err = agentpreview.ErrModelUnavailable
				}
			} else if err == nil {
				err = agentpreview.ErrModelUnavailable
			}
			if err != nil {
				return j, fmt.Errorf("%w: %w", errNativeJudge, err)
			}
			base, safeErrors = selection.Endpoint, true
		} else {
			var err error
			if base, transport, err = localpreview.JudgeTransport(selection); err != nil {
				return j, fmt.Errorf("%w: %w", errNativeJudge, err)
			}
		}
		j.guard = &localpreview.SeparateJudgeTransport{Next: transport, SafeErrors: safeErrors, Check: j.check,
			Report: func(line string) error { return req.Report("%s", line) },
			Validate: func(ctx context.Context) error {
				if err := j.validate(ctx); err != nil {
					return err
				}
				if req.ValidateSource != nil {
					if err := req.ValidateSource(ctx); err != nil {
						if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
							return err
						}
						return errors.Join(agentpreview.ErrSourceChanged, err)
					}
				}
				return nil
			}}
		cases := make([]agentruntime.EvaluationCase, len(req.Examples))
		for i, ex := range req.Examples {
			cases[i] = agentruntime.EvaluationCase{ID: ex.ID, Input: ex.Input, Assertions: ex.Assertions}
		}
		evaluate, err := localFilePreviewJudge(base, "", j.guard, cases)
		if err != nil {
			return j, fmt.Errorf("%w: %w", errNativeJudge, err)
		}
		j.evaluate = evaluate
		return j, nil
	}
}

// nativeJudgeFailure leaves cancellation, deadlines and content/source refusals
// to their existing handling; everything else means the judge itself could not run.
func nativeJudgeFailure(err error) error {
	for _, keep := range []error{context.Canceled, context.DeadlineExceeded, copilot.ErrCredentialContent, agentpreview.ErrCredentialContent, agentpreview.ErrUnsafeToolContent, agentpreview.ErrDiagnosticsFailed, agentpreview.ErrSourceChanged} {
		if errors.Is(err, keep) {
			return err
		}
	}
	return fmt.Errorf("%w: %w", errNativeJudge, err)
}

// nativeJudgeReason is fixed text: never raw provider or transport diagnostics.
func nativeJudgeReason(cause error) string {
	for _, signIn := range []error{copilot.ErrAuthRequired, copilot.ErrAuthRevoked, copilot.ErrKeychain, copilot.ErrHostToken} {
		if errors.Is(cause, signIn) {
			return "The separate judge could not sign in to Copilot, so the result is unknown. Sign in with gh auth login --hostname github.com. Nothing was retried."
		}
	}
	return "The separate judge could not run, so the result is unknown. Check the selected judge model and endpoint; for the default Copilot route, run `kmx model list --provider copilot` and select with --model an exact model available to both the native host and the HTTP judge. Nothing was substituted or retried."
}
