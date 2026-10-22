package app

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestFlowCandidateIsolationAndCleanup(t *testing.T) {
	a, inv, _, opt := flowBindingFixture(t)
	s, err := a.prepareSuiteFlow(t.Context(), inv, opt)
	if err != nil {
		t.Fatal(err)
	}
	p := suiteFlowCandidateProjection{Version: flowCandidateVersion, Suite: inv.Digest, Member: "writer", Case: "facts", Run: 1, Step: "draft", Operation: strings.Repeat("a1", 16)}
	name, err := suiteFlowCandidateName(p.Suite, p.Member, p.Case, p.Run, p.Step, p.Operation)
	if err != nil || len(name) > 63 {
		t.Fatal("invalid candidate", err)
	}
	seen := map[string]bool{name: true}
	for _, change := range []func(*suiteFlowCandidateProjection){
		func(p *suiteFlowCandidateProjection) { p.Suite = "sha256:" + strings.Repeat("b", 64) },
		func(p *suiteFlowCandidateProjection) { p.Member = "reviewer" },
		func(p *suiteFlowCandidateProjection) { p.Case = "other" },
		func(p *suiteFlowCandidateProjection) { p.Run = 2 },
		func(p *suiteFlowCandidateProjection) { p.Step = "review" },
		func(p *suiteFlowCandidateProjection) { p.Operation = strings.Repeat("b2", 16) },
	} {
		next := p
		change(&next)
		n, err := suiteFlowCandidateName(next.Suite, next.Member, next.Case, next.Run, next.Step, next.Operation)
		if err != nil || seen[n] {
			t.Fatal("candidate tuple collision", err)
		}
		seen[n] = true
	}
	subject := s.members[0].subject
	for _, m := range s.members {
		if m.suite.MemberID == p.Member {
			subject = m.subject
		}
	}
	subject.PhysicalMember = name
	subject.EvaluationRun = p.Operation
	subject.FlowCandidate = &p
	subject.BindingDigest = s.bindingDigest
	subject.RenderedDigest = strings.Repeat("2", 64)
	owner := suiteNativeOwner(subject)
	ann := owner.markers()
	if ann[suiteCandidateRunMarker] != p.Operation || ann[flowCandidateMarker] == nil {
		t.Fatal("flow owner lost markers")
	}
	if err := owner.check(ann); err != nil {
		t.Fatal("flow owner rejects its own tuple", err)
	}
	if !subject.validPhysicalMember() {
		t.Fatal("subject rejects physical tuple")
	}
	// Old discovery's existing run marker takes the candidate path, then its
	// old physical-name expectation fails; it cannot treat this as canonical.
	oldName, err := suiteCandidatePhysicalName(p.Suite, p.Operation)
	if err != nil || oldName == name || ann[suiteCandidateRunMarker] == nil {
		t.Fatal("old binary could adopt flow root")
	}
	if validSuitePassingSubject(subject) {
		t.Fatal("legacy receipt accepted flow candidate")
	}
	if suiteCleanupOwner(ann) == nil {
		t.Fatal("flow discovery lost owner")
	}
	legacy := *owner
	legacy.FlowCandidate = nil
	if reflect.DeepEqual(legacy.markers(), ann) || legacy.check(ann) == nil {
		t.Fatal("legacy owner consumed flow projection")
	}
	for _, value := range []any{nil, "{}", `{"version":"unknown"}`} {
		bad := owner.markers()
		bad[flowCandidateMarker] = value
		if !hasOrkaSuiteReservation(bad) || suiteCleanupOwner(bad) != nil || owner.check(bad) == nil {
			t.Fatal("bad tuple authorized cleanup")
		}
	}
	changed := p
	changed.Case = "other"
	foreign := *owner
	foreign.FlowCandidate = &changed
	if owner.check(foreign.markers()) == nil {
		t.Fatal("foreign tuple authorized cleanup")
	}
	changed = p
	changed.Suite = "sha256:" + strings.Repeat("c", 64)
	foreign = *owner
	foreign.Digest = changed.Suite
	foreign.FlowCandidate = &changed
	if sameFlowOperationOwner(owner, &foreign) {
		t.Fatal("foreign suite joined operation deletion set")
	}
	changed = p
	changed.Member = "reviewer"
	foreign = *owner
	foreign.Member = changed.Member
	foreign.FlowCandidate = &changed
	if !sameFlowOperationOwner(owner, &foreign) {
		t.Fatal("same-suite member blocked operation")
	}
	doc := map[string]any{"metadata": map[string]any{"annotations": map[string]any{flowCandidateMarker: nil}}}
	if !statusIsCandidate(doc) {
		t.Fatal("malformed flow candidate counted as deployed")
	}
	if _, state := statusOwnership("orka", doc["metadata"].(map[string]any)); state != "unknown" {
		t.Fatal("malformed flow owner treated healthy")
	}
	raw, err := suiteNativeDigest("test", subject)
	if err != nil || raw == "" {
		t.Fatal("validated operation failed digest", err)
	}
	copyRaw, _ := json.Marshal(p)
	if !strings.Contains(string(copyRaw), p.Operation) {
		t.Fatal("tuple did not retain full operation")
	}
}

func TestFlowCandidateLegacyBytesAndCollision(t *testing.T) {
	a, inv, f, opt := flowBindingFixture(t)
	s, err := a.prepareSuiteFlow(t.Context(), inv, opt)
	if err != nil {
		t.Fatal(err)
	}
	native, err := a.prepareSuiteNative(t.Context(), &inv.Members[0], OrkaNamespace, "inference")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(native.Subject)
	if strings.Contains(string(raw), "FlowCandidate") || native.Subject.FlowCandidate != nil {
		t.Fatal("old canonical bytes changed")
	}
	if len(s.candidates) != 4 {
		t.Fatal("candidate inventory is incomplete")
	}
	projected, err := s.prepareCandidate(t.Context(), a, s.candidates[0])
	if err != nil {
		t.Fatal(err)
	}
	if projected.Subject.BindingDigest != s.bindingDigest || projected.Subject.FlowCandidate == nil || validSuitePassingSubject(projected.Subject) {
		t.Fatal("candidate gained legacy receipt authority or lost B")
	}
	if projected.bundle.Provider["spec"].(map[string]any)["defaultModel"] != "packaged-model" {
		t.Fatal("candidate changed semantic model")
	}
	// Only the physical name differs from the canonical rendering.
	if projected.Subject.PhysicalMember == projected.Subject.Member || !projected.Subject.validPhysicalMember() {
		t.Fatal("candidate physical projection invalid")
	}
	foreignTuple := s.candidates[0]
	foreignTuple.Operation = strings.Repeat("c3", 16)
	if _, err := s.prepareCandidate(t.Context(), a, foreignTuple); err == nil {
		t.Fatal("foreign operation projected")
	}

	provider := s.candidates[0]
	name, _ := suiteFlowCandidateName(provider.Suite, provider.Member, provider.Case, provider.Run, provider.Step, provider.Operation)
	// A frozen serving reference that collides with any tuple must refuse.
	s.members[0].subject.Binding.Provider.Name = name
	if s.prepareCandidates() != errSuiteFlow {
		t.Fatal("inference Provider collision accepted")
	}
	_ = f
}
