package app

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"

	agentsuite "github.com/agent-primitives/suite"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
	godigest "github.com/opencontainers/go-digest"
)

const releaseCreateRecordAnnotation = "kaimahi.dev/lift-create-record"

var errReleaseUpdate = errors.New("release update refused or incomplete; inspect resource outcomes; no automatic retry, undo or takeover")
var errReleaseUpdateRequired = errors.New("owned retained placement already exists; request lift --update with passing evidence for compatibility-checked changes; plain Lift remains create-only")

var errReleaseUpdateDrift = errors.New("recorded release identity, generation, spec or owned metadata changed; refusing update")
var errReleaseUpdateCompatibility = errors.New("update requires the same suite/member/runtime/renderer/model/inference binding and retained ownership proof")
var errReleaseUpdateDependents = errors.New("shared Provider or incomplete dependent inspection; require a complete cluster-wide Agent list of at most 256 objects; no update attempted")

type preparedReleaseUpdate struct {
	lift           *PreparedReleaseLift
	before         *releaseStateSnapshot
	origin, latest *releaseOwnershipRecord
	current        []map[string]any
	dependencies   *releaseTargetDependencies
	noop           bool
	started        atomic.Bool
}

func releaseUpdateFailure(err error) error {
	causes := []error{errReleaseUpdate}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded, errReleaseState, errReleaseStateUnavailable, errReleaseStateWrite, errReleaseStateRBAC, errReleaseOperationInProgress, errReleaseUpdateUnsupported, errReleaseUpdateDrift, errReleaseUpdateCompatibility, errReleaseUpdateDependents, errReleaseLiftProvenance} {
		if errors.Is(err, cause) {
			causes = append(causes, cause)
		}
	}
	return errors.Join(causes...)
}

// This private backend is deliberately not a public command or enrollment path.
// Current placement inspection never requires Ready: a broken known revision
// must not prevent reverting to its original passing digest/evidence.
func (a *App) prepareReleaseUpdate(ctx context.Context, opt ReleaseLiftOptions) (p *preparedReleaseUpdate, err error) {
	defer func() {
		if err != nil {
			err = releaseUpdateFailure(err)
		}
	}()
	if a == nil || a.Cfg == nil || a.Run == nil || ValidateReleaseLiftOptions(opt) != nil {
		return nil, errReleaseUpdate
	}
	inputs, err := readReleaseLiftInputs(ctx, opt)
	if err != nil {
		return nil, err
	}
	if inputs.suite.Runtime.ID != "orka" || opt.Bindings != "" || opt.ExpectedRuntime != "" && opt.ExpectedRuntime != "orka" {
		return nil, errReleaseUpdateCompatibility
	}
	if opt.ToNamespace == "" {
		opt.ToNamespace = OrkaNamespace
	}
	worker := a.releaseLiftWorker(ctx, opt)
	native, err := worker.prepareSuiteNative(ctx, inputs.suite, opt.ToNamespace, strings.TrimPrefix(opt.Inference, "provider:"))
	if err != nil {
		return nil, err
	}
	gate, model, err := worker.releaseLiftGate(ctx, inputs.receipt.Subject, native.Subject, opt.Promote)
	if err != nil {
		return nil, errReleaseUpdateCompatibility
	}
	p, err = worker.inspectReleaseUpdateBaseline(ctx, native, opt.Inference)
	if err != nil {
		return nil, err
	}
	state, current := p.before, p.current
	s := ReleaseLiftSummary{Suite: inputs.suite.Name, SuiteDigest: inputs.suite.Digest, Member: inputs.suite.MemberID, Model: inputs.suite.Model, Runtime: "orka", Target: releaseLiftTarget(native.Subject.Target), SourceTarget: releaseLiftTarget(inputs.receipt.Subject.Target), Promote: opt.Promote, ServingModel: model, Inference: opt.Inference, BindingDigest: native.Subject.BindingDigest, RenderedDigest: native.Subject.RenderedDigest, EvidenceDigest: godigest.FromBytes(inputs.evidence).String(), EvidenceRunID: inputs.receipt.RunID, Gate: gate}
	noop := state.State.History[len(state.State.History)-1].State == "completed"
	for i, doc := range []map[string]any{native.bundle.Provider, native.bundle.Agent} {
		digest, err := suiteNativeDigest("kmx.native-orka.spec/v1", doc["spec"])
		if err != nil {
			return nil, err
		}
		if i == 0 && "sha256:"+digest != state.State.Baseline[0].SpecDigest {
			return nil, errReleaseUpdateCompatibility
		}
		meta, _ := current[i]["metadata"].(map[string]any)
		ann := meta["annotations"].(map[string]any)
		noop = noop && ann[orkaSuiteMarkers[1]] == s.SuiteDigest && "sha256:"+digest == state.State.Baseline[i].SpecDigest
		s.Resources = append(s.Resources, ReleaseLiftResource{Kind: []string{"Provider", "Agent"}[i], Name: s.Member, Action: "update", UID: state.State.Baseline[i].UID, Generation: state.State.Baseline[i].Generation, ResourceVersion: meta["resourceVersion"].(string), SpecDigest: digest})
	}
	if noop {
		for i := range s.Resources {
			s.Resources[i].Action = "verify"
		}
	}
	review, err := agentsuite.Marshal(struct {
		Domain                string
		Plan                  ReleaseLiftSummary
		RecordUID, RecordData string
	}{"kmx.release-update.plan/v1", s, state.UID, state.data})
	if err != nil {
		return nil, err
	}
	s.PlanDigest = godigest.FromBytes(review).String()
	lift := &PreparedReleaseLift{options: opt, artifact: inputs.artifact, native: native, receipt: inputs.receipt, summary: s}
	p.lift, p.noop = lift, noop
	return p, nil
}

