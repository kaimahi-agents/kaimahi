package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// fakeLifecycleAdapter is a minimal LifecycleAdapter implementation used
// only to prove the interface shape compiles and dispatches through the
// embedded Adapter and the reused AgentRef/Capabilities types — no real
// runtime behavior is exercised here.
type fakeLifecycleAdapter struct{ id ID }

func (a fakeLifecycleAdapter) ID() ID { return a.id }
func (a fakeLifecycleAdapter) Probe(context.Context, Target) (Probe, error) {
	return Probe{}, nil
}
func (a fakeLifecycleAdapter) Open(context.Context, Target) (Session, error) {
	return nil, nil
}
func (a fakeLifecycleAdapter) Capabilities() Capabilities {
	return Capabilities{Render: true, Deploy: true, Status: true, Evaluate: false}
}
func (a fakeLifecycleAdapter) Render(context.Context, PortableAgent, RenderOptions) (RenderedBundle, error) {
	return NewRenderedBundle(a.id, []byte("portable source"), []Document{ApplyDocument([]byte("rendered document"))}, nil, TargetResolution{Runtime: a.id, Namespace: "ns"}, nil)
}
func (a fakeLifecycleAdapter) Deploy(context.Context, RenderedBundle, DeployOptions) (AgentRef, error) {
	return AgentRef{Runtime: a.id, Namespace: "ns", Kind: "AgentInstance", Name: "hello", UID: "instance-uid"}, nil
}
func (a fakeLifecycleAdapter) Status(context.Context, AgentRef, StatusOptions) (LifecycleStatus, error) {
	return LifecycleStatus{Pair: PairStatus{DesiredRevision: "1"}}, nil
}
func (a fakeLifecycleAdapter) Evaluate(context.Context, AgentRef, EvaluationRequest) (EvaluationReceipt, error) {
	return EvaluationReceipt{}, &UnsupportedVerbError{Runtime: a.id, Verb: VerbEvaluate}
}

// LifecycleAdapter must embed Adapter, so any implementation satisfies both
// interfaces without a second registry/ID model.
func TestLifecycleAdapterEmbedsAdapter(t *testing.T) {
	var lifecycle LifecycleAdapter = fakeLifecycleAdapter{id: Kagent}
	var adapter Adapter = lifecycle
	if adapter.ID() != Kagent {
		t.Fatalf("ID() through the embedded Adapter = %q", adapter.ID())
	}
}

// Deploy/Status/Evaluate reuse PR #197's runtime-qualified AgentRef — no
// parallel identity type is introduced for lifecycle operations.
func TestLifecycleAdapterDeployStatusEvaluateReuseAgentRef(t *testing.T) {
	var lifecycle LifecycleAdapter = fakeLifecycleAdapter{id: KagentV1}

	ref, err := lifecycle.Deploy(context.Background(), RenderedBundle{}, DeployOptions{})
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if ref.Runtime != KagentV1 || ref.Kind != "AgentInstance" {
		t.Errorf("Deploy returned AgentRef = %+v", ref)
	}

	status, err := lifecycle.Status(context.Background(), ref, StatusOptions{})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Pair.DesiredRevision != "1" {
		t.Errorf("Status = %+v", status)
	}

	if _, err := lifecycle.Evaluate(context.Background(), ref, EvaluationRequest{}); err == nil {
		t.Fatal("expected Evaluate to report unsupported for this fake adapter")
	}
}

// Capabilities gains static lifecycle flags alongside #197's session flags —
// the same struct, not a second capability model.
func TestLifecycleCapabilitiesCarrySessionAndLifecycleFlags(t *testing.T) {
	caps := Capabilities{
		Streaming: true, Resume: true, Approvals: true,
		Render: true, Deploy: true, Status: true, Evaluate: true,
	}
	if !caps.Streaming || !caps.Render || !caps.Deploy || !caps.Status || !caps.Evaluate {
		t.Errorf("Capabilities did not carry every session and lifecycle flag: %+v", caps)
	}
}

// DESIGN.md §1: Status "reports template/Harness desired and latest-
// successful revision with conditions separately from AgentInstance
// state/prepared revision. It exposes no merged readiness boolean." Pair and
// Instance are therefore separate, independently populated fields.
func TestLifecycleStatusSeparatesPairFromInstance(t *testing.T) {
	pairOnly := LifecycleStatus{Pair: PairStatus{DesiredRevision: "3", LatestSuccessfulRevision: "2"}}
	if pairOnly.Instance != nil {
		t.Errorf("pair-only status must leave Instance nil, got %+v", pairOnly.Instance)
	}

	withInstance := LifecycleStatus{
		Pair:     PairStatus{DesiredRevision: "3", LatestSuccessfulRevision: "3"},
		Instance: &InstanceStatus{State: "Ready", PreparedRevision: "3"},
	}
	if withInstance.Instance == nil || withInstance.Instance.State != "Ready" {
		t.Errorf("status with an explicit instance selector = %+v", withInstance)
	}
	if withInstance.Pair.DesiredRevision != "3" {
		t.Errorf("populating Instance must not disturb Pair: %+v", withInstance.Pair)
	}
}

