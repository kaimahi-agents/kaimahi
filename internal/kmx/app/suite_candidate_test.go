package app

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	suitecore "github.com/agent-primitives/suite/core"
)

const candidateRun = "0123456789abcdef" + "0123456789abcdef"

func TestSuiteCandidateCanonicalCompatibility(t *testing.T) {
	_, prepared, suite, placement, observations := passingFixture(t)
	r, err := newSuitePassingReceipt(t.Context(), prepared, placement, observations)
	if err != nil {
		t.Fatal(err)
	}
	r.RunID = candidateRun
	raw, err := canonicalSuitePassingReceipt(t.Context(), r, suite)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	if got := hex.EncodeToString(digest[:]); got != "dc791dd107b2f31254a2e47d68ef5919423bb46e97fb52880380a0ac5a597d63" {
		t.Fatalf("canonical receipt digest = %s; subject revision = %s", got, r.Subject.RenderedDigest)
	}
}

// The new candidate extension must survive the closed wire boundary, whereas
// incomplete extensions, forged physical names and arbitrary fields must not.

func TestSuiteCandidateReceiptWireProjection(t *testing.T) {
	_, prepared, suite, placement, observations := passingFixture(t)
	r, err := newSuitePassingReceipt(t.Context(), prepared, placement, observations)
	if err != nil {
		t.Fatal(err)
	}
	r.RunID = candidateRun
	raw, err := canonicalSuitePassingReceipt(t.Context(), r, suite)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	subject := document["subject"].(map[string]any)
	subject["EvaluationRun"] = candidateRun
	subject["PhysicalMember"] = "kmx-eval-" + strings.TrimPrefix(r.Subject.SuiteDigest, "sha256:")[:16] + "-" + candidateRun
	document["candidateCleanup"] = "complete"
	raw, err = suitecore.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseSuitePassingReceipt(raw)
	if err != nil {
		t.Fatalf("candidate extension refused: %v", err)
	}
	roundtrip, err := suitecore.Marshal(parsed)
	if err != nil || !bytes.Equal(raw, roundtrip) {
		t.Fatalf("candidate extension lost: %v", err)
	}
	for _, change := range []string{"missing physical", "missing run", "mismatched run", "forged physical", "uppercase run", "missing cleanup", "incomplete cleanup", "unknown", "canonical cleanup", "credential metadata"} {
		t.Run(change, func(t *testing.T) {
			var mutated map[string]any
			if err := json.Unmarshal(raw, &mutated); err != nil {
				t.Fatal(err)
			}
			s := mutated["subject"].(map[string]any)
			switch change {
			case "missing physical":
				delete(s, "PhysicalMember")
			case "missing run":
				delete(s, "EvaluationRun")
			case "mismatched run":
				mutated["runID"] = strings.Repeat("a", 32)
			case "forged physical":
				s["PhysicalMember"] = "external-agent"
			case "uppercase run":
				s["EvaluationRun"] = strings.ToUpper(candidateRun)
			case "missing cleanup":
				delete(mutated, "candidateCleanup")
			case "incomplete cleanup":
				mutated["candidateCleanup"] = "pending"
			case "unknown":
				s["FuturePackagedField"] = "ignored?"
			case "canonical cleanup":
				delete(s, "PhysicalMember")
				delete(s, "EvaluationRun")
			case "credential metadata":
				s["Model"] = "ghp_" + strings.Repeat("a", 36)
			}
			bad, err := suitecore.Marshal(mutated)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := parseSuitePassingReceipt(bad); err == nil {
				t.Fatal("unsafe candidate accepted")
			}
		})
	}
	if reflect.DeepEqual(parsed.Subject, placement.Subject) {
		t.Fatal("candidate aliased canonical subject")
	}
	if err := basicSuitePassingReceipt(nil); !errors.Is(err, errSuitePassingReceipt) {
		t.Fatal(err)
	}
}

