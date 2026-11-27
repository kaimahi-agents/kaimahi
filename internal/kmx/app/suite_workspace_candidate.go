package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

// Keep payload-bearing causes available to errors.Is without exposing API/model
// responses. This run-local diagnostic is neither recovery nor cleanup authority.
type suiteCandidateOutcome struct {
	phase, caseID, cleanupReason string
	effects, allGone             bool
	cause                        error
	runs                         *suiteRunSummary
}

func (e *suiteCandidateOutcome) Unwrap() error { return e.cause }
func (e *suiteCandidateOutcome) Error() string {
	verdict := "native candidate evaluation outcome unknown"
	if !e.effects {
		verdict = "native evaluation refused before native changes"
	}
	if !e.effects && errors.Is(e.cause, scaffold.ErrUnresolvedPlaceholder) {
		verdict = "native suite contains an unresolved model or endpoint placeholder; set concrete values, then rerun eval"
	}
	if errors.Is(e.cause, errSuiteCandidateCleanup) {
		verdict = "native candidate cleanup outcome unknown"
	} else if errors.Is(e.cause, errSuiteNativeCaseFailed) {
		verdict = "native candidate evaluation case failed"
		if e.runs != nil && e.runs.Aborted {
			verdict = "native candidate execution failed"
		} else if e.runs != nil && e.runs.ThresholdUnreachable {
			verdict = "native candidate evaluation stopped because the pass threshold is unreachable"
		}
	}
	var port *orkaResultPortError
	if errors.As(e.cause, &port) {
		verdict = port.Error()
	}
	message := fmt.Sprintf("%s at %s; %s", verdict, e.phase, e.cleanup())
	if e.runs != nil {
		message = e.runs.String() + "; " + message
	}
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

func (e *suiteCandidateOutcome) cleanup() string {
	if e.allGone {
		return "candidate cleanup completed"
	}
	if e.effects {
		return "candidate cleanup incomplete; known resources may remain"
	}
	return "candidate cleanup not required"
}

// Public Orka evaluation uses candidates; convenience callers stay canonical.
// One fresh guard covers this concrete frozen worker's pair, Tasks and cleanup.
// No local persistence, name-based recovery, selectable partial proof or retries.
func (a *App) evaluateSuiteWorkspaceCandidate(ctx context.Context, p *PreparedSuiteWorkspaceEvaluation) (err error) {
	outcome := &suiteCandidateOutcome{phase: "preflight"}
	guardRefusal, thresholdStopped := false, false
	defer func() {
		if err != nil && !guardRefusal {
			if ctx.Err() != nil && outcome.runs != nil && thresholdStopped {
				outcome.runs.ThresholdUnreachable = false
				outcome.runs.Aborted = true
			}
			outcome.cause = errors.Join(err, ctx.Err())
			err = outcome
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	if p == nil || p.suite == nil || !suiteNativeOrkaInput(p.suite) || p.output == nil || a.Cfg == nil || a.Run == nil || a.Cfg.KubeContext != p.options.ToContext {
		return errSuiteNativeEvaluation
	}
	if p.operation == nil || p.operation.Start(ctx) != nil {
		return errSuiteNativeEvaluation
	}
	for _, frame := range p.inventory {
		if frame.Metadata.Runs > 1 {
			stop := "cancellation can stop earlier"
			if p.options.FailFast {
				stop = "--fail-fast or cancellation can stop earlier"
			}
			if _, err := fmt.Fprintf(a.Err, "Run preflight: %d fresh Tasks, %s per run, %s total execution budget (upper bound; excludes placement; %s).\n", len(p.inventory), p.options.CaseTimeout, time.Duration(len(p.inventory))*p.options.CaseTimeout, stop); err != nil {
				return err
			}
			break
		}
	}
	cfg, runner := *a.Cfg, *a.Run
	runner.Context = ctx
	worker := *a
	worker.Cfg, worker.Run, worker.guarded = &cfg, &runner, false
	if err := worker.preflightSuiteWorkspaceKubectl(); err != nil {
		return err
	}
	if err := worker.requireRemoteSuiteWorkspaceContext(ctx); err != nil {
		return err
	}
	checkSource := p.checkCurrent
	if err := checkSource(ctx); err != nil {
		return err
	}
	runID := p.candidateRun
	if runID == "" {
		runID, err = randomHex(16)
	}
	if err != nil {
		return err
	}
	native, err := worker.prepareSuiteNativeCandidate(ctx, p.suite, p.options.ToNamespace, strings.TrimPrefix(p.options.Inference, "provider:"), runID)
	if err != nil {
		return err
	}
	worker.reportCandidateStartup(ctx, native.Subject.Target)
	recovery := worker.workspaceTestCommand("cleanup", runID, "--to-context", cfg.KubeContext, "--to-namespace", native.Subject.Target.Namespace)
	if _, err := fmt.Fprintf(worker.Err, "Current candidate: run=%s context=%q clusterUID=%s namespace=%q namespaceUID=%s member=%s suite=%s\nRecovery: %s\n", runID, cfg.KubeContext, native.Subject.Target.ClusterUID, native.Subject.Target.Namespace, native.Subject.Target.NamespaceUID, native.Subject.PhysicalMember, native.Subject.SuiteDigest, recovery); err != nil {
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
	checkOutput := func() error { return p.output.absent(ctx) }
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
		requestCount := fmt.Sprintf("%d independent requests", count)
		if p.options.FailFast {
			requestCount = fmt.Sprintf("up to %d independent requests", count)
		}
		if _, err := fmt.Fprintf(worker.Err, "Judge preflight: %s over case inputs, attributed answers and authored rubrics; shares each run's deadline. Identity/version are user-declared, not provider attestation. Non-pass reasoning is diagnostic-only; --show-content opts into sanitized bounded content on stderr.\n", requestCount); err != nil {
			return err
		}
	}
	outcome.phase = "guard"
	executionCount := fmt.Sprintf("execute %d evaluation Tasks", len(p.inventory))
	if p.options.FailFast {
		executionCount = fmt.Sprintf("execute up to %d evaluation Tasks, stopping only when a repeated pass threshold is unreachable", len(p.inventory))
	}
	action := fmt.Sprintf("create fresh owned native candidate Provider and Agent, %s in %s, and delete this run's acknowledged owned UIDs and verify attributable children gone, including on cancellation", executionCount, p.options.ToNamespace)
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
	if _, err := fmt.Fprintln(worker.Err, "Task execution uses the selected ServiceAccount's full effective authority. v0.3.0 Task-read enforcement is unverified; v0.1.3 only authenticates result reads. Retain the namespaced Task-get grant. Discarding the token is not revocation. Candidate cleanup does not erase runtime/model logs or independently retained results."); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(worker.Out, "Workspace: %q; AgentSuite: %s\n", p.root, p.suiteDigest()); err != nil {
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
		outcome.runs = &p.runs
		observations, err = evaluateSuiteRunsReportingMode(ctx, p.inventory, p.options.CaseTimeout, worker.Out, func(runCtx context.Context, frame suitePassingInventoryCase) (suitePassingObservation, error) {
			outcome.phase, outcome.caseID = "case execution", suiteDiagnosticIdentity(frame.Case.ID, []string{p.diagnosticKey})
			if err := checkSource(runCtx); err != nil {
				return suitePassingObservation{}, err
			}
			return worker.evaluateSuiteNativeCandidate(runCtx, placement, frame, p.options.ResultPort, p.judge, run)
		}, &p.runs, p.options.FailFast)
		thresholdStopped = p.options.FailFast && p.runs.ThresholdUnreachable
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
	p.cleanup = outcome.cleanup()
	if cleanupErr != nil || !result.AllGone {
		if outcome.runs != nil {
			outcome.runs.ThresholdUnreachable = false
		}
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
	receipt, err := newSuitePassingCandidateReceiptFrozen(ctx, p.suite, p.sourceFingerprint(), placement, observations, runID)
	if err != nil {
		return err
	}
	if err := checkSource(ctx); err != nil {
		return err
	}
	if err := p.publishPassingReceipt(ctx, receipt); err != nil {
		return err
	}
	return p.reportPassingReceipt(worker.Out)
}
