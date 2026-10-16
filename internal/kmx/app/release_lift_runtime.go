package app

import (
	"context"
	"encoding/json"
	"maps"
)

// Closed, private effect facts: each runtime retains its own typed proof.
// The common loop alone owns consumption, checkpoints and partial outcomes.
type releaseLiftEffect struct {
	orka   suiteNativeOwnedResource
	kagent kagentIdentity
}
type releaseLiftAdmission struct {
	kagent kagentObjectProof
}

func (p *PreparedReleaseLift) releaseDocuments() ([]map[string]any, error) {
	var desiredDocs []map[string]any
	if p.kagent != nil {
		desiredDocs = p.kagent.prepared.documents
	} else {
		desiredDocs = []map[string]any{p.native.bundle.Provider, p.native.bundle.Agent}
	}
	docs := make([]map[string]any, 0, 2)
	for _, desired := range desiredDocs {
		doc, err := cloneOrkaDoc(desired)
		if err != nil {
			return nil, err
		}
		if p.kagent == nil {
			addOrkaMarker(doc, suiteNativeOwner(p.native.Subject).markers())
		}
		docs = append(docs, doc)
	}
	return docs, nil
}
func (a *App) guardReleaseLift(ctx context.Context, p *PreparedReleaseLift) error {
	if p.kagent != nil {
		return a.guardKagentOperation(ctx, p.options.ToNamespace, "create new suite-owned Kagent ModelConfig and Agent", a.InvocationCommand)
	}
	return a.guardOrkaMutation(ctx, CreateOptions{Name: p.summary.Member, Namespace: p.summary.Target.Namespace}, "create absent or reuse exact evaluated suite Provider and Agent in "+p.summary.Target.Namespace)
}
func (a *App) admitReleaseLift(ctx context.Context, p *PreparedReleaseLift, i int, doc map[string]any) (releaseLiftAdmission, error) {
	var admitted releaseLiftAdmission
	body, err := json.Marshal(doc)
	if err != nil {
		return admitted, err
	}
	ns := p.summary.Target.Namespace
	if p.kagent != nil {
		admitCtx, cancel := a.waitContext(ctx, "kagent-admission", 0)
		defer cancel()
		raw, err := a.kagentCapture(admitCtx, body, "-n", ns, "create", "--dry-run=server", "--validate=strict", "-f", "-", "-o", "json")
		if admitCtx.Err() != nil {
			return admitted, admitCtx.Err()
		}
		if err != nil {
			return admitted, err
		}
		// Server dry-run may assign metadata; it never becomes create authority.
		admitted.kagent, err = validateKagentAdmissionProof(raw, doc, p.kagent.prepared.proofs[i])
		if err == nil {
			// Retain the create-time metadata in this invocation's admission proof,
			// without changing the suite ownership set or the prepared plan.
			admitted.kagent.Markers = maps.Clone(admitted.kagent.Markers)
			if admitted.kagent.Markers == nil {
				admitted.kagent.Markers = map[string]string{}
			}
			ann := doc["metadata"].(map[string]any)["annotations"].(map[string]any)
			if value, ok := ann[releaseLiftRecordAnnotation].(string); ok {
				admitted.kagent.Markers[releaseLiftRecordAnnotation] = value
			}
		}
		return admitted, err
	}
	raw, err := a.orkaCapture(ctx, body, "-n", ns, "create", "--dry-run=server", "--validate=strict", "-f", "-", "-o", "json")
	if err != nil {
		return admitted, err
	}
	_, err = releaseLiftAdmitted(p.native.Subject, doc, raw, false)
	return admitted, err
}
func (a *App) createReleaseLift(ctx context.Context, p *PreparedReleaseLift, doc map[string]any, admitted releaseLiftAdmission, row *ReleaseLiftOutcome) (releaseLiftEffect, error) {
	var effect releaseLiftEffect
	ns := p.summary.Target.Namespace
	if p.kagent != nil {
		id, err := a.createKagentObject(ctx, ns, doc, admitted.kagent)
		// An acknowledgement survives proof failure; a spec credit does not.
		if id.UID != "" {
			row.Outcome, row.UID, row.Generation = "created", id.UID, id.Generation
		}
		if err != nil {
			return effect, err
		}
		digest, err := kagentSpecFingerprint(id.Proof.Spec)
		if err != nil {
			return effect, err
		}
		row.SpecDigest = digest
		effect.kagent = id
		return effect, nil
	}
	id, raw, err := a.createOrkaObjectObserved(ctx, ns, doc)
	if id.UID != "" {
		row.Outcome, row.UID, row.Generation = "created", id.UID, id.Generation
	}
	if err != nil {
		return effect, err
	}
	proof, err := releaseLiftAdmitted(p.native.Subject, doc, raw, true)
	if err != nil {
		return effect, err
	}
	effect.orka = proof
	row.SpecDigest = proof.SpecDigest
	return effect, nil
}
func (a *App) waitReleaseLift(ctx context.Context, p *PreparedReleaseLift, doc map[string]any, effect releaseLiftEffect) error {
	if p.kagent != nil {
		readyCtx, cancel := a.waitContext(ctx, "kagent-readiness-"+effect.kagent.Kind, kagentReadinessTimeout)
		defer cancel()
		err := a.waitKagentReady(readyCtx, p.summary.Target.Namespace, effect.kagent)
		if readyCtx.Err() != nil {
			return readyCtx.Err()
		}
		return err
	}
	return a.waitOrkaReady(ctx, p.summary.Target.Namespace, orkaIdentity{Kind: doc["kind"].(string), Name: p.summary.Member, UID: effect.orka.UID, Generation: effect.orka.Generation})
}
func (a *App) checkReleaseLiftRuntimeLeaves(ctx context.Context, p *PreparedReleaseLift, created []releaseLiftEffect) error {
	if p.kagent != nil {
		return a.checkReleaseLiftKagentLeaves(ctx, p, created)
	}
	proofs := make([]suiteNativeOwnedResource, len(created))
	for i, effect := range created {
		proofs[i] = effect.orka
	}
	return a.checkReleaseLiftLeaves(ctx, p, proofs)
}
func (a *App) finishReleaseLift(ctx context.Context, p *PreparedReleaseLift, created []releaseLiftEffect, result *ReleaseLiftResult) error {
	if p.kagent != nil {
		placed, err := a.finishReleaseLiftKagent(ctx, p, created)
		if err != nil {
			return err
		}
		result.kagent = placed
		return nil
	}
	placement := &suiteNativePlacement{Subject: p.native.Subject}
	proofs := []*suiteNativeOwnedResource{&placement.Provider, &placement.Agent}
	for i := range proofs {
		*proofs[i] = releaseLiftProof(p, i)
		if created[i].orka.UID != "" {
			*proofs[i] = created[i].orka
		}
	}
	if err := a.verifySuiteNative(ctx, placement); err != nil {
		return err
	}
	result.placement = placement
	return nil
}
