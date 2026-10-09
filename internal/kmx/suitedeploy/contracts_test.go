package suitedeploy

import (
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
)

func descriptor(media string) agentsuite.Descriptor {
	return agentsuite.Descriptor{MediaType: media, Digest: "sha256:" + strings.Repeat("a", 64), Size: 1}
}

func fixture() (Request, Installation, []Document) {
	format := VersionedContract{Name: "example.binding", Version: "1"}
	execution := VersionedContract{Name: "example.execution", Version: "1"}
	suite := Suite{Artifact: Reference{Location: "registry.example/suite", Descriptor: descriptor("application/vnd.oci.image.manifest.v1+json")}, LogicalDigest: "sha256:" + strings.Repeat("b", 64), Agents: []Agent{{Manifest: agentsuite.Agent{ID: "writer"}}, {Manifest: agentsuite.Agent{ID: "reviewer"}}}}
	image := BuildOutput{Agent: "writer", SuiteManifest: suite.Artifact.Descriptor, Image: Reference{Location: "registry.example/writer", Descriptor: descriptor("application/vnd.oci.image.manifest.v1+json")}, Composition: descriptor(agentsuite.MediaTypeComposition), BuildProfile: descriptor(agentsuite.MediaTypeBuildProfile), Binding: descriptor(agentsuite.MediaTypeSandboxBinding), ExecutionDescriptor: descriptor("application/vnd.example.execution+json"), Conformance: "conformant", Execution: Execution{Schema: execution, Modes: []Mode{{Name: "service", Protocol: VersionedContract{Name: "example.http", Version: "1"}, Lifecycle: "service"}}}}
	binding := TargetBinding{Format: format, Digest: "sha256:" + strings.Repeat("c", 64), Payload: []byte(`{"secretRef":"model-key"}`)}
	image.Platform = agentsuite.Platform{OS: "linux", Architecture: "amd64"}
	image.Execution.HarnessABI = VersionedContract{Name: "example.harness", Version: "1"}
	request := Request{OperationID: "op-1", Instance: "editorial", Suite: suite, Target: Target{AccessContext: "kind-dev", ClusterUID: "cluster-1", Namespace: "agents", Runtime: "example"}, Selections: []Selection{{Agent: "writer", Kind: "image", Image: &image, Mode: "service", Binding: binding}, {Agent: "reviewer", Kind: "native", Binding: binding}}}
	installation := Installation{Runtime: "example", Version: "1.2.3", Identity: "installation-1", CapabilityRevision: "capabilities-1", ExecutionContracts: []VersionedContract{execution}, BindingFormats: []VersionedContract{format}, Reconcile: true}
	installation.HarnessABIs = []VersionedContract{image.Execution.HarnessABI}
	installation.Protocols = []VersionedContract{image.Execution.Modes[0].Protocol}
	installation.Lifecycles = []string{"service"}
	return request, installation, []Document{{ID: "writer", Apply: true, Bytes: []byte(`{"name":"writer"}`)}, {ID: "reviewer", Apply: true, Bytes: []byte(`{"name":"reviewer"}`)}}
}

