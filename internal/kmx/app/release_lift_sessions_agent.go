package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	suitecore "github.com/agent-primitives/suite/core"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsessions"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
	godigest "github.com/opencontainers/go-digest"
)

// An occupied immutable name is an actionable one-line refusal, including when
// it is discovered after planning. Keep the ordinary sentinel exit contracts.
type sessionsAgentOccupied struct{ message string }

func (e *sessionsAgentOccupied) Error() string   { return e.message }
func (e *sessionsAgentOccupied) Unwrap() []error { return []error{ErrReleaseLift, ErrReleaseLiftApply} }

type releaseLiftSessionsAgent struct {
	document map[string]any
	spec     agentsessions.AgentSpec
}

func (a *App) sessionsAgentTarget(ctx context.Context, opt ReleaseLiftOptions) (ReleaseLiftTarget, error) {
	o := destinationStatusObserver{a: a, ctx: ctx, report: destinationStatusReport{Complete: true}}
	cluster, ns := o.namespaceUID("kube-system"), o.namespaceUID(opt.ToNamespace)
	if !o.report.Complete {
		return ReleaseLiftTarget{}, errors.New("Agent destination identity is unknown")
	}
	return ReleaseLiftTarget{Context: opt.ToContext, Namespace: opt.ToNamespace, ClusterUID: cluster, NamespaceUID: ns}, nil
}

func (a *App) readSessionsAgent(ctx context.Context, ns, name string) (*agentsessions.Agent, error) {
	raw, err := a.orkaCapture(ctx, nil, "-n", ns, "get", agentsessions.AgentPlural, name, "--ignore-not-found=true", "-o", "json")
	if err != nil || len(raw) == 0 {
		return nil, err
	}
	agent, err := agentsessions.DecodeAgent(raw)
	if err != nil || agent.Metadata.Name != name || agent.Metadata.Namespace != ns || !statusOpaque(agent.Metadata.UID) || !statusSafeText(agent.Spec.Member) {
		return nil, errors.New("Agent resource is malformed or has a different identity")
	}
	return &agent, nil
}

func sessionsAgentDecision(agent *agentsessions.Agent, spec agentsessions.AgentSpec) (string, error) {
	if agent == nil {
		return "create", nil
	}
	if agent.Spec != spec {
		return "", &sessionsAgentOccupied{fmt.Sprintf("Agent %s already names %s; use a new --name for changed inputs", agent.Metadata.Name, agent.Spec.Package.Reference)}
	}
	if agent.Status.AgentUID != agent.Metadata.UID || len(agent.Metadata.DeletionTimestamp) > 0 && string(agent.Metadata.DeletionTimestamp) != "null" {
		return "", errors.New("existing Agent UID is not acknowledged or is deleting; no adoption")
	}
	return "unchanged", nil
}

func (a *App) prepareSessionsAgent(ctx context.Context, opt ReleaseLiftOptions, in releaseLiftInputs) (*PreparedReleaseLift, error) {
	if in.receipt.ExecutionMode != "native-agentsessions" || in.receipt.Sessions == nil {
		return nil, ErrReleaseLift
	}
	name := opt.AgentName
	if name == "" {
		name = in.suite.MemberID
	}
	if scaffold.ValidateObjectName(name) != nil || !statusSafeText(name) {
		return nil, ErrReleaseLift
	}
	spec := agentsessions.AgentSpec{Member: in.suite.MemberID}
	spec.Package.Reference = opt.Reference
	spec.ModelBindingRef.Name = "default"
	target, err := a.sessionsAgentTarget(ctx, opt)
	if err != nil {
		return nil, err
	}
	live, err := a.readSessionsAgent(ctx, opt.ToNamespace, name)
	if err != nil {
		return nil, err
	}
	action, err := sessionsAgentDecision(live, spec)
	if err != nil {
		return nil, err
	}
	document := map[string]any{"apiVersion": agentsessions.AgentAPIVersion, "kind": "Agent", "metadata": map[string]any{"name": name, "namespace": opt.ToNamespace}, "spec": spec}
	body, _ := json.Marshal(document)
	var doc map[string]any
	json.Unmarshal(body, &doc)
	specBytes, _ := suitecore.Marshal(spec)
	row := ReleaseLiftResource{Kind: "Agent", Name: name, Action: action, SpecDigest: godigest.FromBytes(specBytes).String()}
	if live != nil {
		row.UID = live.Metadata.UID
		row.Generation = live.Metadata.Generation
	}
	s := ReleaseLiftSummary{Suite: in.suite.Name, SuiteDigest: in.suite.Digest, Member: in.suite.MemberID, Model: in.suite.Model, Runtime: "agentsessions", Target: target, Gate: "development-source", EvidenceDigest: godigest.FromBytes(in.evidence).String(), EvidenceRunID: in.receipt.RunID, Resources: []ReleaseLiftResource{row}, SessionsAgent: doc}
	review, err := suitecore.Marshal(s)
	if err != nil {
		return nil, err
	}
	s.PlanDigest = godigest.FromBytes(review).String()
	s.SessionsAgent = nil
	return &PreparedReleaseLift{options: opt, artifact: in.artifact, receipt: in.receipt, summary: s, sessionsAgent: &releaseLiftSessionsAgent{document: doc, spec: spec}}, nil
}

