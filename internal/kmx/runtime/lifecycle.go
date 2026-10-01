// Lifecycle contracts. LifecycleAdapter is the sibling of the chat Adapter:
// a runtime that can render, deploy, report on, or evaluate an agent embeds
// Adapter and reuses the same ID, AgentRef and Status types rather than
// introducing a parallel identity model. Nothing here knows about Kubernetes:
// Render takes a target-bound portable document whose exact source was checked
// before the adapter received it.
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
	// ConsumedExtensions declares the runtime-specific behavior Render honors.
	ConsumedExtensions() []ID
	// Render takes only a document prepared for this adapter. Its source bytes
	// remain the authoritative portable-identity input.
	Render(context.Context, *PreparedPortableRender, RenderOptions) (RenderedBundle, error)
	// Deploy uses Render's bundle without re-rendering; a reconciler may add
	// ownership metadata to the write payload without changing the digest.
	Deploy(context.Context, RenderedBundle, DeployOptions) (DeployResult, error)
	Status(context.Context, AgentRef, StatusOptions) (Status, error)
	Evaluate(context.Context, AgentRef, EvaluationRequest) (EvaluationReceipt, error)
}

// DeployOptions keeps the create-only path as the zero value. Reconcile is
// desired-state deployment; runtimes that do not support it must refuse it.
type RenderOptions struct{}
type DeployOptions struct{ Reconcile bool }
type StatusOptions struct{}

// DeployResult carries a ready deployment's reference and history data. A
// failed deploy has no receipt, even if some resources were written.
type DeployResult struct {
	Ref     AgentRef
	Receipt DeployReceipt
}

type DeployTarget struct {
	Runtime            ID
	Context, Namespace string
}

type ResourceOutcome string

const (
	ResourceCreated ResourceOutcome = "created"
	ResourceReused  ResourceOutcome = "reused"
	ResourceUpdated ResourceOutcome = "updated"
	ResourceAdopted ResourceOutcome = "adopted"
)

type ResourceResult struct {
	Kind, Name, Namespace, UID string
	Generation                 int64
	Outcome                    ResourceOutcome
}

type DeployReceipt struct {
	Bundle, PortableDigest, RenderedDigest string
	Target                                 DeployTarget
	Resources                              []ResourceResult
}

// EvaluationRequest carries one evaluation case: identity, input, the
// assertions the terminal answer must satisfy, and the portable digest of the
// revision it must run against. Results are bound to that digest, so an
// adapter refuses a request whose target is not deployed at exactly it, and
// refuses one that names no revision at all.
type EvaluationRequest struct {
	CaseID         string
	Input          string
	ExpectContains []string
	PortableDigest string
}

// EvaluationVerdict is one case's outcome. Unknown is neither a pass nor a
// failure: the case ran, or may have, but its outcome could not be observed.
type EvaluationVerdict string

const (
	EvaluationPass    EvaluationVerdict = "pass"
	EvaluationFail    EvaluationVerdict = "fail"
	EvaluationUnknown EvaluationVerdict = "unknown"
)

// EvaluationReceipt is one case's recorded outcome. It deliberately carries
// no answer text: AnswerSHA256 is the only trace of what the agent said.
// Receipts remain local, not committed beside a public bundle. Matched and
// Missing partition ExpectContains only when an answer was read; TaskName and
// TaskUID name the one execution this case caused, and TaskUID is empty when the
// create itself was ambiguous. Detail explains a fail or unknown verdict.
type EvaluationReceipt struct {
	CaseID           string
	Verdict          EvaluationVerdict
	Matched, Missing []string
	TaskName         string
	TaskUID          string
	AnswerSHA256     string
	Detail           string
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
