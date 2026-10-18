package app

import (
	"context"
	"errors"
	"os"
	"strings"

	agentsuite "github.com/agent-primitives/suite"
	godigest "github.com/opencontainers/go-digest"
)

func (a *App) applyReleaseUpdate(ctx context.Context, p *preparedReleaseUpdate, reviewed string) (result ReleaseLiftResult, err error) {
	result.State = "not-applied"
	defer func() {
		if err != nil {
			err = releaseUpdateFailure(err)
		}
	}()
	if p == nil || p.lift == nil || reviewed == "" || reviewed != p.lift.summary.PlanDigest || !p.started.CompareAndSwap(false, true) {
		return result, errReleaseUpdate
	}
	worker := a.releaseLiftWorker(ctx, p.lift.options)
	result.Summary = p.lift.Summary()
	objects := append([]releaseStateObject(nil), p.before.State.Baseline...)
	for i, resource := range result.Summary.Resources {
		resource.SpecDigest = strings.TrimPrefix(objects[i].SpecDigest, "sha256:")
		result.Resources = append(result.Resources, ReleaseLiftOutcome{ReleaseLiftResource: resource, Outcome: "not-attempted"})
	}
	cursor := p.before
	if _, err = worker.recheckReleaseUpdate(ctx, p, cursor, objects); err != nil {
		return result, err
	}
	if err = worker.guardOrkaMutation(ctx, CreateOptions{Name: p.lift.summary.Member, Namespace: p.lift.summary.Target.Namespace}, "update retained suite Provider and Agent in "+p.lift.summary.Target.Namespace+"; operator-arranged quiet window required; no drain, zero-downtime or in-flight guarantees"); err != nil {
		return result, err
	}
	if p.noop {
		if err = worker.finishReleaseUpdate(ctx, p, objects, &result); err != nil {
			return result, err
		}
		if err = worker.checkReleaseReservation(ctx, cursor); err != nil {
			return result, err
		}
		for i := range result.Resources {
			result.Resources[i].Outcome = "reused"
			result.Resources[i].Ready = true
		}
		result.State = "ready"
		return result, nil
	}
	if _, err = worker.recheckReleaseUpdate(ctx, p, cursor, objects); err != nil {
		return result, err
	}
	for _, resource := range []string{"configmaps/" + releaseStateName(p.before.State.Suite, p.before.State.Member, "orka"), "providers.core.orka.ai/" + p.lift.summary.Member, "agents.core.orka.ai/" + p.lift.summary.Member} {
		raw, e := worker.orkaCapture(ctx, nil, "-n", p.lift.summary.Target.Namespace, "auth", "can-i", "patch", resource)
		if e != nil || strings.TrimSpace(string(raw)) != "yes" {
			return result, errors.Join(errReleaseStateRBAC, ctx.Err())
		}
	}
	docs, err := p.lift.releaseDocuments()
	if err != nil {
		return result, err
	}
	original, err := agentsuite.Marshal(p.origin.Annotation)
	if err != nil {
		return result, errReleaseUpdateUnsupported
	}
	for _, doc := range docs {
		addOrkaMarker(doc, map[string]any{releaseCreateRecordAnnotation: string(original)})
	}
	record, id, err := worker.prepareReleaseLiftProvenance(ctx, p.lift, docs)
	if err != nil || record == nil {
		return result, errors.Join(errReleaseLiftProvenance, err)
	}
	result.LiftID, result.ProvenanceState = id, "pending"
	reserved, uncertain, changed := false, false, false
	defer func() {
		// No resolution of ambiguous writes or failed checkpoints. Only a live
		// writer that has stopped object effects can release exact known states.
		known := reserved && !uncertain && ctx.Err() == nil
		if known {
			_, inspectErr := worker.inspectReleaseUpdatePair(ctx, p.lift.native.Subject, objects)
			known = inspectErr == nil && worker.checkReleaseReservation(ctx, cursor) == nil
		}
		if known {
			record.updateProof = &releaseOwnershipProof{Version: 1, RecordUID: cursor.UID, Objects: append([]releaseStateObject(nil), objects...)}
		}
		persistErr := record.checkpoint(context.WithoutCancel(ctx), result)
		if persistErr != nil {
			result.ProvenanceState = "unknown"
			err = errors.Join(err, errReleaseLiftProvenance)
			known = false
		}
		if known {
			next := cursor.State
			terminal := *next.Pending
			terminal.Objects = append([]releaseStateObject(nil), objects...)
			terminal.State = "partial"
			if result.State == "ready" && err == nil {
				terminal.State = "completed"
			}
			next.Baseline = terminal.Objects
			next.History = append(append([]releaseStateRevision(nil), next.History...), terminal)
			next.Pending, next.Operation = nil, ""
			released, releaseErr := worker.writeReleaseState(ctx, cursor, next, id)
			if releaseErr == nil {
				releaseErr = record.acknowledge(ctx, released, result)
			}
			if releaseErr != nil {
				err = errors.Join(err, releaseErr)
				result.ReleaseRecord.State = "unknown"
			} else {
				result.ReleaseRecord.State = terminal.State
				result.ProvenanceState = "recorded"
			}
		}
		if closeErr := record.Close(); closeErr != nil {
			err = errors.Join(err, errReleaseLiftProvenance)
			result.ProvenanceState = "unknown"
		}
		if err != nil && result.State == "ready" {
			result.State = "partial"
			result.placement = nil
		}
		if reserved && !changed && !uncertain && err != nil {
			result.State = "not-applied"
		}
	}()
	intent, err := releaseCreateIntent(p.lift, docs, id)
	if err != nil {
		return result, err
	}
	next := cursor.State
	intent.Pending.Before = append([]releaseStateObject(nil), objects...)
	next.Pending, next.Operation = intent.Pending, id
	if err = reserveReleaseUpdateCapacity(next); err != nil {
		return result, err
	}
	result.ReleaseRecord = &ReleaseRecordObservation{Name: releaseStateName(next.Suite, next.Member, next.Runtime), UID: cursor.UID, State: "unknown"}
	cursor, err = worker.writeReleaseState(ctx, cursor, next, id)
	if err != nil {
		return result, err
	}
	reserved = true
	result.ReleaseRecord = &ReleaseRecordObservation{Name: releaseStateName(next.Suite, next.Member, next.Runtime), UID: cursor.UID, State: "pending"}
	// Preflight both admissions without native effects. Re-prove the complete
	// pair/reservation and use the freshest safe RV at every later boundary.
	for i, doc := range docs {
		live, e := worker.recheckReleaseUpdate(ctx, p, cursor, objects)
		if e != nil {
			uncertain = true
			return result, e
		}
		if _, _, e = worker.patchReleaseUpdate(ctx, p, live[i], doc, true); e != nil {
			return result, e
		}
	}
	for i, doc := range docs {
		live, e := worker.recheckReleaseUpdate(ctx, p, cursor, objects)
		if e != nil {
			uncertain = true
			return result, e
		}
		// Admission is repeated against the exact re-proved fresh version, never
		// an earlier dry-run RV after status/controller churn.
		if _, _, e = worker.patchReleaseUpdate(ctx, p, live[i], doc, true); e != nil {
			return result, e
		}
		live, e = worker.recheckReleaseUpdate(ctx, p, cursor, objects)
		if e != nil {
			uncertain = true
			return result, e
		}
		row := &result.Resources[i]
		row.Outcome, row.UID, row.Generation, row.SpecDigest, row.ResourceVersion = "unknown", "", 0, "", ""
		changed, result.State = true, "partial"
		actual, proof, e := worker.patchReleaseUpdate(ctx, p, live[i], doc, false)
		if e != nil {
			uncertain = true
			return result, e
		}
		row.Outcome, row.UID, row.Generation, row.SpecDigest = "updated", proof.UID, proof.Generation, proof.SpecDigest
		meta, _ := actual["metadata"].(map[string]any)
		row.ResourceVersion, _ = meta["resourceVersion"].(string)
		objects[i], e = releaseObjectFingerprint(actual, proof.UID, proof.Generation)
		if e != nil {
			uncertain = true
			return result, e
		}
		changed, result.State = true, "partial"
		if e = record.checkpoint(context.WithoutCancel(ctx), result); e != nil {
			uncertain = true
			return result, errReleaseLiftProvenance
		}
		next := cursor.State
		pending := *next.Pending
		pending.Objects = append([]releaseStateObject(nil), pending.Objects...)
		pending.Objects[i] = objects[i]
		next.Pending = &pending
		updated, e := worker.writeReleaseState(ctx, cursor, next, id)
		if e != nil {
			uncertain = true
			return result, e
		}
		cursor = updated
		if e = worker.waitOrkaReady(ctx, p.lift.summary.Target.Namespace, orkaIdentity{Kind: objects[i].Kind, Name: p.lift.summary.Member, UID: proof.UID, Generation: proof.Generation}); e != nil {
			return result, e
		}
		row.Ready = true
	}
	if _, err = worker.recheckReleaseUpdate(ctx, p, cursor, objects); err != nil {
		uncertain = true
		return result, err
	}
	if err = worker.finishReleaseUpdate(ctx, p, objects, &result); err != nil {
		return result, err
	}
	result.State = "ready"
	return result, nil
}

