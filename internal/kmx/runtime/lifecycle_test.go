package runtime

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

// exampleAdapter is a runtime that knows nothing about any cluster: it proves
// the lifecycle contract is satisfiable through embedding alone.
type exampleAdapter struct{ capabilities Capabilities }

func (exampleAdapter) ID() ID                                        { return testRuntime }
func (exampleAdapter) Probe(context.Context, Target) (Probe, error)  { return Probe{}, nil }
func (exampleAdapter) Open(context.Context, Target) (Session, error) { return nil, nil }
func (a exampleAdapter) Capabilities() Capabilities                  { return a.capabilities }
func (a exampleAdapter) Render(_ context.Context, source []byte, _ RenderOptions) (RenderedBundle, error) {
	return NewRenderedBundle(a.ID(), source, []Document{ApplyDocument(source)})
}
func (a exampleAdapter) Deploy(_ context.Context, bundle RenderedBundle, _ DeployOptions) (AgentRef, error) {
	return AgentRef{Runtime: a.ID(), Namespace: "example", Name: "agent", UID: bundle.RenderedDigest()}, nil
}
func (a exampleAdapter) Status(_ context.Context, ref AgentRef, _ StatusOptions) (Status, error) {
	return Status{Agent: ref, Fields: []Field{{Label: "Runtime", Value: string(ref.Runtime)}}}, nil
}
func (a exampleAdapter) Evaluate(context.Context, AgentRef, EvaluationRequest) (EvaluationReceipt, error) {
	return EvaluationReceipt{}, &UnsupportedVerbError{Runtime: a.ID(), Verb: VerbEvaluate}
}

// A LifecycleAdapter is a chat Adapter: identity, probing and opening are
// reused, and the lifecycle verbs carry the chat seam's AgentRef and Status.
func TestLifecycleAdapterEmbedsAdapterAndReusesIdentity(t *testing.T) {
	var lifecycle LifecycleAdapter = exampleAdapter{capabilities: Capabilities{Render: true, Deploy: true, Status: true}}
	var adapter Adapter = lifecycle
	if adapter.ID() != testRuntime {
		t.Fatalf("embedded Adapter.ID() = %q, want %q", adapter.ID(), testRuntime)
	}

	ctx := context.Background()
	bundle, err := lifecycle.Render(ctx, []byte("source"), RenderOptions{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	ref, err := lifecycle.Deploy(ctx, bundle, DeployOptions{})
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	status, err := lifecycle.Status(ctx, ref, StatusOptions{})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Agent != ref {
		t.Fatalf("Status.Agent = %+v, want the deployed AgentRef %+v", status.Agent, ref)
	}

	var unsupported *UnsupportedVerbError
	if _, err = lifecycle.Evaluate(ctx, ref, EvaluationRequest{CaseID: "c1", Input: "hi", ExpectContains: []string{"hello"}}); !errors.As(err, &unsupported) {
		t.Fatalf("Evaluate error = %v, want *UnsupportedVerbError", err)
	}
	if !lifecycle.Capabilities().Render || lifecycle.Capabilities().Evaluate {
		t.Fatalf("declared capabilities do not match the implemented verbs: %+v", lifecycle.Capabilities())
	}
}

func TestNewRenderedBundleRejectsUnusableInput(t *testing.T) {
	source := []byte("source")
	for _, tc := range []struct {
		name      string
		adapter   ID
		source    []byte
		documents []Document
	}{
		{"empty adapter", "", source, []Document{ApplyDocument([]byte("a"))}},
		{"empty source", testRuntime, nil, []Document{ApplyDocument([]byte("a"))}},
		{"no documents", testRuntime, source, nil},
		{"empty document bytes", testRuntime, source, []Document{ApplyDocument(nil)}},
		{"no deployable document", testRuntime, source, []Document{ReviewDocument([]byte("a"))}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewRenderedBundle(tc.adapter, tc.source, tc.documents); err == nil {
				t.Fatal("NewRenderedBundle accepted input it must refuse")
			}
		})
	}
}

// Artifact order carries every rendered document, review-only ones included,
// and is what the rendered digest covers. Deploy sees only the deployable
// subset, so a review-only document can never be applied by mistake.
func TestRenderedBundleSeparatesArtifactOrderFromDeployment(t *testing.T) {
	documents := []Document{ApplyDocument([]byte("first")), ReviewDocument([]byte("review")), ApplyDocument([]byte("third"))}
	bundle, err := NewRenderedBundle(testRuntime, []byte("source"), documents)
	if err != nil {
		t.Fatalf("NewRenderedBundle: %v", err)
	}
	if bundle.AdapterID() != testRuntime {
		t.Fatalf("AdapterID() = %q, want %q", bundle.AdapterID(), testRuntime)
	}
	assertDocuments(t, "Documents", bundle.Documents(), "first", "review", "third")
	assertDocuments(t, "DeployDocuments", bundle.DeployDocuments(), "first", "third")

	if want := PortableBundleDigest([]byte("source")); bundle.PortableDigest() != want {
		t.Fatalf("PortableDigest = %q, want %q", bundle.PortableDigest(), want)
	}
	want := RenderedBundleDigest([][]byte{[]byte("first"), []byte("review"), []byte("third")})
	if bundle.RenderedDigest() != want {
		t.Fatalf("RenderedDigest = %q, want the digest over every rendered document %q", bundle.RenderedDigest(), want)
	}
}

func TestDocumentCopiesBytesAtConstructionAndOnAccess(t *testing.T) {
	raw := []byte("value")
	document := ApplyDocument(raw)
	raw[0] = 'X'
	if !document.Deployable() {
		t.Fatal("ApplyDocument must be deployable")
	}
	if got := string(document.Bytes()); got != "value" {
		t.Fatalf("Bytes() = %q after mutating the source slice, want %q", got, "value")
	}
	document.Bytes()[0] = 'X'
	if got := string(document.Bytes()); got != "value" {
		t.Fatalf("Bytes() = %q after mutating a returned copy, want %q", got, "value")
	}
	if ReviewDocument(raw).Deployable() {
		t.Fatal("ReviewDocument must never be deployable")
	}
}

func TestRenderedBundleIsImmutableAfterConstruction(t *testing.T) {
	source := []byte("source")
	documents := []Document{ApplyDocument([]byte("first")), ReviewDocument([]byte("review"))}
	bundle, err := NewRenderedBundle(testRuntime, source, documents)
	if err != nil {
		t.Fatalf("NewRenderedBundle: %v", err)
	}
	portable, rendered := bundle.PortableDigest(), bundle.RenderedDigest()

	source[0] = 'X'
	documents[0] = ApplyDocument([]byte("replaced"))
	bundle.Documents()[0][0] = 'X'
	bundle.DeployDocuments()[0][0] = 'X'

	assertDocuments(t, "Documents", bundle.Documents(), "first", "review")
	assertDocuments(t, "DeployDocuments", bundle.DeployDocuments(), "first")
	if bundle.PortableDigest() != portable || bundle.RenderedDigest() != rendered {
		t.Fatal("digests changed after construction")
	}
}

func assertDocuments(t *testing.T, accessor string, got [][]byte, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s() returned %d documents, want %d", accessor, len(got), len(want))
	}
	for i, expected := range want {
		if !bytes.Equal(got[i], []byte(expected)) {
			t.Fatalf("%s()[%d] = %q, want %q", accessor, i, got[i], expected)
		}
	}
}
