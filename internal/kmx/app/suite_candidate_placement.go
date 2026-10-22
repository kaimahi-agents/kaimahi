package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	suitecore "github.com/agent-primitives/suite/core"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

var errSuiteCandidateRun = errors.New("native candidate run authority invalid")

// Single-owner, concrete run-local ledger. An intent is uncertainty, not an ACK;
// neither a matching name nor live ownership can recover an unknown CREATE UID.
type suiteCandidateResource struct {
	Kind, Name, UID                   string
	Generation                        int64
	SpecDigest, ParentKind, ParentUID string
}
type suiteCandidateRun struct {
	subject                          suiteNativeSubject
	checkSource                      func(context.Context) error
	intents, acknowledged            []suiteCandidateResource
	stopped, started, cleanupStarted bool
}

func newSuiteCandidateRun(p *suiteNativePrepared, checkSource func(context.Context) error) (*suiteCandidateRun, error) {
	if p == nil || checkSource == nil || !validSuitePassingSubject(p.Subject) || p.Subject.EvaluationRun == "" {
		return nil, errSuiteCandidateRun
	}
	s := p.Subject
	s.RequiredImages = append([]string{}, s.RequiredImages...)
	return &suiteCandidateRun{subject: s, checkSource: checkSource}, nil
}

// Snapshot includes independent server ACKs even after hashing or cancellation fails.
func (r *suiteCandidateRun) Resources() []suiteCandidateResource {
	if r == nil {
		return nil
	}
	out := append([]suiteCandidateResource{}, r.intents...)
	for _, known := range r.acknowledged {
		found := false
		for i := range out {
			if out[i].Kind == known.Kind && out[i].Name == known.Name {
				out[i] = known
				found = true
				break
			}
		}
		if !found {
			out = append(out, known)
		}
	}
	return out
}
func (r *suiteCandidateRun) recordIntent(ctx context.Context, kind, name, digest string) error {
	if r == nil || r.stopped || ctx.Err() != nil {
		return errSuiteCandidateRun
	}
	resource := suiteCandidateResource{Kind: kind, Name: name, SpecDigest: digest}
	if !validCandidateRootResource(resource, r.subject) {
		return errSuiteCandidateRun
	}
	for _, old := range r.Resources() {
		if old.Kind == kind && old.Name == name {
			return errSuiteCandidateRun
		}
	}
	r.intents = append(r.intents, resource)
	return nil
}
func validCandidateRootResource(r suiteCandidateResource, s suiteNativeSubject) bool {
	if r.ParentKind != "" || r.ParentUID != "" || !validCandidateResourceMetadata(r) || r.UID != "" && r.Generation < 1 {
		return false
	}
	switch r.Kind {
	case "Provider", "Agent":
		return r.Name == s.PhysicalMember
	case "Task":
		return true
	default:
		return false
	}
}
func validCandidateResourceMetadata(r suiteCandidateResource) bool {
	return scaffold.ValidateObjectName(r.Name) == nil && verificationIdentity(r.Name) && verificationDigest(r.SpecDigest) && r.Generation >= 0 && (r.UID == "" && r.Generation == 0 || verificationIdentity(r.UID))
}
func validCandidateChild(r suiteCandidateResource, resources []suiteCandidateResource) bool {
	if !validCandidateResourceMetadata(r) || r.UID == "" || !verificationIdentity(r.ParentUID) {
		return false
	}
	if !((r.Kind == "Job" || r.Kind == "ConfigMap") && r.ParentKind == "Task" || r.Kind == "Pod" && r.ParentKind == "Job") {
		return false
	}
	for _, parent := range resources {
		if parent.Kind == r.ParentKind && parent.UID == r.ParentUID && parent.UID != "" {
			return true
		}
	}
	return false
}
func (r *suiteCandidateRun) ownsPlacement(p *suiteNativePlacement) bool {
	if r == nil || !r.started || p == nil || !reflect.DeepEqual(p.Subject, r.subject) {
		return false
	}
	for i, kind := range []string{"Provider", "Agent"} {
		owned := []suiteNativeOwnedResource{p.Provider, p.Agent}[i]
		if !verificationIdentity(owned.UID) || owned.Generation < 1 || !verificationDigest(owned.SpecDigest) {
			return false
		}
		matched := false
		for _, acknowledged := range r.acknowledged {
			if acknowledged.Kind == kind && acknowledged.Name == r.subject.PhysicalMember && acknowledged.UID == owned.UID && acknowledged.Generation == owned.Generation && acknowledged.SpecDigest == owned.SpecDigest {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func (r *suiteCandidateRun) check(ctx context.Context, a *App) error {
	if r == nil || r.stopped || r.checkSource == nil || a == nil || !a.guarded || r.subject.EvaluationRun == "" || !validSuitePassingSubject(r.subject) {
		return errSuiteCandidateRun
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.checkSource(ctx); err != nil {
		return err
	}
	if err := a.verifySuiteNativeInputs(ctx, r.subject); err != nil {
		return err
	}
	if err := r.checkSource(ctx); err != nil {
		return err
	}
	return ctx.Err()
}
func suiteCandidateSpecDigest(raw []byte) (string, error) {
	var doc map[string]any
	if json.Unmarshal(raw, &doc) != nil {
		return "", errSuiteNativeEvaluation
	}
	if _, ok := doc["spec"].(map[string]any); !ok {
		return "", errSuiteNativeEvaluation
	}
	canonical, err := suitecore.Marshal(doc["spec"])
	if err != nil {
		return "", errSuiteNativeEvaluation
	}
	return agentruntime.RenderedBundleDigest([][]byte{[]byte("kmx.native-orka.spec/v1"), canonical}), nil
}

func (r *suiteCandidateRun) acknowledge(ctx context.Context, id orkaIdentity, raw []byte) error {
	// Retain UID before any fallible hash or cancellation check.
	r.acknowledged = append(r.acknowledged, suiteCandidateResource{Kind: id.Kind, Name: id.Name, UID: id.UID, Generation: id.Generation})
	i := len(r.acknowledged) - 1
	digest, err := suiteCandidateSpecDigest(raw)
	r.acknowledged[i].SpecDigest = digest
	if err != nil || ctx.Err() != nil || !validCandidateRootResource(r.acknowledged[i], r.subject) {
		r.stopped = true
		return errSuiteCandidateRun
	}
	return nil
}
func (r *suiteCandidateRun) intent(ctx context.Context, doc map[string]any) error {
	raw, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	digest, err := suiteCandidateSpecDigest(raw)
	if err != nil {
		return err
	}
	if err := r.recordIntent(ctx, doc["kind"].(string), orkaObjectName(doc), digest); err != nil {
		r.stopped = true
		return err
	}
	return nil
}

func validateSuiteCandidatePair(raw []byte, desired map[string]any, owner *orkaSuiteOwnership) error {
	var observed map[string]any
	if json.Unmarshal(raw, &observed) != nil || observed["apiVersion"] != desired["apiVersion"] || observed["kind"] != desired["kind"] {
		return errSuiteNativeEvaluation
	}
	meta, _ := observed["metadata"].(map[string]any)
	wanted, _ := desired["metadata"].(map[string]any)
	ann, _ := meta["annotations"].(map[string]any)
	if meta["name"] != wanted["name"] || meta["namespace"] != wanted["namespace"] || meta["deletionTimestamp"] != nil || owner.check(ann) != nil || !reflect.DeepEqual(owner.markers(), suiteOnlyMarkers(ann)) || !orkaRenderedMetadataEqual(meta, wanted) || !reflect.DeepEqual(observed["spec"], desired["spec"]) {
		return errSuiteNativeEvaluation
	}
	return nil
}

func (a *App) verifySuiteCandidateTask(ctx context.Context, ns string, id orkaIdentity, desired map[string]any) error {
	raw, err := a.orkaCapture(ctx, nil, "-n", ns, "get", orkaPlural(id.Kind), id.Name, "-o", "json")
	if err != nil {
		return err
	}
	var object orkaObject
	if json.Unmarshal(raw, &object) != nil || object.Metadata.UID != id.UID || object.Metadata.Generation != id.Generation || !orkaTaskSuccessful(&object) {
		return errSuiteNativeEvaluation
	}
	return validateSuiteNativeTask(raw, desired)
}

func (a *App) admitSuiteCandidatePair(ctx context.Context, r *suiteCandidateRun, doc map[string]any) error {
	if err := r.check(ctx, a); err != nil {
		return err
	}
	ns := r.subject.Target.Namespace
	if err := a.orkaAbsent(ctx, ns, doc); err != nil {
		return err
	}
	if err := r.check(ctx, a); err != nil {
		return err
	}
	body, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	admitted, err := a.orkaCapture(ctx, body, "-n", ns, "create", "--dry-run=server", "--validate=strict", "-f", "-", "-o", "json")
	if err != nil {
		return err
	}
	return validateSuiteCandidatePair(admitted, doc, suiteNativeOwner(r.subject))
}

// Fresh-create only. Never use reconciliation, even for exact S/run collisions.
// A nil placement on failure says nothing about partial UIDs: use run.Resources.
func (a *App) placeSuiteNativeCandidate(ctx context.Context, p *suiteNativePrepared, r *suiteCandidateRun) (placement *suiteNativePlacement, err error) {
	defer func() {
		if err != nil {
			if r != nil {
				r.stopped = true
			}
			err = suiteEvaluationError(ctx, err, true)
		}
	}()
	if p == nil || p.bundle == nil || r == nil || r.started || !reflect.DeepEqual(p.Subject, r.subject) {
		return nil, errSuiteCandidateRun
	}
	worker := a.withRunContext(ctx)
	if err := r.check(ctx, worker); err != nil {
		return nil, err
	}
	r.started = true
	if p.bundle.Task != nil {
		return nil, errSuiteNativeEvaluation
	}
	owner := suiteNativeOwner(r.subject)
	docs := make([]map[string]any, 0, 2)
	for i, desired := range []map[string]any{p.bundle.Provider, p.bundle.Agent} {
		doc, err := cloneOrkaDoc(desired)
		if err != nil {
			return nil, err
		}
		kind := []string{"Provider", "Agent"}[i]
		meta, _ := doc["metadata"].(map[string]any)
		if doc["kind"] != kind || meta["name"] != r.subject.PhysicalMember || meta["namespace"] != r.subject.Target.Namespace {
			return nil, errSuiteNativeEvaluation
		}
		addOrkaMarker(doc, owner.markers())
		docs = append(docs, doc)
	}
	// Both exact desired frames are admitted before the first real CREATE.
	for _, doc := range docs {
		if err := worker.admitSuiteCandidatePair(ctx, r, doc); err != nil {
			return nil, err
		}
	}
	result := &suiteNativePlacement{Subject: r.subject}
	owned := []*suiteNativeOwnedResource{&result.Provider, &result.Agent}
	ns := r.subject.Target.Namespace
	for i, doc := range docs {
		if err := worker.admitSuiteCandidatePair(ctx, r, doc); err != nil {
			return nil, err
		}
		// The admission window may race. Recheck absence, then CREATE atomically
		// refuses AlreadyExists; no recovery GET, update, adoption or name retry.
		if err := worker.orkaAbsent(ctx, ns, doc); err != nil {
			return nil, err
		}
		if err := r.check(ctx, worker); err != nil {
			return nil, err
		}
		if err := r.intent(ctx, doc); err != nil {
			return nil, err
		}
		id, raw, err := worker.createOrkaObjectObserved(ctx, ns, doc)
		if err != nil {
			return nil, err
		}
		if err := r.acknowledge(ctx, id, raw); err != nil {
			return nil, err
		}
		if err := validateSuiteCandidatePair(raw, doc, owner); err != nil {
			return nil, err
		}
		if err := worker.waitOrkaReady(ctx, ns, id); err != nil {
			return nil, err
		}
		current, err := worker.orkaCapture(ctx, nil, "-n", ns, "get", orkaPlural(id.Kind), id.Name, "-o", "json")
		if err != nil {
			return nil, err
		}
		_, fresh, err := inspectSuiteNativeLive(current, ns, id.Kind, id.Name, owner)
		if err != nil || fresh != id || validateSuiteCandidatePair(current, doc, owner) != nil {
			return nil, fmt.Errorf("native candidate changed while waiting for Ready")
		}
		var object orkaObject
		if json.Unmarshal(current, &object) != nil || !orkaCurrentGenerationReady(&object, id.Generation) {
			return nil, errSuiteNativeEvaluation
		}
		digest, err := suiteCandidateSpecDigest(current)
		if err != nil {
			return nil, err
		}
		*owned[i] = suiteNativeOwnedResource{UID: id.UID, Generation: id.Generation, SpecDigest: digest}
	}
	if err := r.check(ctx, worker); err != nil {
		return nil, err
	}
	if err := worker.verifySuiteNative(ctx, result); err != nil {
		return nil, err
	}
	return result, nil
}
