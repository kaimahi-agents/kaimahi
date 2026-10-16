package app

import (
	"context"
	"errors"
	"reflect"
	"slices"

	agentsuite "github.com/agent-primitives/suite"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	godigest "github.com/opencontainers/go-digest"
)

// The evaluated observation is comparison evidence, never create/reuse authority.
// Only actual successful creates populate a new private placement observation.
type releaseLiftKagent struct {
	prepared     *suiteKagentPrepared
	bindings     *suiteKagentBindingSnapshot
	sourceImages []kagentEvaluationImage
}
type releaseLiftKagentPlacement struct {
	prepared    *suiteKagentPrepared
	bindings    *suiteKagentBindingSnapshot
	observation *suiteKagentObservation
}

func releaseLiftKagentGate(source, destination suiteKagentSubject, promote bool) (string, string, error) {
	if reflect.DeepEqual(source, destination) {
		return "same-subject", "", nil
	}
	if !promote || source.Binding.ModelConfig.Provider != "openai" || destination.Binding.ModelConfig.Provider != "openai" || !sameReleaseKagentPackagedSubject(source, destination) || !sameReleaseKagentRuntime(source.Runtime, destination.Runtime) {
		return "", "", ErrReleaseLift
	}
	return "promotion", destination.Model, nil
}

func sameReleaseKagentPackagedSubject(source, destination suiteKagentSubject) bool {
	// Literal approved physical-field whitelist; future fields compare by default.
	project := func(s *suiteKagentSubject) {
		s.Target.Context, s.Target.ClusterUID, s.Target.Namespace, s.Target.NamespaceUID = "", "", "", ""
		s.Binding.Namespace, s.Binding.ModelConfig.BaseURL = "", ""
		s.Binding.ModelConfig.SecretRef.Name, s.Binding.ModelConfig.SecretRef.Key = "", ""
		s.Secret.Name, s.Secret.Namespace, s.Secret.UID, s.Secret.ResourceVersion, s.Secret.Key = "", "", "", "", ""
		s.BindingDigest, s.PayloadDigest, s.RenderedDigest = "", "", ""
	}
	project(&source)
	project(&destination)
	// Runtime has its own equally closed projection, not a blanket zero.
	return sameReleaseKagentRuntime(source.Runtime, destination.Runtime) && func() bool {
		source.Runtime = destination.Runtime
		return reflect.DeepEqual(source, destination)
	}()
}
func sameReleaseKagentRuntime(source, destination suiteKagentRuntime) bool {
	project := func(r *suiteKagentRuntime) {
		r.Namespace, r.ServiceName, r.ServiceUID = "", "", ""
		r.DeploymentName, r.DeploymentUID, r.DeploymentGeneration = "", "", 0
		r.ConfigName, r.ConfigUID, r.ControllerAccount = "", "", ""
		r.Selector, r.WatchNamespaces = nil, ""
	}
	project(&source)
	project(&destination)
	return reflect.DeepEqual(source, destination)
}

func (a *App) prepareReleaseLiftKagent(ctx context.Context, opt ReleaseLiftOptions, in releaseLiftInputs) (*PreparedReleaseLift, error) {
	bindings, err := snapshotSuiteKagentBindings(opt.Bindings)
	if err != nil {
		return nil, err
	}
	if opt.ToNamespace != "" && opt.ToNamespace != bindings.binding.Namespace {
		return nil, ErrReleaseLift
	}
	opt.ToNamespace = bindings.binding.Namespace
	native, err := a.prepareSuiteKagent(ctx, in.suite, bindings.binding)
	if err != nil {
		return nil, err
	}
	source := in.receipt.Kagent.Observation
	gate, model, err := releaseLiftKagentGate(source.Subject, native.subject, opt.Promote)
	if err != nil {
		return nil, err
	}
	s := native.subject
	summary := ReleaseLiftSummary{Suite: s.Suite, SuiteDigest: s.SuiteDigest, Member: s.Member, Model: s.Model, Runtime: s.Profile.ID, Target: releaseLiftTarget(s.Target), SourceTarget: releaseLiftTarget(source.Subject.Target), Promote: opt.Promote, ServingModel: model, BindingDigest: s.BindingDigest, RenderedDigest: s.RenderedDigest, EvidenceDigest: godigest.FromBytes(in.evidence).String(), EvidenceRunID: in.receipt.RunID, Gate: gate}
	p := &PreparedReleaseLift{options: opt, artifact: in.artifact, receipt: in.receipt, summary: summary, kagent: &releaseLiftKagent{prepared: native, bindings: bindings, sourceImages: slices.Clone(source.Images)}}
	if err := a.checkReleaseLiftKagentLeaves(ctx, p, make([]releaseLiftEffect, 2)); err != nil {
		return nil, err
	}
	for _, proof := range native.proofs {
		digest, err := kagentSpecFingerprint(proof.Spec)
		if err != nil {
			return nil, err
		}
		p.summary.Resources = append(p.summary.Resources, ReleaseLiftResource{Kind: proof.Kind, Name: proof.Name, Action: "create", SpecDigest: digest})
	}
	if err := bindings.recheck(); err != nil {
		return nil, err
	}
	if err := native.worker.verifySuiteKagentInputs(ctx, native); err != nil {
		return nil, err
	}
	review, err := agentsuite.Marshal(struct {
		Domain string
		Plan   ReleaseLiftSummary
	}{"kmx.release-lift.plan/v1", p.summary})
	if err != nil {
		return nil, err
	}
	p.summary.PlanDigest = godigest.FromBytes(review).String()
	return p, nil
}

