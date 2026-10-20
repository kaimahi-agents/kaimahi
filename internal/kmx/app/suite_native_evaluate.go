package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

var errSuiteNativeEvaluation = errors.New("native evaluation outcome unknown; resources may remain; no retry or cleanup")
var errSuiteNativeRefused = errors.New("native evaluation refused before native changes")
var errSuiteNativeCaseFailed = errors.New("native evaluation case failed; resources may remain; no retry or cleanup")

func validateSuiteNativeTask(raw []byte, desired map[string]any) error {
	var observed map[string]any
	if json.Unmarshal(raw, &observed) != nil || observed["apiVersion"] != desired["apiVersion"] || observed["kind"] != "Task" {
		return errSuiteNativeEvaluation
	}
	meta, _ := observed["metadata"].(map[string]any)
	wanted := desired["metadata"].(map[string]any)
	if meta["name"] != wanted["name"] || meta["namespace"] != wanted["namespace"] || meta["deletionTimestamp"] != nil {
		return errSuiteNativeEvaluation
	}
	if ann, ok := wanted["annotations"].(map[string]any); ok && ann[suiteCandidateRunMarker] != nil {
		actual, _ := meta["annotations"].(map[string]any)
		for _, key := range []string{orkaBundleMarker, orkaPortableMarker, orkaRenderedMarker} {
			if _, present := actual[key]; present {
				return errSuiteNativeEvaluation
			}
		}
		if !reflect.DeepEqual(suiteOnlyMarkers(actual), suiteOnlyMarkers(ann)) {
			return errSuiteNativeEvaluation
		}
	}
	frame, ok := observed["spec"].(map[string]any)
	if !ok {
		return errSuiteNativeEvaluation
	}
	// Closed v0.2.0 top-level schema defaults affect scheduling, not authored
	// model behavior. Nested execution/retry/session/AI defaults are not accepted.
	for key, value := range map[string]any{"priority": float64(500), "concurrencyPolicy": "Forbid", "failedRunsHistoryLimit": float64(1), "successfulRunsHistoryLimit": float64(3), "startingDeadlineSeconds": float64(100)} {
		if actual, exists := frame[key]; exists {
			if actual != value {
				return errSuiteNativeEvaluation
			}
			delete(frame, key)
		}
	}
	if !reflect.DeepEqual(frame, desired["spec"]) {
		return errSuiteNativeEvaluation
	}
	return nil
}

// Drop payload-bearing diagnostics, not the phase or cancellation identity.
// Entering placement is ambiguous even if the first write was not observed.
func suiteEvaluationError(ctx context.Context, err error, mayHaveChanged bool) error {
	cause := ctx.Err()
	if cause == nil {
		for _, identity := range []error{context.Canceled, context.DeadlineExceeded} {
			if errors.Is(err, identity) {
				cause = identity
				break
			}
		}
	}
	if errors.Is(err, workspace.ErrMultiMemberUnsupported) {
		return errors.Join(workspace.ErrMultiMemberUnsupported, cause)
	}
	if !mayHaveChanged && errors.Is(err, scaffold.ErrUnresolvedPlaceholder) {
		return errors.Join(errors.New("native suite contains an unresolved model or endpoint placeholder; set concrete values, then rerun eval"), cause)
	}
	var port *orkaResultPortError
	if errors.As(err, &port) {
		return errors.Join(port, cause)
	}
	var placement *suiteNativeReuseRefusal
	if errors.As(err, &placement) {
		return errors.Join(placement, cause)
	}
	var diagnostic *suiteDiagnosticError
	if errors.As(err, &diagnostic) {
		cause = errors.Join(cause, &suiteDiagnosticError{class: diagnostic.class})
	}
	classified := errSuiteNativeRefused
	if mayHaveChanged {
		classified = errSuiteNativeEvaluation
	}
	if errors.Is(err, errSuiteNativeCaseFailed) {
		classified = errSuiteNativeCaseFailed
	}
	if errors.Is(err, errSuitePassingExport) {
		classified = errSuitePassingExport
	}
	return errors.Join(classified, cause)
}

