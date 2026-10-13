package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
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

// The caller supplies a bounded per-case context and has consented to placement
// and the full ordered inventory. Only the actual native observation is credited.
func (a *App) evaluateSuiteNative(ctx context.Context, placement *suiteNativePlacement, frame suitePassingInventoryCase, port string) (observation suitePassingObservation, err error) {
	observation = suitePassingObservation{CaseDigest: frame.Metadata.CaseDigest, InputDigest: frame.Metadata.InputDigest, EvaluationReceipt: agentruntime.EvaluationReceipt{CaseID: frame.Case.ID, Verdict: agentruntime.EvaluationUnknown}}
	defer func() {
		if err != nil {
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
	if err = worker.verifySuiteNative(ctx, placement); err != nil {
		return observation, err
	}
	suffix, err := randomHex(8)
	if err != nil {
		return observation, err
	}
	subject := placement.Subject
	ns := subject.Target.Namespace
	name := strings.TrimRight(subject.Member[:min(len(subject.Member), 40)], "-.") + "-eval-" + suffix
	observation.TaskName = name
	task := map[string]any{
		"apiVersion": "core.orka.ai/v1alpha1", "kind": "Task",
		"metadata": map[string]any{"name": name, "namespace": ns},
		"spec":     map[string]any{"type": "ai", "prompt": frame.Case.Input, "agentRef": map[string]any{"name": subject.Member, "namespace": ns}, "resources": map[string]any{}},
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
	admitted, err := worker.orkaCapture(caseCtx, body, "-n", ns, "create", "--dry-run=server", "--validate=strict", "-f", "-", "-o", "json")
	if err != nil {
		return observation, err
	}
	if err = validateSuiteNativeTask(admitted, task); err != nil {
		return observation, err
	}
	if err = worker.verifySuiteNative(caseCtx, placement); err != nil {
		return observation, err
	}
	id, created, err := worker.createOrkaObjectObserved(caseCtx, ns, task)
	if err != nil {
		return observation, err
	}
	if err = validateSuiteNativeTask(created, task); err != nil {
		return observation, err
	}
	observation.TaskUID = id.UID
	// The installed Task CRD has a status subresource: a spec change bumps
	// generation. Existing UID/generation polls bind this CREATE spec frame.
	answer, err := worker.waitOrkaTaskResult(caseCtx, ns, id, session)
	if err != nil {
		var ended *orkaTaskEndedError
		if errors.As(err, &ended) {
			observation.Verdict = agentruntime.EvaluationFail
			return observation, errSuiteNativeCaseFailed
		}
		return observation, err
	}
	if err = worker.verifySuiteNative(caseCtx, placement); err != nil {
		return observation, err
	}
	observation.AnswerSHA256 = agentruntime.EvaluationAnswerDigest(answer)
	observation.Assertions = agentruntime.EvaluateAssertions(frame.Case, agentruntime.EvaluationEvidence{Answer: answer, AnswerAvailable: true})
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
