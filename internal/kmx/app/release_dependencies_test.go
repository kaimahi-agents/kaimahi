package app

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseUpdateInferenceProviderRVOnlyBetweenRevisions(t *testing.T) {
	a, reads, old, next, _, api := releaseUpdateFixture(t, dependencyPromotionFixture)
	// An original passing source receipt does not claim the refreshed target's
	// exact binding; retain the existing explicit promotion gate.
	next.Promote, old.Promote = true, true
	nativeMetadata(reads.objects["providers.core.orka.ai/inference"])["resourceVersion"] = "status-only-2"
	p, err := a.prepareReleaseUpdate(t.Context(), next)
	if err != nil {
		t.Fatal("RV-only dependency churn refused before update", err)
	}
	if r, err := a.applyReleaseUpdate(t.Context(), p, p.lift.summary.PlanDigest); err != nil || r.State != "ready" {
		t.Fatal("RV-only dependency churn prevented update", err)
	}
	// The initial target proof must survive a second RV change and rollback;
	// neither old proof nor original passing evidence is rewritten/backfilled.
	nativeMetadata(reads.objects["providers.core.orka.ai/inference"])["resourceVersion"] = "status-only-3"
	rollback, err := a.prepareReleaseUpdate(t.Context(), old)
	if err != nil {
		t.Fatal("RV-only churn prevented original-evidence rollback", err)
	}
	if r, err := a.applyReleaseUpdate(t.Context(), rollback, rollback.lift.summary.PlanDigest); err != nil || r.State != "ready" || api.patches != 4 {
		t.Fatal("original-evidence rollback failed", err)
	}
}

func dependencyPromotionFixture(reads *nativeReadFixture) {
	reads.objects["providers.core.orka.ai/inference"]["spec"].(map[string]any)["defaultModel"] = "test-model"
}

func TestReleaseUpdateSameDigestRVChurnIsNoWrite(t *testing.T) {
	a, reads, old, _, dir, api := releaseUpdateFixture(t, dependencyPromotionFixture)
	old.Promote = true
	nativeMetadata(reads.objects["providers.core.orka.ai/inference"])["resourceVersion"] = "status-only-new"
	p, err := a.prepareReleaseUpdate(t.Context(), old)
	if err != nil || !p.noop {
		t.Fatal("same-digest dependency refresh not eligible", err)
	}
	before := reads.records.writes
	r, err := a.applyReleaseUpdate(t.Context(), p, p.lift.summary.PlanDigest)
	if err != nil || r.State != "ready" || r.placement == nil || api.patches != 0 || reads.records.writes != before || r.LiftID != "" {
		t.Fatal("RV-only no-op failed or rewrote the placement", r.State, err)
	}
	if err := a.VerifyReleaseLiftPlacement(t.Context(), r); err != nil {
		t.Fatal("fresh dependency proof invalidated retained no-write placement", err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "agents.core.orka.ai.json"))
	var doc map[string]any
	_ = json.Unmarshal(raw, &doc)
	nativeMetadata(doc)["annotations"].(map[string]any)["kaimahi.dev/description"] = "changed owned metadata"
	seedReconcile(t, dir, doc)
	if err := a.VerifyReleaseLiftPlacement(t.Context(), r); err == nil {
		t.Fatal("retained-marker verification relaxed full ownership proof")
	}
}

func TestReleaseUpdateInferenceProviderWholeSpecDriftRefuses(t *testing.T) {
	a, reads, _, next, _, api := releaseUpdateFixture(t, dependencyPromotionFixture)
	// defaultModel is absent from the native binding projection. The exact
	// same-subject gate does not re-run promotion's model check, so only the
	// creation-time whole-spec proof detects this otherwise invisible drift.
	reads.objects["providers.core.orka.ai/inference"]["spec"].(map[string]any)["defaultModel"] = "other-declared-model"
	before := reads.records.writes
	if _, err := a.prepareReleaseUpdate(t.Context(), next); !errors.Is(err, errReleaseUpdateCompatibility) || api.patches != 0 || reads.records.writes != before {
		t.Fatal("unprojected inference spec drift was accepted", err)
	}
}

