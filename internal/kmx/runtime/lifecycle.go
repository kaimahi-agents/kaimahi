// Lifecycle contracts. LifecycleAdapter is the sibling of the chat Adapter:
// a runtime that can render, deploy, report on, or evaluate an agent embeds
// Adapter and reuses the same ID, AgentRef and Status types rather than
// introducing a parallel identity model. Nothing here knows about Kubernetes,
// a document schema, or any specific platform: Render takes the exact
// validated source bytes a caller already holds.
package runtime

import (
	"context"
	"fmt"
)

// LifecycleAdapter declares its supported verbs statically through
// Capabilities; a verb it does not declare returns *UnsupportedVerbError
// rather than being attempted.
type LifecycleAdapter interface {
	Adapter
	Capabilities() Capabilities
	// Render consumes the exact prevalidated source bytes. Those bytes are
	// the authoritative identity input — they are what the portable digest
	// covers — so an adapter may decode them to learn what to render, but it
	// must never render from a re-serialized copy of them: the identity would
	// then be the renderer's spelling of the document rather than the
	// document the caller handed in.
	Render(context.Context, []byte, RenderOptions) (RenderedBundle, error)
	// Deploy applies exactly the bundle Render produced, never a re-render.
	Deploy(context.Context, RenderedBundle, DeployOptions) (AgentRef, error)
	Status(context.Context, AgentRef, StatusOptions) (Status, error)
	Evaluate(context.Context, AgentRef, EvaluationRequest) (EvaluationReceipt, error)
}

// RenderOptions, DeployOptions and StatusOptions are reserved for
// adapter-specific tuning. They define no fields yet: adapter-local knobs stay
// in the owning adapter, and fields can be added later without changing any
// method signature.
type (
	RenderOptions struct{}
	DeployOptions struct{}
	StatusOptions struct{}
)

// EvaluationRequest carries one evaluation case: identity, input, and the
// assertions the terminal answer must satisfy.
type EvaluationRequest struct {
	CaseID         string
	Input          string
	ExpectContains []string
}

// EvaluationReceipt is Evaluate's result. It stays minimal until a receipt
// schema exists, fixing the call shape without inventing untested fields.
type EvaluationReceipt struct {
	CaseID  string
	Verdict string
}

// Document is one rendered document: the exact bytes plus whether Deploy may
// apply them. Both kinds are rendered output and both are covered by the
// rendered digest; they differ only in what Deploy is allowed to do. A
// review-only document is one an adapter renders for a human to read but must
// never write — a value-free credential skeleton naming a prerequisite an
// operator provisions separately, which applying would create empty.
type Document struct {
	bytes      []byte
	deployable bool
}

// ApplyDocument is a rendered document Deploy applies, in the order given.
func ApplyDocument(document []byte) Document {
	return Document{bytes: copyBytes(document), deployable: true}
}

// ReviewDocument is a rendered document that belongs to the artifact and the
// rendered digest but is never written to a cluster.
func ReviewDocument(document []byte) Document {
	return Document{bytes: copyBytes(document)}
}

// Bytes returns a copy of the document's exact rendered bytes.
func (d Document) Bytes() []byte { return copyBytes(d.bytes) }

// Deployable reports whether Deploy may apply this document.
func (d Document) Deployable() bool { return d.deployable }

// RenderedBundle is one adapter's exact rendered output: every document
// byte-for-byte in artifact order, which of them Deploy applies, and both
// identity digests. It is constructed only through NewRenderedBundle and every
// accessor returns a copy, so no caller can mutate a bundle after rendering.
type RenderedBundle struct {
	adapter                        ID
	documents                      []Document
	portableDigest, renderedDigest string
}

// NewRenderedBundle copies its inputs and computes both digests. source must
// be the exact validated source bytes Render consumed: an empty source has no
// identity to digest and is refused rather than hashed into a constant.
// documents must already be in final artifact order, and at least one must be
// deployable, because a bundle Deploy could only no-op on is never what an
// adapter meant to render.
func NewRenderedBundle(adapter ID, source []byte, documents []Document) (RenderedBundle, error) {
	if adapter == "" {
		return RenderedBundle{}, fmt.Errorf("rendered bundle: adapter ID is required")
	}
	if len(source) == 0 {
		return RenderedBundle{}, fmt.Errorf("rendered bundle: the exact source bytes are required for the portable digest")
	}
	if len(documents) == 0 {
		return RenderedBundle{}, fmt.Errorf("rendered bundle: at least one rendered document is required")
	}
	deployable := false
	rendered := make([][]byte, len(documents))
	for i, document := range documents {
		if len(document.bytes) == 0 {
			return RenderedBundle{}, fmt.Errorf("rendered bundle: document %d is empty", i)
		}
		rendered[i] = document.bytes
		deployable = deployable || document.deployable
	}
	if !deployable {
		return RenderedBundle{}, fmt.Errorf("rendered bundle: no document is marked for deployment")
	}
	return RenderedBundle{
		adapter:        adapter,
		documents:      append([]Document(nil), documents...),
		portableDigest: PortableBundleDigest(source),
		renderedDigest: RenderedBundleDigest(rendered),
	}, nil
}

// AdapterID returns the ID of the runtime that rendered this bundle.
func (b RenderedBundle) AdapterID() ID { return b.adapter }

// Documents returns every rendered document in artifact order: exactly the
// bytes the emitted artifact carries and the rendered digest covers,
// review-only documents included. Deploy applies DeployDocuments, not these.
func (b RenderedBundle) Documents() [][]byte { return b.copyDocuments(false) }

// DeployDocuments returns exactly the documents Deploy applies, in deployment
// order. It is the only list a Deploy implementation may write.
func (b RenderedBundle) DeployDocuments() [][]byte { return b.copyDocuments(true) }

func (b RenderedBundle) copyDocuments(deployableOnly bool) [][]byte {
	out := make([][]byte, 0, len(b.documents))
	for _, document := range b.documents {
		if deployableOnly && !document.deployable {
			continue
		}
		out = append(out, document.Bytes())
	}
	return out
}

// PortableDigest is the digest of the source bytes Render consumed.
func (b RenderedBundle) PortableDigest() string { return b.portableDigest }

// RenderedDigest is the digest of every rendered document in artifact order,
// review-only documents included.
func (b RenderedBundle) RenderedDigest() string { return b.renderedDigest }

func copyBytes(data []byte) []byte {
	if data == nil {
		return nil
	}
	return append([]byte(nil), data...)
}