func (a *App) evaluateSuiteNativeCandidate(ctx context.Context, placement *suiteNativePlacement, frame suitePassingInventoryCase, port string, judge agentruntime.EvaluationJudge, run *suiteCandidateRun) (suitePassingObservation, error) {
	if run == nil {
		return suitePassingObservation{}, errSuiteNativeEvaluation
	}
	return a.evaluateSuiteNativeTracked(ctx, placement, frame, port, judge, run)
}

// The caller supplies a bounded per-run context and has consented to placement
// and the full ordered inventory. Only the actual native observation is credited.
func (a *App) evaluateSuiteNative(ctx context.Context, placement *suiteNativePlacement, frame suitePassingInventoryCase, port string, judge agentruntime.EvaluationJudge) (suitePassingObservation, error) {
	return a.evaluateSuiteNativeTracked(ctx, placement, frame, port, judge, nil)
}

func (a *App) evaluateSuiteNativeTracked(ctx context.Context, placement *suiteNativePlacement, frame suitePassingInventoryCase, port string, judge agentruntime.EvaluationJudge, run *suiteCandidateRun) (observation suitePassingObservation, err error) {
	observation = suitePassingObservation{CaseDigest: frame.Metadata.CaseDigest, InputDigest: frame.Metadata.InputDigest, EvaluationReceipt: agentruntime.EvaluationReceipt{CaseID: frame.Case.ID, Verdict: agentruntime.EvaluationUnknown}}
	var answer string
	attributed := false
	defer func() {
		diagnostic := observation
		if !attributed || ctx.Err() != nil {
			if answer != "" {
				recordSuiteFailureClass(ctx, "attribution-unverified")
			}
			if ctx.Err() != nil {
				recordSuiteFailureClass(ctx, suiteFailureClass(ctx.Err()))
			}
			diagnostic.AnswerSHA256, diagnostic.Assertions = "", nil
			answer = ""
		}
		captureSuiteFailureContext(ctx, frame, &diagnostic, answer, err)
		observation.failure = diagnostic.failure
		if err != nil {
			if run != nil {
				count, _ := frame.Case.RunPolicy()
				// Only a fully attributed assertion failure in a declared repeat
				// group may continue. Task failures and unknowns still seal the run.
				countable := count > 1 && ctx.Err() == nil && errors.Is(err, errSuiteNativeCaseFailed) && suiteRunComplete(observation, frame) && suiteRunVerdict(observation.Assertions) == agentruntime.EvaluationFail
				if !countable {
					run.stopped = true
				}
			}
			err = suiteEvaluationError(ctx, err, true)
		}
	}()
	if err = ctx.Err(); err != nil {
		return observation, err
	}
	if _, bounded := ctx.Deadline(); !bounded {
		return observation, errSuiteNativeEvaluation
	}
	worker := a.withRunContext(ctx)
	verify := func(checkCtx context.Context) error {
		if run != nil {
			if !run.ownsPlacement(placement) {
				return errSuiteCandidateRun
			}
			if err := run.check(checkCtx, worker); err != nil {
				return err
			}
		}
		if err := worker.verifySuiteNative(checkCtx, placement); err != nil {
			return err
		}
		if run != nil {
			if err := run.checkSource(checkCtx); err != nil {
				return err
			}
			return checkCtx.Err()
		}
		return nil
	}
	if err = verify(ctx); err != nil {
		return observation, err
	}
	suffix, err := randomHex(8)
	if err != nil {
		return observation, err
	}
	subject := placement.Subject
	ns := subject.Target.Namespace
	prefix := subject.Member
	if run != nil {
		prefix = subject.PhysicalMember
	}
	name := strings.TrimRight(prefix[:min(len(prefix), 40)], "-.") + "-eval-" + suffix
	observation.TaskName = name
	task := map[string]any{
		"apiVersion": "core.orka.ai/v1alpha1", "kind": "Task",
		"metadata": map[string]any{"name": name, "namespace": ns},
		"spec":     map[string]any{"type": "ai", "prompt": frame.Case.Input, "agentRef": map[string]any{"name": subject.physicalMember(), "namespace": ns}, "resources": map[string]any{}},
	}
	if run != nil {
		addOrkaMarker(task, suiteNativeOwner(subject).markers())
	}
	quiet := *worker
	quiet.Err = io.Discard
	session, err := quiet.openOrkaResultSession(ctx, CreateOptions{Namespace: ns, ResultServiceAccount: orkaResultAccount, ResultPort: port})
	if err != nil {
		return observation, err
	}
	defer session.close()
	caseCtx := session.ctx
	if err = session.probe(caseCtx, ns, name); err != nil {
		return observation, err
	}
	if err = worker.orkaAbsent(caseCtx, ns, task); err != nil {
		return observation, err
	}
	body, err := json.Marshal(task)
	if err != nil {
		return observation, err
	}
	if run != nil {
		if err = verify(caseCtx); err != nil {
			return observation, err
		}
	}
	admitted, err := worker.orkaCapture(caseCtx, body, "-n", ns, "create", "--dry-run=server", "--validate=strict", "-f", "-", "-o", "json")
	if err != nil {
		return observation, err
	}
	if err = validateSuiteNativeTask(admitted, task); err != nil {
		return observation, err
	}
	if err = verify(caseCtx); err != nil {
		return observation, err
	}
	if run != nil {
		if err = run.intent(caseCtx, task); err != nil {
			return observation, err
		}
	}
	id, created, err := worker.createOrkaObjectObserved(caseCtx, ns, task)
	if err != nil {
		return observation, err
	}
	if run != nil {
		observation.TaskUID = id.UID
		if err = run.acknowledge(caseCtx, id, created); err != nil {
			return observation, err
		}
	}
	if err = validateSuiteNativeTask(created, task); err != nil {
		return observation, err
	}
	observation.TaskUID = id.UID
	// The installed Task CRD has a status subresource: a spec change bumps
	// generation. Existing UID/generation polls bind this CREATE spec frame.
	answer, err = worker.waitOrkaTaskResult(caseCtx, ns, id, session)
	if err != nil {
		var ended *orkaTaskEndedError
		if errors.As(err, &ended) {
			observation.Verdict = agentruntime.EvaluationFail
			return observation, errSuiteNativeCaseFailed
		}
		return observation, err
	}
	if err = verify(caseCtx); err != nil {
		return observation, err
	}
	if run != nil {
		if err = worker.verifySuiteCandidateTask(caseCtx, ns, id, task); err != nil {
			return observation, err
		}
	}
	observation.AnswerSHA256 = agentruntime.EvaluationAnswerDigest(answer)
	evidence := agentruntime.EvaluationEvidence{Answer: answer, AnswerAvailable: true}
	if judge == nil {
		observation.Assertions = agentruntime.EvaluateAssertions(frame.Case, evidence)
	} else {
		observation.Assertions = judge(caseCtx, frame.Case, evidence)
		if err = caseCtx.Err(); err != nil {
			return observation, err
		}
		if err = verify(caseCtx); err != nil {
			return observation, err
		}
		if run != nil {
			if err = worker.verifySuiteCandidateTask(caseCtx, ns, id, task); err != nil {
				return observation, err
			}
		}
		var current *orkaObject
		current, err = worker.readOrkaObject(caseCtx, ns, id)
		if err != nil {
			return observation, err
		}
		if !orkaTaskSuccessful(current) {
			return observation, errSuiteNativeEvaluation
		}
	}
	attributed = true
	observation.Verdict = agentruntime.EvaluationPass
	for _, row := range observation.Assertions {
		if row.Verdict != agentruntime.EvaluationPass {
			observation.Verdict = row.Verdict
			if row.Verdict == agentruntime.EvaluationFail {
				return observation, errSuiteNativeCaseFailed
			}
			return observation, errSuiteNativeEvaluation
		}
	}
	return observation, nil
}