func TestReleaseUpdateStableDependencyIdentityCannotChange(t *testing.T) {
	for _, component := range []string{"provider UID", "provider generation", "secret RV", "secret UID", "secret ref"} {
		t.Run(component, func(t *testing.T) {
			a, reads, _, next, _, api := releaseUpdateFixture(t, dependencyPromotionFixture)
			next.Promote = true
			provider := reads.objects["providers.core.orka.ai/inference"]
			secret := reads.objects["secret/model-secret"]
			switch component {
			case "provider UID":
				nativeMetadata(provider)["uid"] = "other-provider"
			case "provider generation":
				nativeMetadata(provider)["generation"] = float64(2)
			case "secret RV":
				nativeMetadata(secret)["resourceVersion"] = "other-secret-version"
			case "secret UID":
				nativeMetadata(secret)["uid"] = "other-secret"
			case "secret ref":
				provider["spec"].(map[string]any)["secretRef"].(map[string]any)["key"] = "other-key"
			}
			before := reads.records.writes
			if _, err := a.prepareReleaseUpdate(t.Context(), next); err == nil || api.patches != 0 || reads.records.writes != before {
				t.Fatal("changed dependency was accepted", component, err)
			}
		})
	}
}

func TestReleaseUpdateNoCreationDependencyProofCannotEnroll(t *testing.T) {
	a, _, _, next, _, api := releaseUpdateFixture(t)
	files, err := filepath.Glob(filepath.Join(os.Getenv("KMX_HOME"), "release-lifts", "*", "4.json"))
	if err != nil || len(files) != 1 {
		t.Fatal("expected one new baseline")
	}
	raw, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	if json.Unmarshal(raw, &wire) != nil {
		t.Fatal("invalid baseline fixture")
	}
	// Explicitly simulate the earlier observational/prototype proof format.
	// Only this member is changed; canonical receipt bytes are untouched.
	proof := strings.Replace(string(wire["updateProof"]), `"version":2`, `"version":1`, 1)
	var old map[string]json.RawMessage
	_ = json.Unmarshal([]byte(proof), &old)
	delete(old, "dependencies")
	encoded, _ := json.Marshal(old)
	changed := strings.Replace(string(raw), string(wire["updateProof"]), string(encoded), 1)
	if err := os.WriteFile(files[0], []byte(changed), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.prepareReleaseUpdate(t.Context(), next); !errors.Is(err, errReleaseUpdateUnsupported) || api.patches != 0 {
		t.Fatal("old proof was enrolled or silently backfilled", err)
	}
}

func TestReleaseCreateBoundsDependencyProofBeforeEffects(t *testing.T) {
	a, reads, opt, _ := releaseApplyFixture(t, "both")
	dependencyPromotionFixture(reads)
	opt.Promote = true
	reads.objects["providers.core.orka.ai/inference"]["spec"].(map[string]any)["baseURL"] = "https://model.example.invalid/" + strings.Repeat("x", 9<<10)
	p, err := a.PrepareReleaseLift(t.Context(), opt)
	if err != nil {
		t.Fatal("valid large target URL did not reach proof preflight", err)
	}
	if _, err := a.ApplyReleaseLift(t.Context(), p, p.Summary().PlanDigest); err == nil || reads.records.writes != 0 {
		t.Fatal("oversize target proof reached shared/native effects", err)
	}
}

func TestReleaseUpdateInferenceRVWithinOperationStillRefuses(t *testing.T) {
	a, reads, _, next, _, api := releaseUpdateFixture(t)
	p, err := a.prepareReleaseUpdate(t.Context(), next)
	if err != nil {
		t.Fatal(err)
	}
	before := reads.records.writes
	nativeMetadata(reads.objects["providers.core.orka.ai/inference"])["resourceVersion"] = "after-reviewed-snapshot"
	if _, err := a.applyReleaseUpdate(t.Context(), p, p.lift.summary.PlanDigest); err == nil || reads.records.writes != before || api.patches != 0 {
		t.Fatal("cross-revision allowance relaxed the within-operation snapshot")
	}
}
