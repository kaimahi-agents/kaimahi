package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

var errSuiteKagentOccupied = errors.New("suite Kagent target occupied without complete caller-retained proof; no adoption or updates")

// Observation is caller-retained, in-memory authority only. Resource proofs are
// payload-free fingerprints; the private prepared frame owns normalized specs.
type suiteKagentObservation struct {
	Subject   suiteKagentSubject
	Resources []agentruntime.ResourceEvidence
	Children  []agentruntime.ResourceEvidence
	Images    []kagentEvaluationImage
}
type suiteKagentOutcome struct {
	Kind, Name, Namespace, UID string
	Generation                 int64
	Outcome                    agentruntime.ResourceOutcome
}
type suiteKagentPlacement struct {
	Outcomes    []suiteKagentOutcome
	Observation *suiteKagentObservation
}

func (a *App) placeSuiteKagent(ctx context.Context, p *suiteKagentPrepared, expected *suiteKagentObservation) (result suiteKagentPlacement, err error) {
	if p == nil || p.worker == nil {
		return result, fmt.Errorf("suite Kagent requires a private prepared frame")
	}
	w := p.worker
	ns := p.subject.Target.Namespace
	for _, proof := range p.proofs {
		result.Outcomes = append(result.Outcomes, suiteKagentOutcome{Kind: proof.Kind, Name: proof.Name, Namespace: ns, Outcome: "not-attempted"})
	}
	fail := func(stage string, cause error) (suiteKagentPlacement, error) {
		message := "suite Kagent placement failed at " + stage + "; retained outcomes require inspection, no retry or later writes"
		// Preserve cancellation identity, not untrusted subprocess/payload text.
		if ctx.Err() != nil {
			return result, fmt.Errorf("%s: %w", message, ctx.Err())
		}
		for _, cancellation := range []error{context.Canceled, context.DeadlineExceeded} {
			if errors.Is(cause, cancellation) {
				return result, fmt.Errorf("%s: %w", message, cancellation)
			}
		}
		if errors.Is(cause, errSuiteKagentOccupied) {
			return result, errors.Join(errors.New(message), errSuiteKagentOccupied)
		}
		return result, errors.New(message)
	}
	if expected != nil {
		observed, e := w.observeSuiteKagent(ctx, p, expected)
		if e != nil {
			return fail("retained-proof reuse", e)
		}
		result.Observation = observed
		for i, id := range observed.Resources {
			result.Outcomes[i].UID = id.UID
			result.Outcomes[i].Generation = id.Generation
			result.Outcomes[i].Outcome = agentruntime.ResourceReused
		}
		return result, nil
	}
	if err = w.verifySuiteKagentInputs(ctx, p); err != nil {
		return result, err
	}
	absent := func() error {
		for _, proof := range p.proofs {
			obj, e := w.readKagentLiftObject(ctx, ns, proof.Kind, proof.Name)
			if e != nil {
				return fmt.Errorf("suite Kagent primary absence unknown")
			}
			if obj != nil {
				return errSuiteKagentOccupied
			}
		}
		if w.kagentGeneratedChildrenAbsent(ctx, ns, p.subject.Member) != nil {
			return fmt.Errorf("suite Kagent generated child occupied or absence unknown")
		}
		return nil
	}
	if err = absent(); err != nil {
		return result, err
	}
	// Consent is never preparation and always precedes server admission.
	if e := w.guardKagentOperation(ctx, ns, "place private suite Kagent ModelConfig and Agent", ""); e != nil {
		return fail("destination consent", e)
	}
	admitted := make([]kagentObjectProof, len(p.documents))
	for i, doc := range p.documents {
		if err = w.verifySuiteKagentInputs(ctx, p); err != nil {
			return result, err
		}
		if err = absent(); err != nil {
			return result, err
		}
		body, e := json.Marshal(doc)
		if e != nil {
			return fail("document encoding", e)
		}
		admitCtx, cancel := w.waitContext(ctx, "kagent-admission", 0)
		raw, e := w.kagentCapture(admitCtx, body, "-n", ns, "create", "--dry-run=server", "--validate=strict", "-f", "-", "-o", "json")
		if e != nil && admitCtx.Err() != nil {
			e = admitCtx.Err()
		}
		cancel()
		if e != nil {
			return fail("admission request", e)
		}
		admitted[i], e = validateKagentAdmissionProof(raw, doc, p.proofs[i])
		if e != nil {
			return fail("admission proof", e)
		}
	}
	ids := make([]kagentIdentity, 0, len(p.documents))
	for i, doc := range p.documents {
		if err = w.verifySuiteKagentInputs(ctx, p); err != nil {
			return result, err
		}
		// Preserve both-name preflight while ModelConfig is absent; after its
		// confirmed creation, require its exact current Accepted proof instead.
		if i == 0 {
			if err = absent(); err != nil {
				return result, err
			}
		} else {
			obj, e := w.readKagentObject(ctx, ns, ids[0])
			if e != nil {
				return fail("created ModelConfig proof", e)
			}
			if state, _ := kagentPrimaryReadiness(obj, ids[0]); state != "" {
				return fail("created ModelConfig readiness", nil)
			}
			if w.kagentAbsent(ctx, ns, doc) != nil || w.kagentGeneratedChildrenAbsent(ctx, ns, p.subject.Member) != nil {
				return fail("Agent/child absence", nil)
			}
		}
		result.Outcomes[i].Outcome = "unknown"
		id, e := w.createKagentObject(ctx, ns, doc, admitted[i])
		if id.UID != "" {
			result.Outcomes[i].UID = id.UID
			result.Outcomes[i].Generation = id.Generation
			result.Outcomes[i].Outcome = agentruntime.ResourceCreated
		}
		if e != nil {
			return fail("conditional create "+id.Kind, e)
		}
		ids = append(ids, id)
		readyCtx, cancel := w.waitContext(ctx, "kagent-readiness-"+id.Kind, kagentReadinessTimeout)
		e = w.waitKagentReady(readyCtx, ns, id)
		if e != nil && readyCtx.Err() != nil {
			e = readyCtx.Err()
		}
		cancel()
		if e != nil {
			return fail("created "+id.Kind+" readiness", e)
		}
	}
	expected = &suiteKagentObservation{Subject: p.subject}
	for _, id := range ids {
		digest, e := kagentSpecFingerprint(id.Proof.Spec)
		if e != nil {
			return fail("created spec fingerprint", e)
		}
		expected.Resources = append(expected.Resources, agentruntime.ResourceEvidence{Kind: id.Kind, Name: id.Name, Namespace: ns, UID: id.UID, Generation: id.Generation, SpecDigest: digest})
	}
	children, images, e := w.suiteKagentChildren(ctx, ns, ids[1])
	if e != nil {
		return fail("generated workload proof", e)
	}
	expected.Children = children
	expected.Images = images
	observed, e := w.observeSuiteKagent(ctx, p, expected)
	if e != nil {
		return fail("final placement proof", e)
	}
	result.Observation = observed
	return result, nil
}