func TestSuiteCandidatePhysicalName(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	name, err := suiteCandidatePhysicalName(digest, candidateRun)
	if err != nil || name != "kmx-eval-aaaaaaaaaaaaaaaa-"+candidateRun || len(name) != 58 {
		t.Fatalf("physical name %q: %v", name, err)
	}
	for _, tc := range []struct{ digest, run string }{
		{strings.Repeat("a", 64), candidateRun}, {"sha256:" + strings.Repeat("a", 63), candidateRun}, {"sha256:" + strings.Repeat("A", 64), candidateRun}, {digest, ""}, {digest, strings.ToUpper(candidateRun)}, {digest, candidateRun + "a"},
	} {
		if _, err := suiteCandidatePhysicalName(tc.digest, tc.run); err == nil {
			t.Errorf("accepted invalid candidate %q/%q", tc.digest, tc.run)
		}
	}
}

func TestSuiteCandidateRenderPreservesLogicalPackage(t *testing.T) {
	a, suite, f := nativeFixture(t)
	before, err := suitecore.Marshal(suite)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := a.prepareSuiteNative(t.Context(), suite, OrkaNamespace, "inference")
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := a.prepareSuiteNativeCandidate(t.Context(), suite, OrkaNamespace, "inference", candidateRun)
	if err != nil {
		t.Fatal(err)
	}
	physical := "kmx-eval-" + strings.TrimPrefix(suite.Digest, "sha256:")[:16] + "-" + candidateRun
	for _, doc := range []map[string]any{candidate.bundle.Provider, candidate.bundle.Agent} {
		if nativeMetadata(doc)["name"] != physical {
			t.Fatalf("wrong physical render: %v", nativeMetadata(doc))
		}
	}
	ref := candidate.bundle.Agent["spec"].(map[string]any)["providerRef"].(map[string]any)
	if ref["name"] != physical || ref["namespace"] != OrkaNamespace {
		t.Fatalf("wrong providerRef: %v", ref)
	}
	if candidate.Subject.Member != suite.MemberID || candidate.Subject.EvaluationRun != candidateRun || candidate.Subject.physicalMember() != physical || canonical.Subject.physicalMember() != suite.MemberID {
		t.Fatal("logical/physical identity conflated")
	}
	if candidate.Subject.BindingDigest != canonical.Subject.BindingDigest || candidate.Subject.PayloadDigest == canonical.Subject.PayloadDigest || candidate.Subject.RenderedDigest == canonical.Subject.RenderedDigest {
		t.Fatal("wrong candidate identity domains")
	}
	if err := coherentSuitePassingSubject(candidate.Subject, suite); err != nil {
		t.Fatal(err)
	}
	another, err := a.prepareSuiteNativeCandidate(t.Context(), suite, OrkaNamespace, "inference", strings.Repeat("b", 32))
	if err != nil || another.Subject.RenderedDigest == candidate.Subject.RenderedDigest || another.Subject.SuiteDigest != candidate.Subject.SuiteDigest {
		t.Fatalf("run did not distinguish render while retaining full suite identity: %v", err)
	}
	after, err := suitecore.Marshal(suite)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("candidate mutated loaded source")
	}
	// A logical-name inference Provider is distinct from the candidate physical pair.
	selected := f.objects["providers.core.orka.ai/inference"]
	nativeMetadata(selected)["name"] = suite.MemberID
	f.objects[orkaPlural("Provider")+"/"+suite.MemberID] = selected
	if _, err := a.prepareSuiteNativeCandidate(t.Context(), suite, OrkaNamespace, suite.MemberID, candidateRun); err != nil {
		t.Fatalf("logical collision refused candidate: %v", err)
	}
	calls := f.calls
	if _, err := a.prepareSuiteNativeCandidate(t.Context(), suite, OrkaNamespace, physical, candidateRun); err == nil || f.calls != calls {
		t.Fatal("physical collision not refused before reads")
	}
	for _, run := range []string{"", strings.ToUpper(candidateRun)} {
		if _, err := a.prepareSuiteNativeCandidate(t.Context(), suite, OrkaNamespace, "inference", run); err == nil || f.calls != calls {
			t.Fatal("invalid run reached reads")
		}
	}
	// Physical length does not depend on the authored member's maximum length.
	nativeMetadata(selected)["name"] = "inference"
	suite.MemberID = strings.Repeat("m", 63)
	long, err := a.prepareSuiteNativeCandidate(t.Context(), suite, OrkaNamespace, "inference", candidateRun)
	if err != nil || nativeMetadata(long.bundle.Agent)["name"] != physical {
		t.Fatalf("long logical member affected physical name: %v", err)
	}
}