func (a *App) applySessionsAgent(ctx context.Context, p *PreparedReleaseLift, digest string) (result ReleaseLiftResult, err error) {
	result.State = "not-applied"
	if a == nil || a.Cfg == nil || a.Run == nil || digest == "" || digest != p.summary.PlanDigest || !p.started.CompareAndSwap(false, true) {
		return result, ErrReleaseLiftApply
	}
	result.Summary = p.Summary()
	result.Resources = []ReleaseLiftOutcome{{ReleaseLiftResource: p.summary.Resources[0], Outcome: "not-attempted"}}
	defer func() {
		_, occupied := err.(*sessionsAgentOccupied)
		if err != nil && !occupied {
			err = errors.Join(ErrReleaseLiftApply, err)
		}
	}()
	worker := a.releaseLiftWorker(ctx, p.options)
	in, err := readReleaseLiftInputs(ctx, p.options)
	if err != nil || godigest.FromBytes(in.evidence).String() != p.summary.EvidenceDigest {
		return result, ErrReleaseLiftApply
	}
	target, err := worker.sessionsAgentTarget(ctx, p.options)
	if err != nil || target != p.summary.Target {
		return result, errors.Join(err, errors.New("Agent destination identity changed"))
	}
	row := &result.Resources[0]
	live, err := worker.readSessionsAgent(ctx, target.Namespace, row.Name)
	if err != nil {
		return result, err
	}
	action, err := sessionsAgentDecision(live, p.sessionsAgent.spec)
	if err != nil {
		return result, err
	}
	if action != row.Action || live != nil && (live.Metadata.UID != row.UID || live.Metadata.Generation != row.Generation) {
		return result, errors.New("Agent changed since plan")
	}
	if live != nil {
		row.Outcome = "unchanged"
		result.State = "pending"
		if agentsessions.AgentRegistered(*live) {
			result.State = "registered"
			return result, nil
		}
		return result, ErrDestinationStatusPending
	}
	if err = worker.guardOrkaMutation(ctx, CreateOptions{Name: row.Name, Namespace: target.Namespace}, "create immutable Agent "+target.Namespace+"/"+row.Name); err != nil {
		return result, err
	}
	// Consent may take time. Re-read frozen inputs/target and absence, never retry
	// a conditional create or adopt an object that appeared meanwhile.
	in, err = readReleaseLiftInputs(ctx, p.options)
	if err != nil || godigest.FromBytes(in.evidence).String() != p.summary.EvidenceDigest {
		return result, ErrReleaseLiftApply
	}
	targetNow, e := worker.sessionsAgentTarget(ctx, p.options)
	if e != nil || targetNow != target {
		return result, errors.New("Agent destination identity changed")
	}
	live, err = worker.readSessionsAgent(ctx, target.Namespace, row.Name)
	if err != nil || live != nil {
		return result, errors.Join(err, errors.New("Agent name is no longer absent"))
	}
	body, _ := json.Marshal(p.sessionsAgent.document)
	row.Outcome = "unknown"
	result.State = "partial"
	raw, err := worker.orkaCapture(ctx, body, "-n", target.Namespace, "create", "--validate=strict", "-f", "-", "-o", "json")
	if err != nil {
		return result, err
	}
	created, decodeErr := agentsessions.DecodeAgent(raw)
	// Preserve a safely acknowledged identity even if admission changed the spec.
	identity, identityErr := agentsessions.DecodeAgentIdentity(raw)
	if identityErr == nil && identity.Metadata.Name == row.Name && identity.Metadata.Namespace == target.Namespace && statusOpaque(identity.Metadata.UID) {
		row.Outcome = "created"
		row.UID = identity.Metadata.UID
		row.Generation = identity.Metadata.Generation
	}
	if decodeErr != nil || row.Outcome != "created" || created.Spec != p.sessionsAgent.spec {
		return result, errors.New("created Agent identity or spec does not match plan")
	}
	waitCtx, cancel := worker.waitContext(ctx, "sessions-registration", 2*time.Minute)
	defer cancel()
	for {
		observed, e := worker.readSessionsAgent(waitCtx, target.Namespace, row.Name)
		if e != nil {
			return result, e
		}
		if observed == nil || observed.Metadata.UID != row.UID || observed.Metadata.Generation != row.Generation || !reflect.DeepEqual(observed.Spec, p.sessionsAgent.spec) {
			return result, errors.New("Agent identity or spec changed while waiting for registration")
		}
		if agentsessions.AgentRegistered(*observed) {
			result.State = "registered"
			return result, nil
		}
		if err = worker.pause(waitCtx, time.Second); err != nil {
			return result, err
		}
	}
}