func (a *App) finishReleaseUpdate(ctx context.Context, p *preparedReleaseUpdate, objects []releaseStateObject, result *ReleaseLiftResult) error {
	placement := &suiteNativePlacement{Subject: p.lift.native.Subject, Provider: suiteNativeOwnedResource{UID: objects[0].UID, Generation: objects[0].Generation, SpecDigest: strings.TrimPrefix(objects[0].SpecDigest, "sha256:")}, Agent: suiteNativeOwnedResource{UID: objects[1].UID, Generation: objects[1].Generation, SpecDigest: strings.TrimPrefix(objects[1].SpecDigest, "sha256:")}}
	if err := a.verifySuiteNative(ctx, placement); err != nil {
		return err
	}
	result.placement = placement
	return nil
}

// Reserve the largest acknowledged pending/terminal forms before any shared
// reservation or object patch. Known UIDs are preserved; 128 bytes cover all
// int64 generation growth in the two baseline and two revision fields. These
// estimates are capacity bounds only, never invented acknowledgment proof.
func reserveReleaseUpdateCapacity(s releaseState) error {
	if s.Pending == nil || !validReleaseStateObjects(s.Baseline, true) {
		return errReleaseState
	}
	pending := *s.Pending
	pending.Objects = append([]releaseStateObject(nil), pending.Objects...)
	for i := range pending.Objects {
		pending.Objects[i].UID = s.Baseline[i].UID
		pending.Objects[i].Generation = s.Baseline[i].Generation
	}
	s.Pending = &pending
	check := func(state releaseState) error {
		raw, err := releaseStateBytes(state)
		if err != nil || len(raw)+128 > maxReleaseStateBytes {
			return errReleaseState
		}
		return nil
	}
	if err := check(s); err != nil {
		return err
	}
	s.Pending, s.Operation = nil, ""
	s.Baseline = pending.Objects
	for _, outcome := range []string{"partial", "completed"} {
		terminal := s
		pending.State = outcome
		terminal.History = append(append([]releaseStateRevision(nil), s.History...), pending)
		if err := check(terminal); err != nil {
			return err
		}
	}
	return nil
}

// Acknowledge only after the final shared-state write is known committed. An
// absent/failed private ack leaves the next invocation blocked, even if the
// API committed that release but its response was lost.
func (w *releaseLiftRecordWriter) acknowledge(ctx context.Context, state *releaseStateSnapshot, result ReleaseLiftResult) error {
	if len(w.outputs) != 5 || w.updateProof == nil || state == nil {
		return errReleaseLiftProvenance
	}
	for i := w.next; i < 4; i++ {
		if _, err := w.outputs[i].root.Lstat(w.outputs[i].name); !errors.Is(err, os.ErrNotExist) {
			return errReleaseLiftProvenance
		}
	}
	w.next, w.releaseDataDigest = 4, godigest.FromString(state.data).String()
	if err := w.checkpoint(ctx, result); err != nil {
		return errReleaseLiftProvenance
	}
	return nil
}