func TestWholeSuiteSelectionAndCompatibility(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*Request, *Installation)
	}{
		{"missing member", func(r *Request, _ *Installation) { r.Selections = r.Selections[:1] }},
		{"duplicate member", func(r *Request, _ *Installation) { r.Selections[1].Agent = "writer" }},
		{"undeclared member", func(r *Request, _ *Installation) { r.Selections[1].Agent = "other" }},
		{"native discards image", func(r *Request, _ *Installation) { r.Selections[0].Kind = "native" }},
		{"wrong suite", func(r *Request, _ *Installation) { r.Selections[0].Image.SuiteManifest.Digest = r.Suite.LogicalDigest }},
		{"experimental image", func(r *Request, _ *Installation) { r.Selections[0].Image.Conformance = "experimental" }},
		{"unsupported mode", func(r *Request, _ *Installation) { r.Selections[0].Mode = "session" }},
		{"unsupported binding", func(_ *Request, i *Installation) { i.BindingFormats = nil }},
		{"unsupported execution", func(_ *Request, i *Installation) { i.ExecutionContracts = nil }},
		{"unsupported harness", func(_ *Request, i *Installation) { i.HarnessABIs = nil }},
		{"unsupported protocol", func(_ *Request, i *Installation) { i.Protocols = nil }},
		{"unsupported lifecycle", func(_ *Request, i *Installation) { i.Lifecycles = nil }},
		{"duplicate mode", func(r *Request, _ *Installation) {
			e := &r.Selections[0].Image.Execution
			e.Modes = append(e.Modes, e.Modes[0])
		}},
		{"wrong image media type", func(r *Request, _ *Installation) {
			r.Selections[0].Image.Image.Descriptor.MediaType = "application/json"
		}},
		{"unsupported capability", func(r *Request, _ *Installation) {
			r.Selections[0].Image.Execution.RequiredCapabilities = []VersionedContract{{Name: "delegation", Version: "1"}}
		}},
		{"create only", func(r *Request, i *Installation) { r.Reconcile = true; i.Reconcile = false }},
		{"wrong runtime", func(_ *Request, i *Installation) { i.Runtime = "different" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			r, i, docs := fixture()
			test.change(&r, &i)
			if _, err := NewPlan(r, i, docs); err == nil {
				t.Fatal("accepted incompatible whole-suite plan")
			}
		})
	}
}

func TestPlanFreezesInputsAndBindsDisposition(t *testing.T) {
	r, i, docs := fixture()
	plan, err := NewPlan(r, i, docs)
	if err != nil {
		t.Fatal(err)
	}
	original := plan.Summary().Digest
	r.Selections[0].Image.Image.Descriptor.Digest = "mutated"
	i.ExecutionContracts[0].Name = "mutated"
	docs[0].Bytes[0] = '!'
	copyDocs := plan.Documents()
	copyDocs[0].Bytes[0] = '!'
	summary := plan.Summary()
	summary.Members[0].Image.Digest = "mutated"
	if plan.Summary().Digest != original || plan.Documents()[0].Bytes[0] != '{' || plan.Summary().Members[0].Image.Digest == "mutated" {
		t.Fatal("mutable plan")
	}
	r, i, docs = fixture()
	docs[1].Apply = false
	changed, err := NewPlan(r, i, docs)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Summary().Digest == original {
		t.Fatal("apply/review disposition missing from identity")
	}
	docs[1].ID = docs[0].ID
	if _, err := NewPlan(r, i, docs); err == nil {
		t.Fatal("duplicate resource accepted")
	}
}

func TestSameAdapterContractForLocalAndRemote(t *testing.T) {
	r, i, docs := fixture()
	local, err := NewPlan(r, i, docs)
	if err != nil {
		t.Fatal(err)
	}
	r.Target.AccessContext = "production-aks"
	r.Target.ClusterUID = "cluster-2"
	remote, err := NewPlan(r, i, docs)
	if err != nil {
		t.Fatal(err)
	}
	if local.Summary().Digest == remote.Summary().Digest {
		t.Fatal("target identity omitted")
	}
	if local.Summary().Target.Runtime != remote.Summary().Target.Runtime {
		t.Fatal("location changed runtime")
	}
}

func TestAuthorizationPinsReviewAndGate(t *testing.T) {
	r, i, docs := fixture()
	plan, err := NewPlan(r, i, docs)
	if err != nil {
		t.Fatal(err)
	}
	digest := plan.Summary().Digest
	for _, verdict := range []string{"refused", "unknown", "pass"} {
		if _, err := Authorize(plan, GateDecision{PlanDigest: digest, Verdict: verdict}, digest); err == nil {
			t.Fatalf("accepted %s without evidence", verdict)
		}
	}
	auth, err := Authorize(plan, GateDecision{PlanDigest: digest, Verdict: "not-required"}, digest)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.Validate(plan); err != nil {
		t.Fatal(err)
	}
	r.Target.ClusterUID = "different"
	other, err := NewPlan(r, i, docs)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.Validate(other); err == nil {
		t.Fatal("authorization reused across targets")
	}
	if _, err := Authorize(plan, GateDecision{PlanDigest: "different", Verdict: "not-required"}, digest); err == nil {
		t.Fatal("stale gate accepted")
	}
}