// Advice never enrolls a placement or prescribes deleting foreign resources.
func (a *App) releaseLiftOccupiedAdvice(ctx context.Context, native *suiteNativePrepared, inference string, original error) error {
	for _, kind := range []string{"Provider", "Agent"} {
		raw, err := a.orkaCapture(ctx, nil, "-n", native.Subject.Target.Namespace, "get", orkaPlural(kind), native.Subject.Member, "-o", "json")
		if err != nil {
			return original
		}
		if _, _, err := inspectSuiteNativeLive(raw, native.Subject.Target.Namespace, kind, native.Subject.Member, suiteNativeOwner(native.Subject)); err != nil {
			return original
		}
	}
	if _, err := a.inspectReleaseUpdateOwnership(ctx, native, inference); err != nil {
		return errors.Join(original, err)
	}
	return errReleaseUpdateRequired
}

// Inspect existing target authority before workspace evaluation/publication spend.
// This grants no new evidence or mutation authority and does not require Ready.
func (a *App) inspectReleaseUpdateBaseline(ctx context.Context, native *suiteNativePrepared, inference string) (*preparedReleaseUpdate, error) {
	baseline, err := a.inspectReleaseUpdateOwnership(ctx, native, inference)
	if err != nil {
		return nil, err
	}
	if err = a.checkReleaseUpdateDependents(ctx, native.Subject, baseline.before.State.Baseline[1].UID); err != nil {
		return nil, err
	}
	return baseline, nil
}

