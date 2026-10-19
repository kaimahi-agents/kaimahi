package app

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

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
	if err = worker.preflightReleaseUpdateWrites(ctx, p.lift.native.Subject, p.before); err != nil {
		return result, err
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
		// Finalization is bounded metadata-only work after all object sends have
		// ceased. Cancellation never resumes an object PATCH. An unknown send or
		// failed checkpoint still forbids this path, even if reads look healthy.
		finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		known := reserved && !uncertain
		if known {
			_, finalErr := worker.inspectReleaseUpdatePair(finalCtx, p.lift.native.Subject, objects)
			if finalErr == nil {
				finalErr = worker.checkReleaseReservation(finalCtx, cursor)
			}
			if finalErr != nil {
				known = false
				err = errors.Join(err, finalErr)
				result.ReleaseRecord.State = "unknown"
				if result.State == "ready" {
					result.State = "partial"
					result.placement = nil
				}
			}
		}
		if known && changed {
			record.updateProof = &releaseOwnershipProof{Version: 2, RecordUID: cursor.UID, Objects: append([]releaseStateObject(nil), objects...), Dependencies: p.dependencies}
		}
		persistErr := record.checkpoint(context.WithoutCancel(ctx), result)
		if persistErr != nil {
			result.ProvenanceState = "unknown"
			err = errors.Join(err, errReleaseLiftProvenance)
			known = false
		}
		if known && !changed {
			// No object request was ever sent. Restore the exact prior data, not
			// a fake partial revision: its existing ownership ACK remains valid.
			// A lost restore response may leave the reservation, but cannot make
			// the old baseline or its unchanged object outcomes ambiguous.
			_, restoreErr := worker.writeReleaseState(finalCtx, cursor, p.before.State, id)
			if restoreErr != nil {
				err = errors.Join(err, restoreErr)
				result.ReleaseRecord.State = "unknown"
			} else {
				result.ReleaseRecord.State = p.before.State.History[len(p.before.State.History)-1].State
				result.ProvenanceState = "recorded"
			}
		}
		if known && changed {
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
			released, releaseErr := worker.writeReleaseState(finalCtx, cursor, next, id)
			if releaseErr == nil {
				releaseErr = record.acknowledge(finalCtx, released, result)
			}
			if releaseErr != nil {
				err = errors.Join(err, releaseErr)
				result.ProvenanceState = "unknown"
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
	// Initial admissions precede even the reservation write; ordinary refusal
	// cannot consume non-rollable shared partial history. They are not authority
	// for a later write: the reserved loop re-proves and re-admits each patch.
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
	result.ReleaseRecord = &ReleaseRecordObservation{Name: releaseStateName(next.Suite, next.Member, next.Runtime), UID: cursor.UID, State: "unknown"}
	cursor, err = worker.writeReleaseState(ctx, cursor, next, id)
	if err != nil {
		return result, err
	}
	reserved = true
	result.ReleaseRecord.State = "pending"
	for i, doc := range docs {
		live, e := worker.recheckReleaseUpdate(ctx, p, cursor, objects)
		if e != nil {
			return result, e
		}
		// Admission is repeated against the exact re-proved fresh version, never
		// an earlier dry-run RV after status/controller churn.
		if _, _, e = worker.patchReleaseUpdate(ctx, p, live[i], doc, true); e != nil {
			return result, e
		}
		live, e = worker.recheckReleaseUpdate(ctx, p, cursor, objects)
		if e != nil {
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
		return result, err
	}
	if err = worker.finishReleaseUpdate(ctx, p, objects, &result); err != nil {
		return result, err
	}
	result.State = "ready"
	return result, nil
}

func (a *App) preflightReleaseUpdateWrites(ctx context.Context, subject suiteNativeSubject, before *releaseStateSnapshot) error {
	for _, resource := range []string{"configmaps/" + releaseStateName(before.State.Suite, before.State.Member, "orka"), "providers.core.orka.ai/" + subject.Member, "agents.core.orka.ai/" + subject.Member} {
		raw, e := a.orkaCapture(ctx, nil, "-n", subject.Target.Namespace, "auth", "can-i", "patch", resource)
		if e != nil || strings.TrimSpace(string(raw)) != "yes" {
			return errors.Join(errReleaseStateRBAC, ctx.Err())
		}
	}
	return nil
}

func (a *App) finishReleaseUpdate(ctx context.Context, p *preparedReleaseUpdate, objects []releaseStateObject, result *ReleaseLiftResult) error {
	placement := &suiteNativePlacement{Subject: p.lift.native.Subject, Provider: suiteNativeOwnedResource{UID: objects[0].UID, Generation: objects[0].Generation, SpecDigest: strings.TrimPrefix(objects[0].SpecDigest, "sha256:")}, Agent: suiteNativeOwnedResource{UID: objects[1].UID, Generation: objects[1].Generation, SpecDigest: strings.TrimPrefix(objects[1].SpecDigest, "sha256:")}}
	if p.noop {
		// Keep the acknowledged placement's complete ownership fingerprint;
		// a fresh dependency RV does not authorize re-stamping its markers.
		// Subject remains fresh for strict current-input and later verification.
		placement.releaseBaseline = append([]releaseStateObject(nil), objects...)
	}
	if err := a.verifySuiteNative(ctx, placement); err != nil {
		return err
	}
	result.placement = placement
	return nil
}

// Check shared capacity before a workspace can spend on missing evidence or
// publication. Fixed-width digests and acknowledged identities size the pending
// and terminal forms; this in-memory placeholder is never persisted as proof.
// Apply still checks its actual reservation immediately before effects.
func preflightReleaseUpdateCapacity(s releaseState, suiteDigest string) error {
	id, err := randomHex(16)
	if err != nil {
		return err
	}
	s.Operation = id
	s.Pending = &releaseStateRevision{LiftID: id, SuiteDigest: suiteDigest, EvidenceDigest: "sha256:" + strings.Repeat("0", 64), State: "pending",
		Before: append([]releaseStateObject(nil), s.Baseline...), Objects: append([]releaseStateObject(nil), s.Baseline...)}
	return reserveReleaseUpdateCapacity(s)
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
	return w.commitAcknowledgment(ctx)
}