func candidatePassingFixture(t *testing.T) (*suitePassingReceipt, *suiteNativePrepared) {
	t.Helper()
	_, prepared, suite, placement, observations := passingFixture(t)
	a, _, _ := nativeFixture(t)
	candidate, err := a.prepareSuiteNativeCandidate(t.Context(), suite, OrkaNamespace, "inference", candidateRun)
	if err != nil {
		t.Fatal(err)
	}
	placement.Subject = candidate.Subject
	entropy := &refusingSuiteEntropy{}
	previous := rand.Reader
	rand.Reader = entropy
	defer func() { rand.Reader = previous }()
	r, err := newSuitePassingCandidateReceipt(t.Context(), prepared, placement, observations, candidateRun)
	if err != nil || entropy.calls != 0 {
		t.Fatalf("candidate constructor: %v, RNG reads=%d", err, entropy.calls)
	}
	if r.RunID != candidateRun || r.CandidateCleanup != "complete" || r.SourceFingerprint != prepared.Fingerprint {
		t.Fatal("candidate run/cleanup/source identity lost")
	}
	raw, err := canonicalSuitePassingReceipt(t.Context(), r, suite)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseSuitePassingReceipt(raw); err != nil {
		t.Fatal(err)
	}
	for _, run := range []string{"", strings.Repeat("b", 32), strings.ToUpper(candidateRun)} {
		if _, err := newSuitePassingCandidateReceipt(t.Context(), prepared, placement, observations, run); !errors.Is(err, errSuitePassingReceipt) {
			t.Fatalf("mismatched constructor run accepted: %v", err)
		}
	}
	if _, err := newSuitePassingReceipt(t.Context(), prepared, placement, observations); !errors.Is(err, errSuitePassingReceipt) {
		t.Fatalf("legacy constructor accepted candidate: %v", err)
	}
	return r, candidate
}

func TestSuiteCandidateOwnershipAndLiveVerification(t *testing.T) {
	r, p := candidatePassingFixture(t)
	owner := suiteNativeOwner(p.Subject)
	if owner.markers()[suiteCandidateRunMarker] != candidateRun || !owner.valid() {
		t.Fatal("candidate owner lost run")
	}
	if !hasOrkaSuiteReservation(map[string]any{suiteCandidateRunMarker: nil}) || !hasOrkaSuiteReservation(map[string]string{suiteCandidateRunMarker: "malformed"}) {
		t.Fatal("run-only marker permits legacy adoption")
	}
	for _, mutation := range []string{"missing", "other", "null"} {
		ann := owner.markers()
		switch mutation {
		case "missing":
			delete(ann, suiteCandidateRunMarker)
		case "other":
			ann[suiteCandidateRunMarker] = strings.Repeat("b", 32)
		case "null":
			ann[suiteCandidateRunMarker] = nil
		}
		if owner.check(ann) == nil {
			t.Fatalf("accepted %s run ownership", mutation)
		}
	}
	canonical := p.Subject
	canonical.PhysicalMember, canonical.EvaluationRun = "", ""
	if _, ok := suiteNativeOwner(canonical).markers()[suiteCandidateRunMarker]; ok {
		t.Fatal("canonical marker changed")
	}
	if suiteNativeOwner(canonical).check(owner.markers()) == nil {
		t.Fatal("canonical owner adopted candidate")
	}
	a, _, f := nativeFixture(t)
	placement := &suiteNativePlacement{Subject: r.Subject}
	for i, kind := range []string{"Provider", "Agent"} {
		doc := nativeOwnedDoc(t, p, kind)
		addOrkaMarker(doc, owner.markers())
		doc["status"] = map[string]any{"ready": true, "conditions": []any{map[string]any{"type": "Ready", "status": "True", "observedGeneration": 1}}}
		var live map[string]any
		if err := json.Unmarshal(mustJSON(t, doc), &live); err != nil {
			t.Fatal(err)
		}
		f.objects[orkaPlural(kind)+"/"+p.Subject.PhysicalMember] = live
		digest, err := suiteNativeDigest("kmx.native-orka.spec/v1", live["spec"])
		if err != nil {
			t.Fatal(err)
		}
		owned := suiteNativeOwnedResource{strings.ToLower(kind) + "-uid", 1, digest}
		if i == 0 {
			placement.Provider = owned
		} else {
			placement.Agent = owned
		}
	}
	f.change = func(key string, _ int, _ map[string]any) error {
		if key == orkaPlural("Provider")+"/"+p.Subject.Member || key == orkaPlural("Agent")+"/"+p.Subject.Member {
			t.Fatal("candidate verification read logical member")
		}
		return nil
	}
	if err := a.verifySuiteNative(t.Context(), placement); err != nil {
		t.Fatal(err)
	}
	nativeMetadata(f.objects[orkaPlural("Agent")+"/"+p.Subject.PhysicalMember])["annotations"].(map[string]any)[suiteCandidateRunMarker] = strings.Repeat("b", 32)
	if err := a.verifySuiteNative(t.Context(), placement); err == nil {
		t.Fatal("wrong run live pair credited")
	}
}