// The bundle's digests are checked against independently framed expectations
// — the exact strings DESIGN.md §2 specifies, hashed here in the test — not
// against the digest helpers the constructor itself calls.
func TestRenderedBundleComputesBothDigests(t *testing.T) {
	source := []byte("portable source bytes")
	docs := []Document{ApplyDocument([]byte("doc-0")), ApplyDocument([]byte("doc-1"))}

	bundle, err := NewRenderedBundle(Orka, source, docs, nil, TargetResolution{Runtime: Orka, Namespace: "ns"}, nil)
	if err != nil {
		t.Fatalf("NewRenderedBundle: %v", err)
	}
	if bundle.Adapter() != Orka {
		t.Errorf("Adapter() = %q", bundle.Adapter())
	}
	portableSum := sha256.Sum256([]byte("portable-agent.yaml 21\nportable source bytes\n"))
	if want := hex.EncodeToString(portableSum[:]); bundle.PortableDigest() != want {
		t.Errorf("PortableDigest() = %q, want %q", bundle.PortableDigest(), want)
	}
	renderedSum := sha256.Sum256([]byte("rendered/000.yaml 5\ndoc-0\nrendered/001.yaml 5\ndoc-1\n"))
	if want := hex.EncodeToString(renderedSum[:]); bundle.RenderedDigest() != want {
		t.Errorf("RenderedDigest() = %q, want %q", bundle.RenderedDigest(), want)
	}
	if bundle.Target().Namespace != "ns" {
		t.Errorf("Target() = %+v", bundle.Target())
	}
}

// Each digest must answer to its own input: changing the portable source
// alone, or one rendered byte alone, must move exactly one of them.
func TestRenderedBundleDigestsRespondToTheirOwnInput(t *testing.T) {
	build := func(t *testing.T, source, document string) RenderedBundle {
		t.Helper()
		bundle, err := NewRenderedBundle(Orka, []byte(source), []Document{ApplyDocument([]byte(document))}, nil, TargetResolution{}, nil)
		if err != nil {
			t.Fatalf("NewRenderedBundle: %v", err)
		}
		return bundle
	}
	base := build(t, "source A", "doc-0")
	otherSource := build(t, "source B", "doc-0")
	otherDocuments := build(t, "source A", "doc-1")

	if base.PortableDigest() == otherSource.PortableDigest() {
		t.Error("a different portable source produced the same portable digest")
	}
	if base.RenderedDigest() != otherSource.RenderedDigest() {
		t.Error("the portable source changed the rendered digest")
	}
	if base.RenderedDigest() == otherDocuments.RenderedDigest() {
		t.Error("different rendered bytes produced the same rendered digest")
	}
	if base.PortableDigest() != otherDocuments.PortableDigest() {
		t.Error("rendered bytes changed the portable digest")
	}
}

// DESIGN.md §2 digests "every byte of the final rendered documents", while
// Deploy may write only what the bundle explicitly marks for deployment.
func TestRenderedBundleSeparatesArtifactFromDeployDocuments(t *testing.T) {
	review := []byte("review only")
	apply := []byte("deploy me")
	bundle, err := NewRenderedBundle(Orka, []byte("source"), []Document{ReviewDocument(review), ApplyDocument(apply)}, nil, TargetResolution{}, nil)
	if err != nil {
		t.Fatalf("NewRenderedBundle: %v", err)
	}
	if len(bundle.Documents()) != 2 || !bytes.Equal(bundle.Documents()[0], review) {
		t.Fatalf("Documents() dropped the review-only document: %q", bundle.Documents())
	}
	deploy := bundle.DeployDocuments()
	if len(deploy) != 1 || !bytes.Equal(deploy[0], apply) {
		t.Fatalf("DeployDocuments() = %q, want only the applied document", deploy)
	}
	framed := sha256.Sum256([]byte("rendered/000.yaml 11\nreview only\nrendered/001.yaml 9\ndeploy me\n"))
	if want := hex.EncodeToString(framed[:]); bundle.RenderedDigest() != want {
		t.Errorf("RenderedDigest() = %q, want %q covering every rendered byte", bundle.RenderedDigest(), want)
	}
	deploy[0][0] = 'X'
	if !bytes.Equal(bundle.DeployDocuments()[0], apply) {
		t.Error("DeployDocuments() returned a shared, not a defensive, copy")
	}
}