// Plain create-only refusal advice must not require update-only cluster access.
// This proves retained ownership, not exclusive-dependency update eligibility.
func (a *App) inspectReleaseUpdateOwnership(ctx context.Context, native *suiteNativePrepared, inference string) (*preparedReleaseUpdate, error) {
	state, err := a.readReleaseState(ctx, releaseStateSubject(native.Subject))
	if err != nil {
		return nil, err
	}
	if state == nil {
		return nil, errReleaseUpdateUnsupported
	}
	if state.State.Operation != "" {
		return nil, errReleaseOperationInProgress
	}
	if len(state.State.History) == 0 {
		return nil, errReleaseUpdateUnsupported
	}
	origin, err := readReleaseOwnership(ctx, state.State.OriginLiftID)
	if err != nil {
		return nil, err
	}
	latest, err := readReleaseOwnership(ctx, state.State.History[len(state.State.History)-1].LiftID)
	if err != nil {
		return nil, err
	}
	if origin.State != "ready" || origin.UpdateProof.RecordUID != state.UID || !releaseOwnershipMatches(latest, state) {
		return nil, errReleaseUpdateUnsupported
	}
	for i, object := range origin.UpdateProof.Objects {
		if object.UID != state.State.Baseline[i].UID {
			return nil, errReleaseUpdateUnsupported
		}
	}
	dependencies, err := a.readReleaseTargetDependencies(ctx, native.Subject)
	if err != nil {
		return nil, err
	}
	if !sameReleaseTargetDependencies(origin.UpdateProof.Dependencies, dependencies) || !sameReleaseTargetDependencies(latest.UpdateProof.Dependencies, dependencies) {
		return nil, errReleaseUpdateCompatibility
	}
	if latest.Summary.Model != native.Subject.Model || latest.Summary.Inference != inference ||
		origin.Summary.Suite != state.State.Suite || origin.Summary.Member != state.State.Member || origin.Summary.Runtime != "orka" || !statusSameTarget(origin.Summary.Target, state.State.Target) {
		return nil, errReleaseUpdateCompatibility
	}
	current, err := a.inspectReleaseUpdatePair(ctx, native.Subject, state.State.Baseline)
	if err != nil {
		return nil, err
	}
	digest, err := suiteNativeDigest("kmx.native-orka.spec/v1", native.bundle.Provider["spec"])
	if err != nil || "sha256:"+digest != state.State.Baseline[0].SpecDigest {
		return nil, errReleaseUpdateCompatibility
	}
	baseline := &preparedReleaseUpdate{before: state, origin: origin, latest: latest, current: current, dependencies: dependencies}
	baseline.noop = state.State.History[len(state.State.History)-1].State == "completed"
	for i, doc := range []map[string]any{native.bundle.Provider, native.bundle.Agent} {
		spec, e := suiteNativeDigest("kmx.native-orka.spec/v1", doc["spec"])
		if e != nil {
			return nil, e
		}
		meta, _ := current[i]["metadata"].(map[string]any)
		ann, _ := meta["annotations"].(map[string]any)
		baseline.noop = baseline.noop && ann[orkaSuiteMarkers[1]] == native.Subject.SuiteDigest && "sha256:"+spec == state.State.Baseline[i].SpecDigest
	}
	return baseline, nil
}

// Historical receipts supply evidence only. Current pair ownership always comes
// from the separately validated current shared/private baseline, never history.
func (p *preparedReleaseUpdate) retainedReleaseEvidence(ctx context.Context, digest string) (*suitePassingReceipt, error) {
	ids := []string{}
	if p.origin.Summary.SuiteDigest == digest {
		ids = append(ids, p.origin.ID)
	}
	for i := len(p.before.State.History) - 1; i >= 0; i-- {
		revision := p.before.State.History[i]
		if revision.SuiteDigest == digest {
			ids = append(ids, revision.LiftID)
		}
	}
	if len(ids) == 0 {
		return nil, errSuitePassMissing
	}
	record, err := readReleaseOwnership(ctx, ids[0])
	if err != nil {
		return nil, err
	}
	if record.Summary.SuiteDigest != digest {
		return nil, errReleaseUpdateUnsupported
	}
	receipt, err := parseSuitePassingReceipt(record.Receipt)
	if err != nil || receipt.Subject.SuiteDigest != digest {
		return nil, errReleaseUpdateUnsupported
	}
	return receipt, nil
}