func (a *App) recheckReleaseLiftKagentInputs(ctx context.Context, p *PreparedReleaseLift, in releaseLiftInputs) error {
	k := p.kagent
	if in.suite.Runtime.ID != "kagent" || in.receipt.Kagent == nil || k.bindings.recheck() != nil {
		return ErrReleaseLiftApply
	}
	current, err := a.prepareSuiteKagent(ctx, in.suite, k.bindings.binding)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(current.subject, k.prepared.subject) {
		return ErrReleaseLiftApply
	}
	gate, model, err := releaseLiftKagentGate(in.receipt.Kagent.Observation.Subject, current.subject, p.options.Promote)
	if err != nil || gate != p.summary.Gate || model != p.summary.ServingModel {
		return ErrReleaseLiftApply
	}
	// Current preparation fully qualified all consumed inputs during this recheck.
	return nil
}
func (a *App) checkReleaseLiftKagentLeaves(ctx context.Context, p *PreparedReleaseLift, created []releaseLiftEffect) error {
	ns := p.kagent.prepared.subject.Target.Namespace
	for i, proof := range p.kagent.prepared.proofs {
		if created[i].kagent.UID != "" {
			obj, err := a.readKagentObject(ctx, ns, created[i].kagent)
			if err != nil {
				return err
			}
			if state, _ := kagentPrimaryReadiness(obj, created[i].kagent); state != "" {
				return ErrReleaseLiftApply
			}
		} else {
			obj, err := a.readKagentLiftObject(ctx, ns, proof.Kind, proof.Name)
			if err != nil {
				return ErrReleaseLiftApply
			}
			if obj != nil {
				return errSuiteKagentOccupied
			}
		}
	}
	if created[1].kagent.UID == "" {
		if err := a.kagentGeneratedChildrenAbsent(ctx, ns, p.summary.Member); err != nil {
			var occupied *kagentChildCollisionError
			if errors.As(err, &occupied) {
				return errSuiteKagentOccupied
			}
			return ErrReleaseLiftApply
		}
	}
	return nil
}

func (a *App) finishReleaseLiftKagent(ctx context.Context, p *PreparedReleaseLift, created []releaseLiftEffect) (*releaseLiftKagentPlacement, error) {
	native := p.kagent.prepared
	expected := &suiteKagentObservation{Subject: native.subject, Images: slices.Clone(p.kagent.sourceImages)}
	for _, effect := range created {
		id := effect.kagent
		digest, err := kagentSpecFingerprint(id.Proof.Spec)
		if err != nil || id.UID == "" {
			return nil, ErrReleaseLiftApply
		}
		expected.Resources = append(expected.Resources, agentruntime.ResourceEvidence{Kind: id.Kind, Name: id.Name, Namespace: p.options.ToNamespace, UID: id.UID, Generation: id.Generation, SpecDigest: digest})
	}
	children, images, err := a.suiteKagentChildren(ctx, p.options.ToNamespace, created[1].kagent)
	if err != nil || !reflect.DeepEqual(images, expected.Images) {
		return nil, ErrReleaseLiftApply
	}
	expected.Children = children
	observed, err := a.observeSuiteKagent(ctx, native, expected)
	if err != nil {
		return nil, err
	}
	if err := p.kagent.bindings.recheck(); err != nil {
		return nil, err
	}
	return &releaseLiftKagentPlacement{prepared: native, bindings: p.kagent.bindings, observation: observed}, nil
}