func (a *App) suiteKagentChildren(ctx context.Context, ns string, id kagentIdentity) ([]agentruntime.ResourceEvidence, []kagentEvaluationImage, error) {
	var images []kagentEvaluationImage
	children, err := a.observeKagentLiftWorkload(ctx, ns, id, &images)
	if err != nil || !suiteKagentAMD64(images) {
		return nil, nil, fmt.Errorf("suite Kagent generated workload must qualify linux/amd64")
	}
	return children, images, nil
}

func (a *App) observeSuiteKagent(ctx context.Context, p *suiteKagentPrepared, expected *suiteKagentObservation) (*suiteKagentObservation, error) {
	fail := func() (*suiteKagentObservation, error) {
		return nil, fmt.Errorf("suite Kagent complete retained placement changed or cannot be qualified")
	}
	if p == nil || expected == nil || len(expected.Resources) != 2 || len(expected.Children) != 4 || !reflect.DeepEqual(expected.Subject, p.subject) {
		return fail()
	}
	if a.verifySuiteKagentInputs(ctx, p) != nil {
		return fail()
	}
	ids := make([]kagentIdentity, 0, 2)
	ns := p.subject.Target.Namespace
	for i, resource := range expected.Resources {
		proof := p.proofs[i]
		digest, err := kagentSpecFingerprint(proof.Spec)
		if err != nil || resource.Kind != proof.Kind || resource.Name != proof.Name || resource.Namespace != ns || resource.UID == "" || resource.Generation < 1 || resource.SpecDigest != digest {
			return fail()
		}
		id := kagentIdentity{Kind: proof.Kind, Name: proof.Name, UID: resource.UID, Generation: resource.Generation, Proof: proof}
		ids = append(ids, id)
	}
	checkPrimaries := func() bool {
		for _, id := range ids {
			obj, e := a.readKagentObject(ctx, ns, id)
			if e != nil {
				return false
			}
			if state, _ := kagentPrimaryReadiness(obj, id); state != "" {
				return false
			}
		}
		return true
	}
	if !checkPrimaries() {
		return fail()
	}
	children, images, err := a.suiteKagentChildren(ctx, ns, ids[1])
	if err != nil || !reflect.DeepEqual(children, expected.Children) || !reflect.DeepEqual(images, expected.Images) {
		return fail()
	}
	if a.verifySuiteKagentInputs(ctx, p) != nil || !checkPrimaries() {
		return fail()
	}
	current, currentImages, err := a.suiteKagentChildren(ctx, ns, ids[1])
	if err != nil || !reflect.DeepEqual(current, children) || !reflect.DeepEqual(currentImages, images) || a.verifySuiteKagentInputs(ctx, p) != nil || !checkPrimaries() {
		return fail()
	}
	// The returned snapshot shares no marker/selector/image slices with caller proof.
	subject := p.subject
	subject.Runtime.Selector = copyKagentLabels(subject.Runtime.Selector)
	subject.Runtime.ControllerImages = slices.Clone(subject.Runtime.ControllerImages)
	return &suiteKagentObservation{Subject: subject, Resources: slices.Clone(expected.Resources), Children: slices.Clone(children), Images: slices.Clone(images)}, nil
}
