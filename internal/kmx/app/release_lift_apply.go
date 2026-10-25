package app

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"

	suiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	godigest "github.com/opencontainers/go-digest"
)

var ErrReleaseLiftApply = errors.New("release lift apply refused or incomplete; inspect resource outcomes; no automatic retry or cleanup")

type ReleaseLiftOutcome struct {
	ReleaseLiftResource
	Outcome string `json:"outcome"`
	Ready   bool   `json:"ready"`
}

// ReleaseLiftResult is a placement observation, never destination evaluation.
// A partial result preserves acknowledged identities without granting reuse.
type ReleaseLiftResult struct {
	Summary         ReleaseLiftSummary        `json:"summary"`
	State           string                    `json:"state"`
	Resources       []ReleaseLiftOutcome      `json:"resources"`
	LiftID          string                    `json:"liftID,omitempty"`
	ProvenanceState string                    `json:"provenanceState,omitempty"`
	ReleaseRecord   *ReleaseRecordObservation `json:"releaseRecord,omitempty"`
	placement       *suiteNativePlacement
	kagent          *releaseLiftKagentPlacement
}

type releaseLiftInputs struct {
	artifact *suiteoras.Artifact
	suite    *workspace.NativeSuite
	receipt  *suitePassingReceipt
	evidence []byte
}

func readReleaseLiftInputs(ctx context.Context, opt ReleaseLiftOptions) (releaseLiftInputs, error) {
	var in releaseLiftInputs
	var err error
	in.receipt, err = readSuitePassingReceipt(ctx, opt.Evidence)
	if err != nil {
		return in, err
	}
	if opt.FromLayout != "" {
		_, digest, _ := strings.Cut(opt.Reference, "@")
		in.artifact, err = suiteoras.ReadArtifactLayout(ctx, opt.FromLayout, digest)
	} else {
		in.artifact, err = suiteoras.ReadArtifactRegistry(ctx, opt.Reference, opt.PlainHTTP)
	}
	if err != nil {
		return in, err
	}
	in.suite, err = workspace.LoadArtifact(ctx, in.artifact)
	if err != nil {
		return in, err
	}
	in.evidence, err = canonicalSuitePassingReceipt(ctx, in.receipt, in.suite)
	return in, err
}

func (a *App) releaseLiftWorker(ctx context.Context, opt ReleaseLiftOptions) *App {
	worker := *a.withRunContext(ctx)
	cfg := *a.Cfg
	cfg.KubeContext, cfg.ContextSource = opt.ToContext, config.SourceFlag
	worker.Cfg = &cfg
	worker.guarded = false
	return &worker
}

func (a *App) recheckReleaseLiftInputs(ctx context.Context, p *PreparedReleaseLift) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	in, err := readReleaseLiftInputs(ctx, p.options)
	if err != nil {
		return err
	}
	if godigest.FromBytes(in.evidence).String() != p.summary.EvidenceDigest {
		return ErrReleaseLiftApply
	}
	if p.kagent != nil {
		return a.recheckReleaseLiftKagentInputs(ctx, p, in)
	}
	if in.suite.Runtime.ID != "orka" || in.receipt.ExecutionMode != "native-orka" {
		return ErrReleaseLiftApply
	}
	current, err := a.prepareSuiteNative(ctx, in.suite, p.options.ToNamespace, strings.TrimPrefix(p.options.Inference, "provider:"))
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(current.Subject, p.native.Subject) {
		return ErrReleaseLiftApply
	}
	gate, model, err := a.releaseLiftGate(ctx, in.receipt.Subject, current.Subject, p.options.Promote)
	if err != nil {
		return err
	}
	if gate != p.summary.Gate || model != p.summary.ServingModel {
		return ErrReleaseLiftApply
	}
	// The same-subject gate consumes current.Subject without further I/O.
	// Reuse this recheck's freshly read, fully compared inputs, not a prior
	// recheck's observations. Promotion performs an extra Provider read, so
	// its post-gate inspection must remain fresh.
	if gate == "same-subject" {
		return nil
	}
	return a.verifySuiteNativeInputs(ctx, p.native.Subject)
}

func sameReleaseLiftDecision(before, fresh ReleaseLiftResource) bool {
	// Owned status-only RV churn cannot erase evaluated UID/gen/whole-spec proof.
	before.ResourceVersion, fresh.ResourceVersion = "", ""
	return before == fresh
}

