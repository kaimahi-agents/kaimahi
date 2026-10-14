package app

import (
	"context"
	"encoding/json"
	"reflect"
)

// Closed, private effect facts: each runtime retains its own typed proof.
// The common loop alone owns consumption, checkpoints and partial outcomes.
type releaseLiftEffect struct {
	orka   suiteNativeOwnedResource
	kagent kagentIdentity
	image  *ReleaseLiftResource
}
type releaseLiftAdmission struct {
	kagent kagentObjectProof
	image  map[string]any
}

func (p *PreparedReleaseLift) releaseDocuments() ([]map[string]any, error) {
	if p.image != nil {
		var docs []map[string]any
		for _, doc := range p.image.plan.Objects {
			copy, err := cloneOrkaDoc(doc)
			if err != nil {
				return nil, err
			}
			docs = append(docs, copy)
		}
		return docs, nil
	}
	if p.kagent != nil {
		return p.kagent.prepared.documents, nil
	}
	docs := make([]map[string]any, 0, 2)
	for _, desired := range []map[string]any{p.native.bundle.Provider, p.native.bundle.Agent} {
		doc, err := cloneOrkaDoc(desired)
		if err != nil {
			return nil, err
		}
		addOrkaMarker(doc, suiteNativeOwner(p.native.Subject).markers())
		docs = append(docs, doc)
	}
	return docs, nil
}
func (a *App) guardReleaseLift(ctx context.Context, p *PreparedReleaseLift) error {
	if p.image != nil {
		return a.guardOrkaMutation(ctx, CreateOptions{Name: p.image.plan.Name, Namespace: p.summary.Target.Namespace}, "create absent or reuse exact evaluated image suite placement")
	}
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
	if p.image != nil {
		raw, err := a.orkaCapture(ctx, body, "-n", ns, "create", "--dry-run=server", "--validate=strict", "-f", "-", "-o", "json")
		if err != nil {
			return admitted, err
		}
		admitted.image, err = imageAdmission(doc, raw)
		return admitted, err
	}
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
	if p.image != nil {
		body, err := json.Marshal(doc)
		if err != nil {
			return effect, err
		}
		raw, err := a.orkaCapture(ctx, body, "-n", ns, "create", "--validate=strict", "-f", "-", "-o", "json")
		if err != nil {
			return effect, err
		}
		var live map[string]any
		if json.Unmarshal(raw, &live) != nil {
			return effect, ErrReleaseLiftApply
		}
		meta, _ := live["metadata"].(map[string]any)
		uid, _ := meta["uid"].(string)
		generation, _ := meta["generation"].(float64)
		if uid != "" {
			row.UID, row.Generation, row.Outcome = uid, int64(generation), "created"
		}
		checked, err := imageAdmission(doc, raw)
		if err != nil {
			return effect, err
		}
		if !reflect.DeepEqual(imageAdmissionContent(checked), imageAdmissionContent(admitted.image)) {
			return effect, ErrReleaseLiftApply
		}
		d, err := imageObjectDigest(live)
		if err != nil || uid == "" {
			return effect, ErrReleaseLiftApply
		}
		row.SpecDigest = d
		proof := row.ReleaseLiftResource
		proof.Action = "reuse"
		effect.image = &proof
		return effect, nil
	}
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
	if p.image != nil {
		if doc["kind"] != "Deployment" {
			return nil
		}
		_, err := a.orkaCapture(ctx, nil, "-n", p.summary.Target.Namespace, "rollout", "status", "deployment/"+p.image.plan.Name, "--timeout=180s")
		return err
	}
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
	if p.image != nil {
		for i, doc := range p.image.plan.Objects {
			proof := &p.image.evidence.Resources[i]
			expected := p.summary.Resources[i]
			if created[i].image != nil {
				proof = created[i].image
				expected = *proof
			}
			row, err := a.inspectImageReleaseResource(ctx, p.image, doc, proof)
			if err != nil {
				return err
			}
			if !sameReleaseLiftDecision(expected, row) {
				return ErrReleaseLiftApply
			}
		}
		return nil
	}
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
	if p.image != nil {
		if err := a.checkReleaseLiftRuntimeLeaves(ctx, p, created); err != nil {
			return err
		}
		copy := *p.image
		copy.observed = make([]ReleaseLiftResource, len(p.summary.Resources))
		for i, row := range p.summary.Resources {
			copy.observed[i] = row
			if created[i].image != nil {
				copy.observed[i] = *created[i].image
			}
		}
		result.image = &copy
		return nil
	}
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
