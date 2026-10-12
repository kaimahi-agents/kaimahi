package app

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// Success-only current observation, not a transaction or a lineage receipt.
type suiteNativePlacement struct {
	Subject         suiteNativeSubject
	Provider, Agent suiteNativeOwnedResource
}
type suiteNativeOwnedResource struct {
	UID        string
	Generation int64
	SpecDigest string
}

func sameSuiteNativeDecision(before, fresh orkaReconcileCheck) bool {
	if before.id != fresh.id || before.outcome != fresh.outcome {
		return false
	}
	oldMeta, _ := before.existing["metadata"].(map[string]any)
	newMeta, _ := fresh.existing["metadata"].(map[string]any)
	oldAnn, _ := oldMeta["annotations"].(map[string]any)
	newAnn, _ := newMeta["annotations"].(map[string]any)
	return reflect.DeepEqual(suiteOnlyMarkers(oldAnn), suiteOnlyMarkers(newAnn))
}

func (a *App) placeSuiteNative(ctx context.Context, p *suiteNativePrepared) (*suiteNativePlacement, error) {
	if p == nil {
		return nil, fmt.Errorf("native prepared input required")
	}
	owner := suiteNativeOwner(p.Subject)
	if !owner.valid() {
		return nil, fmt.Errorf("invalid native desired ownership")
	}
	if err := a.verifySuiteNativeInputs(ctx, p.Subject); err != nil {
		return nil, err
	}
	identity := orkaPlacementIdentity{suite: owner}
	ns := p.Subject.Target.Namespace
	reviewed, err := a.planOrkaPlacement(ctx, identity, p.bundle, ns)
	if err != nil {
		return nil, err
	}
	if err := a.guardOrkaMutation(ctx, CreateOptions{Name: p.Subject.Member, Namespace: ns}, "create or update owned native suite Provider and Agent in "+ns); err != nil {
		return nil, err
	}
	if err := a.verifySuiteNativeInputs(ctx, p.Subject); err != nil {
		return nil, err
	}
	current, err := a.planOrkaPlacement(ctx, identity, p.bundle, ns)
	if err != nil {
		return nil, err
	}
	for i := range reviewed {
		if !sameSuiteNativeDecision(reviewed[i], current[i]) {
			return nil, fmt.Errorf("native pair changed after review")
		}
	}
	placement := &suiteNativePlacement{Subject: p.Subject}
	resources := []*suiteNativeOwnedResource{&placement.Provider, &placement.Agent}
	for i, doc := range []map[string]any{p.bundle.Provider, p.bundle.Agent} {
		if err := a.verifySuiteNativeInputs(ctx, p.Subject); err != nil {
			return nil, err
		}
		fresh, err := a.inspectOrkaPlacement(ctx, ns, doc, identity)
		if err != nil {
			return nil, err
		}
		if !sameSuiteNativeDecision(current[i], fresh) {
			return nil, fmt.Errorf("%s/%s changed before native action", fresh.id.Kind, fresh.id.Name)
		}
		// Do not pin the review's RV across Ready waits; this fresh candidate
		// carries the immediate inspection's conditional-write precondition.
		id, err := a.applyOrkaReconcile(ctx, ns, fresh)
		if err != nil {
			return nil, err
		}
		if err := a.waitOrkaReady(ctx, ns, id); err != nil {
			return nil, err
		}
		observed, err := a.inspectOrkaPlacement(ctx, ns, doc, identity)
		if err != nil {
			return nil, err
		}
		if observed.outcome != agentruntime.ResourceReused || observed.id != id {
			return nil, fmt.Errorf("%s/%s changed while waiting for native Ready", id.Kind, id.Name)
		}
		if err := a.verifyOrkaReadyNow(ctx, ns, id); err != nil {
			return nil, err
		}
		digest, err := suiteNativeDigest("kmx.native-orka.spec/v1", observed.existing["spec"])
		if err != nil {
			return nil, err
		}
		*resources[i] = suiteNativeOwnedResource{id.UID, id.Generation, digest}
	}
	if err := a.verifySuiteNative(ctx, placement); err != nil {
		return nil, err
	}
	return placement, nil
}

func (a *App) verifySuiteNative(ctx context.Context, p *suiteNativePlacement) error {
	if p == nil {
		return fmt.Errorf("native placement required")
	}
	if err := a.verifySuiteNativeInputs(ctx, p.Subject); err != nil {
		return err
	}
	owner := suiteNativeOwner(p.Subject)
	ns := p.Subject.Target.Namespace
	for i, kind := range []string{"Provider", "Agent"} {
		owned := []suiteNativeOwnedResource{p.Provider, p.Agent}[i]
		raw, err := a.orkaCapture(ctx, nil, "-n", ns, "get", orkaPlural(kind), p.Subject.Member, "--ignore-not-found=true", "-o", "json")
		if err != nil {
			return err
		}
		doc, id, err := inspectSuiteNativeLive(raw, ns, kind, p.Subject.Member, owner)
		if err != nil {
			return err
		}
		meta, _ := doc["metadata"].(map[string]any)
		ann, _ := meta["annotations"].(map[string]any)
		digest, err := suiteNativeDigest("kmx.native-orka.spec/v1", doc["spec"])
		if err != nil {
			return err
		}
		if owned.UID != id.UID || owned.Generation != id.Generation || owned.SpecDigest != digest || !reflect.DeepEqual(owner.markers(), suiteOnlyMarkers(ann)) {
			return fmt.Errorf("%s/%s native placement changed", kind, id.Name)
		}
		var object orkaObject
		if json.Unmarshal(raw, &object) != nil || !orkaCurrentGenerationReady(&object, id.Generation) {
			return fmt.Errorf("%s/%s is not current-generation Ready in the owned snapshot", kind, id.Name)
		}
	}
	return nil
}