func TestRenderedBundleRejectsMissingAdapterOrDocuments(t *testing.T) {
	if _, err := NewRenderedBundle("", []byte("source"), []Document{ApplyDocument([]byte("doc"))}, nil, TargetResolution{}, nil); err == nil {
		t.Error("expected an error for a missing adapter ID")
	}
	if _, err := NewRenderedBundle(Orka, []byte("source"), nil, nil, TargetResolution{}, nil); err == nil {
		t.Error("expected an error for zero rendered documents")
	}
	if _, err := NewRenderedBundle(Orka, []byte("source"), []Document{ApplyDocument(nil)}, nil, TargetResolution{}, nil); err == nil {
		t.Error("expected an error for an empty rendered document")
	}
	// An absent portable source would digest a constant that identifies no
	// authored behavior at all.
	if _, err := NewRenderedBundle(Orka, nil, []Document{ApplyDocument([]byte("doc"))}, nil, TargetResolution{}, nil); err == nil {
		t.Error("expected an error for an absent portable source")
	}
	// A bundle Deploy could only no-op on is never what an adapter meant.
	if _, err := NewRenderedBundle(Orka, []byte("source"), []Document{ReviewDocument([]byte("doc"))}, nil, TargetResolution{}, nil); err == nil {
		t.Error("expected an error for a bundle with no deployable document")
	}
}

// A RenderedBundle's documents must be immutable once constructed: neither
// mutating the caller's input slices after construction, nor mutating a
// slice returned by Documents(), may change what the bundle reports or
// digests.
func TestRenderedBundleDocumentsAreImmutable(t *testing.T) {
	doc := []byte("original document")
	docs := []Document{ApplyDocument(doc)}

	bundle, err := NewRenderedBundle(Orka, []byte("source"), docs, nil, TargetResolution{}, nil)
	if err != nil {
		t.Fatalf("NewRenderedBundle: %v", err)
	}
	originalDigest := bundle.RenderedDigest()

	// Mutate the caller's original input slices after construction.
	doc[0] = 'X'
	docs[0] = ApplyDocument([]byte("replaced"))

	// Mutate a slice returned by the accessor.
	returned := bundle.Documents()
	returned[0][0] = 'Y'
	returned[0] = []byte("also replaced")

	if got := bundle.RenderedDigest(); got != originalDigest {
		t.Errorf("RenderedDigest() changed after external mutation: %q vs %q", got, originalDigest)
	}
	if !bytes.Equal(bundle.Documents()[0], []byte("original document")) {
		t.Errorf("Documents()[0] = %q, want unaffected original content", bundle.Documents()[0])
	}
}

func TestRenderedBundlePrerequisitesAndLossesAreDefensivelyCopied(t *testing.T) {
	prereqs := []Prerequisite{{Kind: "Harness", Namespace: "ns", Name: "kagent"}}
	losses := []Loss{{Field: "spec.foo", Reason: "unsupported"}}

	bundle, err := NewRenderedBundle(KagentV1, []byte("source"), []Document{ApplyDocument([]byte("doc"))}, prereqs, TargetResolution{}, losses)
	if err != nil {
		t.Fatalf("NewRenderedBundle: %v", err)
	}

	prereqs[0].Name = "mutated"
	losses[0].Reason = "mutated"

	if bundle.Prerequisites()[0].Name != "kagent" {
		t.Errorf("Prerequisites() reflects post-construction mutation: %+v", bundle.Prerequisites())
	}
	if bundle.Losses()[0].Reason != "unsupported" {
		t.Errorf("Losses() reflects post-construction mutation: %+v", bundle.Losses())
	}

	bundle.Prerequisites()[0].Name = "also mutated"
	if bundle.Prerequisites()[0].Name != "kagent" {
		t.Errorf("mutating a returned Prerequisites() slice affected internal state: %+v", bundle.Prerequisites())
	}
}

// DESIGN.md §2: "Losses are normally empty because lossy mappings fail."
func TestRenderedBundleLossesAreEmptyByDefault(t *testing.T) {
	bundle, err := NewRenderedBundle(Orka, []byte("source"), []Document{ApplyDocument([]byte("doc"))}, nil, TargetResolution{}, nil)
	if err != nil {
		t.Fatalf("NewRenderedBundle: %v", err)
	}
	if len(bundle.Losses()) != 0 {
		t.Errorf("Losses() = %+v, want empty", bundle.Losses())
	}
}