func releaseLiftProof(p *PreparedReleaseLift, i int) suiteNativeOwnedResource {
	if p.summary.Gate == "promotion" {
		return suiteNativeOwnedResource{}
	}
	if i == 0 {
		return p.receipt.Provider
	}
	return p.receipt.Agent
}

func (a *App) checkReleaseLiftLeaves(ctx context.Context, p *PreparedReleaseLift, created []suiteNativeOwnedResource) error {
	for i, doc := range []map[string]any{p.native.bundle.Provider, p.native.bundle.Agent} {
		proof := releaseLiftProof(p, i)
		expected := p.summary.Resources[i]
		if created[i].UID != "" {
			proof = created[i]
			expected.Action, expected.UID, expected.Generation, expected.SpecDigest = "reuse", proof.UID, proof.Generation, proof.SpecDigest
		}
		fresh, err := a.inspectReleaseLiftResource(ctx, p.native.Subject, doc, proof)
		if err != nil {
			return err
		}
		if !sameReleaseLiftDecision(expected, fresh) {
			return ErrReleaseLiftApply
		}
	}
	return nil
}

// ApplyReleaseLift is single-use and consumes the retained review digest. All
// gate/guard refusals precede admission. Only absent-conditional CREATE and exact
// receipt-bound no-write reuse are permitted; eval's update policy is unchanged.
func (a *App) ApplyReleaseLift(ctx context.Context, p *PreparedReleaseLift, reviewedDigest string) (result ReleaseLiftResult, err error) {
	if p != nil && p.sessionsAgent != nil {
		return a.applySessionsAgent(ctx, p, reviewedDigest)
	}
	if p != nil && p.update != nil {
		return a.applyReleaseUpdate(ctx, p.update, reviewedDigest)
	}
	result.State = "not-applied"
	guardFailure := false
	var record *releaseLiftRecordWriter
	var shared *releaseStateSnapshot
	var dependencies *releaseTargetDependencies
	var worker *App
	provenanceFailed := false
	defer func() {
		if shared != nil && err == nil && result.State == "ready" {
			record.updateProof, err = releaseCreateProof(shared, result, dependencies)
		}
		if record != nil {
			// Local bounded persistence must retain acknowledged effects even when
			// the operation context was cancelled. It grants no recovery authority.
			if !provenanceFailed {
				result.ProvenanceState = "recorded"
			}
			persistErr := record.checkpoint(context.WithoutCancel(ctx), result)
			if persistErr != nil || provenanceFailed {
				result.ProvenanceState = "unknown"
				if result.State == "ready" {
					result.State = "partial"
					result.placement = nil
					result.kagent = nil
				}
				err = errors.Join(err, errReleaseLiftProvenance)
			}
		}
		if shared != nil && err == nil && result.State == "ready" {
			var released *releaseStateSnapshot
			released, err = worker.finishReleaseCreate(ctx, shared)
			if err == nil {
				err = record.acknowledge(ctx, released, result)
			}
			if err == nil {
				result.ReleaseRecord.State = "ready"
			} else {
				result.ProvenanceState = "unknown"
			}
		}
		if closeErr := record.Close(); closeErr != nil {
			err = errors.Join(err, errReleaseLiftProvenance)
			result.ProvenanceState = "unknown"
		}
		if result.ReleaseRecord != nil && err != nil && result.State == "ready" {
			result.State, result.ReleaseRecord.State = "partial", "unknown"
			result.placement = nil
		}
		if err != nil && !guardFailure {
			cause := ctx.Err()
			if cause == nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
				cause = errContext(err)
			}
			if errors.Is(err, errSuiteKagentOccupied) {
				cause = errors.Join(cause, errSuiteKagentOccupied)
			}
			if errors.Is(err, ErrAgentSessionsLiftPlanOnly) {
				cause = errors.Join(cause, ErrAgentSessionsLiftPlanOnly)
			}
			if errors.Is(err, errReleaseLiftProvenance) {
				cause = errors.Join(cause, errReleaseLiftProvenance)
			}
			if isReleaseStateError(err) {
				cause = errors.Join(cause, err)
			}
			err = errors.Join(ErrReleaseLiftApply, cause)
		}
	}()
	if p != nil && p.sessions != nil {
		return result, ErrAgentSessionsLiftPlanOnly
	}
	if a == nil || a.Cfg == nil || a.Run == nil || p == nil || (p.native == nil) == (p.kagent == nil) || reviewedDigest == "" || reviewedDigest != p.summary.PlanDigest {
		return result, ErrReleaseLiftApply
	}
	result.Summary = p.Summary()
	for _, r := range p.summary.Resources {
		result.Resources = append(result.Resources, ReleaseLiftOutcome{ReleaseLiftResource: r, Outcome: "not-attempted"})
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if !p.started.CompareAndSwap(false, true) {
		return result, ErrReleaseLiftApply
	}
	worker = a.releaseLiftWorker(ctx, p.options)
	if p.kagent != nil {
		// Preserve the closed qualified profile, but use this invocation's App
		// transports and context rather than a preparation-time worker.
		profile := p.kagent.prepared.worker.selectedKagentProfile()
		worker.kagentProfile = &profile
	}
	created := make([]releaseLiftEffect, 2)
	var docs []map[string]any
	recheck := func() error {
		if shared != nil {
			if err := worker.checkReleaseReservation(ctx, shared); err != nil {
				return err
			}
		}
		if err := worker.recheckReleaseLiftInputs(ctx, p); err != nil {
			return err
		}
		if err := worker.checkReleaseLiftRuntimeLeaves(ctx, p, created); err != nil {
			return err
		}
		// Acknowledged creates retain provenance through readiness and every
		// later effect boundary, not only the final pair readback.
		return worker.verifyReleaseLiftProvenance(ctx, p, docs, result)
	}
	if err := recheck(); err != nil {
		return result, err
	}
	if err := worker.guardReleaseLift(ctx, p); err != nil {
		guardFailure = true
		return result, err
	}
	if err := recheck(); err != nil {
		return result, err
	}
	docs, err = p.releaseDocuments()
	if err != nil {
		return result, err
	}
	if releaseFreshPair(p) {
		if err := worker.preflightReleaseStateCreate(ctx, releaseStateSubject(p.native.Subject)); err != nil {
			return result, err
		}
	}
	record, result.LiftID, err = worker.prepareReleaseLiftProvenance(ctx, p, docs)
	if err != nil {
		return result, errors.Join(errReleaseLiftProvenance, ctx.Err())
	}
	if record != nil {
		result.ProvenanceState = "pending"
	}
	admitted := make([]releaseLiftAdmission, len(docs))
	// Preflight both creations before the first persistent write. Reuse never
	// enters admission, and no Secret skeleton or Task belongs to this path.
	for i, doc := range docs {
		if p.summary.Resources[i].Action != "create" {
			continue
		}
		if err := recheck(); err != nil {
			return result, err
		}
		admitted[i], err = worker.admitReleaseLift(ctx, p, i, doc)
		if err != nil {
			return result, err
		}
	}
	if err := recheck(); err != nil {
		return result, err
	}
	if releaseFreshPair(p) {
		dependencies, err = worker.readReleaseTargetDependencies(ctx, p.native.Subject)
		if err != nil {
			return result, ErrReleaseLiftApply
		}
		intent, e := releaseCreateIntent(p, docs, result.LiftID)
		if e != nil {
			return result, e
		}
		result.ReleaseRecord = &ReleaseRecordObservation{Name: releaseStateName(intent.Suite, intent.Member, intent.Runtime), State: "unknown"}
		result.State = "partial"
		shared, err = worker.createReleaseState(ctx, intent)
		if err != nil {
			return result, err
		}
		result.ReleaseRecord.UID, result.ReleaseRecord.State = shared.UID, "pending"
	}
	for i, doc := range docs {
		if err := recheck(); err != nil {
			return result, err
		}
		row := &result.Resources[i]
		if row.Action == "create" {
			// Set ambiguity before the single write. Failed CREATE never triggers a
			// recovery GET, retry, adoption, rollback or invented acknowledged UID.
			result.State, row.Outcome = "partial", "unknown"
			row.UID, row.Generation, row.ResourceVersion, row.SpecDigest = "", 0, "", ""
			created[i], err = worker.createReleaseLift(ctx, p, doc, admitted[i], row)
			if persistErr := record.checkpoint(context.WithoutCancel(ctx), result); persistErr != nil {
				provenanceFailed = true
				return result, errors.Join(err, errReleaseLiftProvenance)
			}
			if err != nil {
				return result, err
			}
			if shared != nil {
				sharedNext, e := worker.checkpointReleaseCreate(ctx, shared, i, *row)
				if e != nil {
					return result, e
				}
				shared = sharedNext
			}
			if err := worker.waitReleaseLift(ctx, p, doc, created[i]); err != nil {
				return result, err
			}
		} else {
			row.Outcome = "reused"
		}
		if err := recheck(); err != nil {
			return result, err
		}
		// Agent readiness includes its exact source workload inventory; defer
		// that credit until the complete private final proof succeeds.
		row.Ready = p.kagent == nil || i == 0
	}
	if err := recheck(); err != nil {
		return result, err
	}
	if err := worker.finishReleaseLift(ctx, p, created, &result); err != nil {
		return result, err
	}
	// Final full input/name checkpoint also binds artifact, E and binding-file
	// identity after the workload proof and before granting success authority.
	if p.kagent != nil {
		if err := recheck(); err != nil {
			result.kagent = nil
			return result, err
		}
	}
	if err := worker.verifyReleaseLiftProvenance(ctx, p, docs, result); err != nil {
		result.placement, result.kagent = nil, nil
		return result, err
	}
	if p.kagent != nil {
		result.Resources[1].Ready = true
	}
	result.State = "ready"
	return result, nil
}

