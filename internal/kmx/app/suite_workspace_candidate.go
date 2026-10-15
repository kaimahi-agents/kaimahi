package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
)

// Keep payload-bearing causes available to errors.Is without exposing API/model
// responses. This run-local diagnostic is neither recovery nor cleanup authority.
type suiteCandidateOutcome struct {
	phase, caseID, cleanupReason string
	effects, allGone             bool
	cause                        error
}

func (e *suiteCandidateOutcome) Unwrap() error { return e.cause }
func (e *suiteCandidateOutcome) Error() string {
	verdict := "native candidate evaluation outcome unknown"
	if !e.effects {
		verdict = "native evaluation refused before native changes"
	}
	if errors.Is(e.cause, errSuiteNativeCaseFailed) {
		verdict = "native candidate evaluation case failed"
	}
	cleanup := "candidate cleanup not required"
	if e.allGone {
		cleanup = "candidate cleanup completed"
	} else if e.effects {
		cleanup = "candidate cleanup incomplete; known resources may remain"
	}
	message := fmt.Sprintf("%s at %s; %s", verdict, e.phase, cleanup)
	if !e.allGone && e.cleanupReason != "" {
		message += "; " + e.cleanupReason
	}
	if e.caseID != "" {
		message += fmt.Sprintf("; case %q", e.caseID)
	}
	if errors.Is(e.cause, errSuitePassingExport) {
		message += "; receipt publication incomplete; output may exist"
	}
	return message
}