func (a *App) inspectReleaseUpdatePair(ctx context.Context, subject suiteNativeSubject, expected []releaseStateObject) ([]map[string]any, error) {
	if !validReleaseStateObjects(expected, true) {
		return nil, errReleaseUpdateUnsupported
	}
	var docs []map[string]any
	for _, proof := range expected {
		raw, err := a.orkaCapture(ctx, nil, "-n", subject.Target.Namespace, "get", orkaPlural(proof.Kind), subject.Member, "-o", "json")
		if err != nil {
			return nil, errors.Join(errReleaseUpdateDrift, ctx.Err())
		}
		doc, id, err := inspectSuiteNativeLive(raw, subject.Target.Namespace, proof.Kind, subject.Member, suiteNativeOwner(subject))
		if err != nil {
			return nil, errReleaseUpdateDrift
		}
		actual, err := releaseObjectFingerprint(doc, id.UID, id.Generation)
		if err != nil || actual != proof {
			return nil, errReleaseUpdateDrift
		}
		docs = append(docs, doc)
	}
	return docs, nil
}

func (a *App) checkReleaseUpdateDependents(ctx context.Context, s suiteNativeSubject, agentUID string) error {
	raw, err := a.orkaCapture(ctx, nil, "get", "--raw", "/apis/core.orka.ai/v1alpha1/agents?limit=256")
	var list struct {
		APIVersion string           `json:"apiVersion"`
		Kind       string           `json:"kind"`
		Metadata   map[string]any   `json:"metadata"`
		Items      []map[string]any `json:"items"`
	}
	if err != nil || agentsuite.Unmarshal(raw, &list) != nil || list.Kind != "AgentList" || list.APIVersion != "core.orka.ai/v1alpha1" || len(list.Items) > 256 {
		return errors.Join(errReleaseUpdateDependents, ctx.Err())
	}
	if more, present := list.Metadata["continue"]; present && more != "" {
		return errReleaseUpdateDependents
	}
	seen := false
	for _, item := range list.Items {
		meta, _ := item["metadata"].(map[string]any)
		spec, _ := item["spec"].(map[string]any)
		ns, _ := meta["namespace"].(string)
		if item["kind"] != "Agent" || ns == "" || spec == nil {
			return errReleaseUpdateDependents
		}
		uses := false
		if ref, present := spec["providerRef"]; present {
			provider, ok := ref.(map[string]any)
			if !ok {
				return errReleaseUpdateDependents
			}
			providerNS := ""
			if value, present := provider["namespace"]; present {
				var ok bool
				providerNS, ok = value.(string)
				if !ok {
					return errReleaseUpdateDependents
				}
			}
			name, ok := provider["name"].(string)
			if !ok || scaffold.ValidateObjectName(name) != nil || providerNS != "" && scaffold.ValidateNamespace(providerNS) != nil {
				return errReleaseUpdateDependents
			}
			// Unqualified refs can resolve in a Task's namespace. Without
			// invocation inventory, another namespace cannot prove exclusivity.
			uses = name == s.Member && (providerNS == "" || providerNS == s.Target.Namespace)
		}
		if value, present := spec["model"]; present {
			model, ok := value.(map[string]any)
			if !ok {
				return errReleaseUpdateDependents
			}
			if value, present := model["fallbacks"]; present {
				fallbacks, ok := value.([]any)
				if !ok {
					return errReleaseUpdateDependents
				}
				for _, value := range fallbacks {
					fallback, ok := value.(map[string]any)
					if !ok {
						return errReleaseUpdateDependents
					}
					name, ok := fallback["providerRef"].(string)
					if !ok || scaffold.ValidateObjectName(name) != nil {
						return errReleaseUpdateDependents
					}
					uses = uses || name == s.Member
				}
			}
		}
		if !uses {
			continue
		}
		if meta["uid"] != agentUID || meta["name"] != s.Member || ns != s.Target.Namespace {
			return errReleaseUpdateDependents
		}
		seen = true
	}
	if !seen {
		return errReleaseUpdateDependents
	}
	return nil
}