func errContext(err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	return context.DeadlineExceeded
}

// releaseLiftAdmitted permits server metadata but no unreviewed spec mutation.
// UID/gen is credited only to an acknowledged real create, never a dry-run UID.
func releaseLiftAdmitted(subject suiteNativeSubject, desired map[string]any, raw []byte, real bool) (suiteNativeOwnedResource, error) {
	var proof suiteNativeOwnedResource
	var doc map[string]any
	if json.Unmarshal(raw, &doc) != nil || doc["kind"] != desired["kind"] || doc["apiVersion"] != desired["apiVersion"] {
		return proof, ErrReleaseLiftApply
	}
	meta, ok := doc["metadata"].(map[string]any)
	if !ok || meta["name"] != subject.Member || meta["namespace"] != subject.Target.Namespace || meta["deletionTimestamp"] != nil {
		return proof, ErrReleaseLiftApply
	}
	ann, _ := meta["annotations"].(map[string]any)
	if !reflect.DeepEqual(suiteNativeOwner(subject).markers(), suiteOnlyMarkers(ann)) || !orkaRenderedMetadataEqual(meta, desired["metadata"].(map[string]any)) || !reflect.DeepEqual(doc["spec"], desired["spec"]) {
		return proof, ErrReleaseLiftApply
	}
	if !real {
		return proof, nil
	}
	_, id, err := inspectSuiteNativeLive(raw, subject.Target.Namespace, desired["kind"].(string), subject.Member, suiteNativeOwner(subject))
	if err != nil {
		return proof, err
	}
	digest, err := suiteNativeDigest("kmx.native-orka.spec/v1", doc["spec"])
	if err != nil {
		return proof, err
	}
	return suiteNativeOwnedResource{UID: id.UID, Generation: id.Generation, SpecDigest: digest}, nil
}