func TestSuiteCandidateRunScanExceptionIsTypedAndNarrow(t *testing.T) {
	_, p := candidatePassingFixture(t)
	if _, err := suiteNativeDigest("kmx.native-orka.revision/v1", p.Subject); err != nil {
		t.Fatal(err)
	}
	bad := p.Subject
	bad.Model = candidateRun
	if _, err := suiteNativeDigest("kmx.native-orka.revision/v1", bad); err == nil {
		t.Fatal("candidate run exception exempted model metadata")
	}
	bad = p.Subject
	bad.PhysicalMember = "external"
	if _, err := suiteNativeDigest("kmx.native-orka.revision/v1", bad); err == nil {
		t.Fatal("candidate run exception exempted an invalid projection")
	}
	if _, err := suiteNativeDigest("kmx.native-orka.revision/v1", map[string]any{"EvaluationRun": candidateRun}); err == nil {
		t.Fatal("untyped run metadata bypassed credential scan")
	}
}

func TestReleaseLiftGateCandidateRequiresExplicitPromotion(t *testing.T) {
	for _, tc := range []string{"strict", "promote", "missing model", "different model", "occupied", "package", "model", "profile", "runtime", "physical"} {
		t.Run(tc, func(t *testing.T) {
			originalPath := os.Getenv("PATH")
			a, f, opt, _ := releaseLiftFixture(t)
			t.Setenv("PATH", originalPath)
			_, prepared, suite, placement, observations := passingFixture(t)
			candidate, err := a.prepareSuiteNativeCandidate(t.Context(), suite, OrkaNamespace, "inference", candidateRun)
			if err != nil {
				t.Fatal(err)
			}
			placement.Subject = candidate.Subject
			r, err := newSuitePassingCandidateReceipt(t.Context(), prepared, placement, observations, candidateRun)
			if err != nil {
				t.Fatal(err)
			}
			opt.Promote = tc != "strict"
			for _, kind := range []string{"Provider", "Agent"} {
				if tc != "occupied" {
					delete(f.objects, orkaPlural(kind)+"/"+suite.MemberID)
				}
			}
			provider := f.objects[orkaPlural("Provider")+"/inference"]["spec"].(map[string]any)
			provider["defaultModel"] = suite.Model
			switch tc {
			case "missing model":
				delete(provider, "defaultModel")
			case "different model":
				provider["defaultModel"] = "another-model"
			case "package":
				r.Subject.MemberDigest = "sha256:" + strings.Repeat("a", 64)
			case "model":
				r.Subject.Model = "another-model"
			case "profile":
				r.Subject.BuildProfileDigest = "sha256:" + strings.Repeat("a", 64)
			case "runtime":
				r.Subject.Runtime.WorkerAccount = "another-worker"
			case "physical":
				r.Subject.PhysicalMember = "external"
			}
			raw, err := suitecore.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(opt.Evidence, raw, 0600); err != nil {
				t.Fatal(err)
			}
			plan, err := a.PrepareReleaseLift(t.Context(), opt)
			if tc != "promote" {
				if !errors.Is(err, ErrReleaseLift) || plan != nil {
					t.Fatalf("unsafe gate accepted: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if plan.Summary().Gate != "promotion" || plan.native.Subject.PhysicalMember != "" || !reflect.DeepEqual(plan.receipt.Subject, r.Subject) {
				t.Fatal("candidate evidence rewritten or exact reuse allowed")
			}
		})
	}
}