func (a *App) recheckReleaseUpdate(ctx context.Context, p *preparedReleaseUpdate, cursor *releaseStateSnapshot, objects []releaseStateObject) ([]map[string]any, error) {
	live, err := a.readReleaseState(ctx, cursor.State)
	if err != nil {
		return nil, err
	}
	if live != nil && live.State.Operation != "" && live.State.Operation != cursor.State.Operation {
		return nil, errReleaseOperationInProgress
	}
	if err := a.checkReleaseReservation(ctx, cursor); err != nil {
		return nil, err
	}
	for _, retained := range []*releaseOwnershipRecord{p.latest, p.origin} {
		fresh, err := readReleaseOwnership(ctx, retained.ID)
		if err != nil || !reflect.DeepEqual(fresh, retained) {
			return nil, errReleaseUpdateUnsupported
		}
	}
	if err := a.recheckReleaseLiftInputs(ctx, p.lift); err != nil {
		return nil, err
	}
	dependencies, err := a.readReleaseTargetDependencies(ctx, p.lift.native.Subject)
	if err != nil || !reflect.DeepEqual(dependencies, p.dependencies) {
		return nil, errReleaseUpdateCompatibility
	}
	if err := a.checkReleaseUpdateDependents(ctx, p.lift.native.Subject, objects[1].UID); err != nil {
		return nil, err
	}
	return a.inspectReleaseUpdatePair(ctx, p.lift.native.Subject, objects)
}

func (a *App) patchReleaseUpdate(ctx context.Context, p *preparedReleaseUpdate, before, desired map[string]any, dry bool) (map[string]any, suiteNativeOwnedResource, error) {
	meta, _ := before["metadata"].(map[string]any)
	ann, _ := meta["annotations"].(map[string]any)
	merged := map[string]any{}
	for k, v := range ann {
		merged[k] = v
	}
	delete(merged, "kaimahi.dev/description")
	desiredMeta, _ := desired["metadata"].(map[string]any)
	for k, v := range desiredMeta["annotations"].(map[string]any) {
		merged[k] = v
	}
	patch := []map[string]any{}
	for _, key := range []string{"uid", "resourceVersion", "generation"} {
		patch = append(patch, map[string]any{"op": "test", "path": "/metadata/" + key, "value": meta[key]})
	}
	patch = append(patch, map[string]any{"op": "test", "path": "/spec", "value": before["spec"]}, map[string]any{"op": "test", "path": "/metadata/annotations", "value": ann}, map[string]any{"op": "replace", "path": "/spec", "value": desired["spec"]}, map[string]any{"op": "replace", "path": "/metadata/annotations", "value": merged})
	body, err := json.Marshal(patch)
	if err != nil {
		return nil, suiteNativeOwnedResource{}, errReleaseUpdate
	}
	args := []string{"-n", p.lift.summary.Target.Namespace, "patch", orkaPlural(desired["kind"].(string)), p.lift.summary.Member, "--type=json", "--patch-file=/dev/stdin", "-o", "json"}
	if dry {
		args = append(args, "--dry-run=server")
	}
	raw, err := a.orkaCapture(ctx, body, args...)
	if err != nil {
		return nil, suiteNativeOwnedResource{}, err
	}
	admitted, err := releaseLiftAdmitted(p.lift.native.Subject, desired, raw, true)
	if err != nil || admitted.UID != meta["uid"] {
		return nil, suiteNativeOwnedResource{}, errReleaseUpdate
	}
	var doc map[string]any
	if json.Unmarshal(raw, &doc) != nil {
		return nil, suiteNativeOwnedResource{}, errReleaseUpdate
	}
	return doc, admitted, nil
}