// VerifyReleaseLiftPlacement revalidates an opaque in-process success result
// before invocation. Serialized summaries cannot manufacture placement proof.
func (a *App) VerifyReleaseLiftPlacement(ctx context.Context, result ReleaseLiftResult) (err error) {
	defer func() {
		if err != nil {
			err = errors.Join(ErrReleaseLiftApply, ctx.Err())
		}
	}()
	if a == nil || a.Cfg == nil || a.Run == nil || result.State != "ready" || (result.placement == nil) == (result.kagent == nil) {
		return ErrReleaseLiftApply
	}
	if result.kagent != nil {
		k := result.kagent
		if k.bindings.recheck() != nil {
			return ErrReleaseLiftApply
		}
		worker := a.releaseLiftWorker(ctx, ReleaseLiftOptions{ToContext: k.prepared.subject.Target.Context})
		profile := k.prepared.worker.selectedKagentProfile()
		worker.kagentProfile = &profile
		_, err := worker.observeSuiteKagent(ctx, k.prepared, k.observation)
		if err != nil {
			return err
		}
		return k.bindings.recheck()
	}
	worker := a.releaseLiftWorker(ctx, ReleaseLiftOptions{ToContext: result.placement.Subject.Target.Context})
	return worker.verifySuiteNative(ctx, result.placement)
}
