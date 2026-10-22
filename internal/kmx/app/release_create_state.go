package app

import (
	"context"
	"errors"

	suitecore "github.com/agent-primitives/suite/core"
	godigest "github.com/opencontainers/go-digest"
)

// Only a newly created complete pair gets new-version private ownership proof.
// Historical observations and exact or mixed reuse are never enrolled.
type releaseOwnershipProof struct {
	Version      int                        `json:"version"`
	RecordUID    string                     `json:"recordUID"`
	Objects      []releaseStateObject       `json:"objects"`
	Dependencies *releaseTargetDependencies `json:"dependencies"`
}

type ReleaseRecordObservation struct {
	Name  string `json:"name"`
	UID   string `json:"uid,omitempty"`
	State string `json:"state"`
}

func releaseStateSubject(s suiteNativeSubject) releaseState {
	return releaseState{Version: 1, Suite: s.Suite, Member: s.Member, Runtime: "orka", Target: releaseLiftTarget(s.Target)}
}

func releaseFreshPair(p *PreparedReleaseLift) bool {
	return p.kagent == nil && len(p.summary.Resources) == 2 && p.summary.Resources[0].Action == "create" && p.summary.Resources[1].Action == "create"
}

func releaseObjectFingerprint(doc map[string]any, uid string, generation int64) (releaseStateObject, error) {
	meta, _ := doc["metadata"].(map[string]any)
	ann, _ := meta["annotations"].(map[string]any)
	provenance, _ := ann[releaseLiftRecordAnnotation].(string)
	kind, _ := doc["kind"].(string)
	if provenance == "" {
		return releaseStateObject{}, errReleaseState
	}
	spec, err := suiteNativeDigest("kmx.native-orka.spec/v1", doc["spec"])
	if err != nil {
		return releaseStateObject{}, errReleaseState
	}
	owner, err := suitecore.Marshal(struct {
		Domain                    string
		Markers                   map[string]any
		Description, CreateRecord any
	}{"kmx.release-owned-metadata/v1", suiteOnlyMarkers(ann), ann["kaimahi.dev/description"], ann[releaseCreateRecordAnnotation]})
	if err != nil {
		return releaseStateObject{}, errReleaseState
	}
	return releaseStateObject{Kind: kind, UID: uid, Generation: generation, SpecDigest: "sha256:" + spec,
		OwnershipDigest: godigest.FromBytes(owner).String(), LiftRecordDigest: godigest.FromString(provenance).String()}, nil
}

func releaseCreateIntent(p *PreparedReleaseLift, docs []map[string]any, id string) (releaseState, error) {
	s := releaseStateSubject(p.native.Subject)
	s.OriginLiftID, s.Operation = id, id
	s.Pending = &releaseStateRevision{LiftID: id, SuiteDigest: p.summary.SuiteDigest, EvidenceDigest: p.summary.EvidenceDigest, State: "pending"}
	for _, doc := range docs {
		proof, err := releaseObjectFingerprint(doc, "", 0)
		if err != nil {
			return releaseState{}, err
		}
		s.Pending.Objects = append(s.Pending.Objects, proof)
	}
	if _, err := releaseStateBytes(s); err != nil {
		return releaseState{}, err
	}
	return s, nil
}

func (a *App) checkReleaseReservation(ctx context.Context, expected *releaseStateSnapshot) error {
	live, err := a.readReleaseState(ctx, expected.State)
	if err != nil {
		return err
	}
	if live == nil || live.UID != expected.UID || live.ResourceVersion != expected.ResourceVersion || live.data != expected.data {
		return errReleaseStateWrite
	}
	return nil
}

func (a *App) checkpointReleaseCreate(ctx context.Context, before *releaseStateSnapshot, i int, outcome ReleaseLiftOutcome) (*releaseStateSnapshot, error) {
	if outcome.Outcome != "created" || outcome.UID == "" || outcome.Generation < 1 {
		return nil, errReleaseState
	}
	next := before.State
	pending := *next.Pending
	pending.Objects = append([]releaseStateObject(nil), pending.Objects...)
	pending.Objects[i].UID, pending.Objects[i].Generation = outcome.UID, outcome.Generation
	if pending.Objects[i].SpecDigest != "sha256:"+outcome.SpecDigest {
		return nil, errReleaseState
	}
	next.Pending = &pending
	return a.writeReleaseState(ctx, before, next, next.Operation)
}

func releaseCreateProof(state *releaseStateSnapshot, result ReleaseLiftResult, dependencies *releaseTargetDependencies) (*releaseOwnershipProof, error) {
	objects := state.State.Pending.Objects
	if !validReleaseStateObjects(objects, true) || len(result.Resources) != 2 || !validReleaseTargetDependencies(dependencies) {
		return nil, errReleaseState
	}
	for i, object := range objects {
		row := result.Resources[i]
		if row.Outcome != "created" || row.UID != object.UID || row.Generation != object.Generation || "sha256:"+row.SpecDigest != object.SpecDigest {
			return nil, errReleaseState
		}
	}
	return &releaseOwnershipProof{Version: 2, RecordUID: state.UID, Objects: append([]releaseStateObject(nil), objects...), Dependencies: dependencies}, nil
}

func (a *App) finishReleaseCreate(ctx context.Context, before *releaseStateSnapshot) (*releaseStateSnapshot, error) {
	next := before.State
	completed := *next.Pending
	completed.State = "completed"
	next.Baseline = append([]releaseStateObject(nil), completed.Objects...)
	next.History = append(append([]releaseStateRevision(nil), next.History...), completed)
	next.Pending, next.Operation = nil, ""
	return a.writeReleaseState(ctx, before, next, before.State.Operation)
}

func isReleaseStateError(err error) bool {
	return errors.Is(err, errReleaseState) || errors.Is(err, errReleaseStateUnavailable) || errors.Is(err, errReleaseStateWrite) ||
		errors.Is(err, errReleaseOperationInProgress) || errors.Is(err, errReleaseStateRBAC) || errors.Is(err, errReleaseCreateOccupied)
}