// Internal only: public evaluation and convenience callers remain canonical.
// One fresh guard covers this concrete frozen worker's pair, Tasks and cleanup.
// No local persistence, name-based recovery, selectable partial proof or retries.
func (a *App) evaluateSuiteWorkspaceCandidate(ctx context.Context, p *PreparedSuiteWorkspaceEvaluation) (err error) {
	outcome := &suiteCandidateOutcome{phase: "preflight"}
	guardRefusal := false
	defer func() {
		if err != nil && !guardRefusal {
			outcome.cause = errors.Join(err, ctx.Err())
			err = outcome
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	if p == nil || p.suite == nil || !suiteNativeOrkaInput(p.suite) || p.output == nil || p.started || a.Cfg == nil || a.Run == nil || a.Cfg.KubeContext != p.options.ToContext {
		return errSuiteNativeEvaluation
	}
	p.started = true
	cfg, runner := *a.Cfg, *a.Run
	runner.Context = ctx
	worker := *a
	worker.Cfg, worker.Run, worker.guarded = &cfg, &runner, false
	kubectl := depKubectl
	kubectl.fetchable = false
	if err := worker.preflight(kubectl); err != nil {
		return err
	}
	checkSource := func(checkCtx context.Context) error { return workspace.CheckCurrent(checkCtx, p.root, p.prepared) }
	if err := checkSource(ctx); err != nil {
		return err
	}
	runID, err := randomHex(16)
	if err != nil {
		return err
	}
	native, err := worker.prepareSuiteNativeCandidate(ctx, p.suite, p.options.ToNamespace, strings.TrimPrefix(p.options.Inference, "provider:"), runID)
	if err != nil {
		return err
	}
	if !validSuitePassingSubject(native.Subject) {
		return errSuitePassingReceipt
	}
	if err := coherentSuitePassingSubject(native.Subject, p.suite); err != nil {
		return err
	}
	if err := worker.preflightSuiteNativeResults(ctx, p.options.ToNamespace); err != nil {
		return err
	}
	checkOutput := func() error {
		if err := p.output.unchanged(); err != nil {
			return err
		}
		if _, err := p.output.root.Lstat(p.output.name); !os.IsNotExist(err) {
			return errSuitePassingExport
		}
		return nil
	}
	if err := checkOutput(); err != nil {
		return err
	}
	if p.judge != nil {
		count := 0
		for _, frame := range p.inventory {
			for _, assertion := range frame.Case.Assertions {
				if assertion.Type == "llmJudge" {
					count += 2
				}
			}
		}
		if _, err := fmt.Fprintf(worker.Err, "Judge preflight: %d independent requests over case inputs, attributed answers and authored rubrics; shares each run's deadline. Identity/version are user-declared, not provider attestation. Reasoning is discarded.\n", count); err != nil {
			return err
		}
	}
	outcome.phase = "guard"
	action := fmt.Sprintf("create fresh owned native candidate Provider and Agent, execute %d evaluation Tasks in %s, and delete this run's acknowledged owned UIDs and verify attributable children gone, including on cancellation", len(p.inventory), p.options.ToNamespace)
	if err := worker.guardOrkaMutation(ctx, CreateOptions{Name: native.Subject.PhysicalMember, Namespace: p.options.ToNamespace}, action); err != nil {
		guardRefusal = true
		return err
	}
	if err := checkSource(ctx); err != nil {
		return err
	}
	if err := worker.verifySuiteNativeInputs(ctx, native.Subject); err != nil {
		return err
	}
	if err := checkOutput(); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(worker.Err, "Task execution uses the selected ServiceAccount's full effective authority. v0.2.0 Task-read enforcement is unverified; v0.1.3 only authenticates result reads. Retain the namespaced Task-get grant. Discarding the token is not revocation. Candidate cleanup does not erase runtime/model logs or independently retained results."); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(worker.Out, "Workspace: %q; AgentSuite: %s\n", p.root, p.prepared.Descriptor.Digest); err != nil {
		return err
	}
	run, err := newSuiteCandidateRun(native, checkSource)
	if err != nil {
		return err
	}
	var placement *suiteNativePlacement
	observations := make([]suitePassingObservation, 0, len(p.inventory))
	executionErr := func() error {
		outcome.phase = "placement"
		var err error
		placement, err = worker.placeSuiteNativeCandidate(ctx, native, run)
		if err != nil {
			return err
		}
		observations, err = evaluateSuiteRuns(ctx, p.inventory, p.options.CaseTimeout, worker.Out, func(runCtx context.Context, frame suitePassingInventoryCase) (suitePassingObservation, error) {
			outcome.phase, outcome.caseID = "case execution", frame.Case.ID
			if err := checkSource(runCtx); err != nil {
				return suitePassingObservation{}, err
			}
			return worker.evaluateSuiteNativeCandidate(runCtx, placement, frame, p.options.ResultPort, p.judge, run)
		})
		if err != nil {
			return err
		}
		outcome.caseID = ""
		return nil
	}()
	outcome.effects = len(run.Resources()) > 0
	// Fresh live Runner context, not merely a detached parameter alongside a
	// canceled Runner. Frozen config survives caller mutation/cancellation.
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), suiteCandidateCleanupBudget)
	cleanupWorker := worker.withRunContext(cleanupCtx)
	result, cleanupErr := cleanupWorker.cleanupSuiteNativeCandidate(cleanupCtx, run)
	cleanupContextErr := cleanupCtx.Err() // Preserve deadline identity before releasing the context.
	cancel()
	outcome.allGone, outcome.cleanupReason = result.AllGone, result.Reason
	if cleanupErr != nil || !result.AllGone {
		outcome.phase = "cleanup"
		return errors.Join(executionErr, cleanupErr, cleanupContextErr, errSuiteCandidateCleanup)
	}
	if executionErr != nil {
		return executionErr
	}
	outcome.phase = "final source and shared inputs"
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := checkSource(ctx); err != nil {
		return err
	}
	// Deleted pair cannot be reverified; original shared inputs still must match.
	if err := worker.verifySuiteNativeInputs(ctx, native.Subject); err != nil {
		return err
	}
	if err := checkSource(ctx); err != nil {
		return err
	}
	outcome.phase = "receipt export"
	receipt, err := newSuitePassingCandidateReceipt(ctx, p.prepared, placement, observations, runID)
	if err != nil {
		return err
	}
	if err := checkSource(ctx); err != nil {
		return err
	}
	if err := p.output.publish(ctx, receipt, p.suite); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(worker.Out, "Passing native receipt: %q (unsigned observations, not authentication or a policy gate)\n", p.options.Out); err != nil {
		return errSuitePassingExport
	}
	return nil
}
