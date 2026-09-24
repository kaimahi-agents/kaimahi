// Lifecycle contracts: DESIGN.md §1 extends PR #197's chat/session Adapter
// with a sibling LifecycleAdapter for Render/Deploy/Status/Evaluate. It
// embeds Adapter (reusing ID/Probe/Open) and reuses #197's AgentRef as the
// single identity type across chat and lifecycle. Deploy/Status/Evaluate all
// take the same runtime-qualified AgentRef PR #197 already defined; no
// parallel identity or registry model is introduced.
package runtime

import (
	"context"
	"fmt"
)

// LifecycleAdapter is the sibling of #197's chat Adapter: a runtime that
// supports Render/Deploy/Status/Evaluate implements this in addition to (by
// embedding) Adapter. Capabilities is a static declaration — not dynamic
// plugin negotiation — and a verb a runtime declares unsupported returns
// UnsupportedVerbError instead of being called.
type LifecycleAdapter interface {
	Adapter
	Capabilities() Capabilities
	Render(context.Context, PortableAgent, RenderOptions) (RenderedBundle, error)
	Deploy(context.Context, RenderedBundle, DeployOptions) (AgentRef, error)
	Status(context.Context, AgentRef, StatusOptions) (LifecycleStatus, error)
	Evaluate(context.Context, AgentRef, EvaluationRequest) (EvaluationReceipt, error)
}

// RenderOptions is reserved for adapter-specific render tuning. W94 defines
// no fields yet; later tasks (Orka's dry-run/schema-target flags, kagent-v1's
// admission label) extend it without changing Render's shape.
type RenderOptions struct{}

// DeployOptions is reserved for adapter-specific deploy tuning, mirroring
// RenderOptions. W94 defines no fields yet.
type DeployOptions struct{}

// StatusOptions is reserved for adapter-specific status tuning. Which
// instance (if any) to additionally report comes from the AgentRef passed to
// Status — an empty AgentRef.UID means pair-only status — not a separate
// selector field here.
type StatusOptions struct{}

// PairStatus is the top-level deployable identity's status: for kagent-v1
// this is the AgentTemplate/Harness pair's desired and latest-successful
// revisions; Orka and legacy kagent, which have no template/instance split,
// still populate this with their own workload state via Fields. It never
// carries a merged readiness boolean — DESIGN.md §1 is explicit that Status
// "exposes no merged readiness boolean".
type PairStatus struct {
	DesiredRevision          string
	LatestSuccessfulRevision string
	Fields                   []Field
}

// InstanceStatus is one running instance's state and the revision it was
// prepared against. It is populated only when a caller selected a specific
// instance (via AgentRef.UID); the zero value never appears merged into
// PairStatus.
type InstanceStatus struct {
	State            string
	PreparedRevision string
	Fields           []Field
}

// LifecycleStatus separates pair status from optional instance status, so no
// adapter collapses them into a single boolean. Instance is nil whenever no
// specific instance was requested or found.
type LifecycleStatus struct {
	Pair     PairStatus
	Instance *InstanceStatus
}

// EvaluationRequest carries the inputs for one bound instance evaluation:
// the case identity, the input text, and the assertions its terminal answer
// must satisfy. Task 14 defines the full closed evaluation receipt pipeline
// (DESIGN.md §5); this only fixes Evaluate's call shape.
type EvaluationRequest struct {
	CaseID         string
	Input          string
	ExpectContains []string
}

// EvaluationReceipt is Evaluate's result shape. Task 14 defines the full
// closed kmx.kaimahi.dev/v1alpha1 evaluation receipt schema (DESIGN.md §5);
// keeping it minimal here fixes Evaluate's return type without inventing
// untested fields ahead of that work.
type EvaluationReceipt struct {
	CaseID  string
	Verdict string
}

// Prerequisite names one external object a rendered bundle depends on but
// does not itself emit — e.g. kagent-v1's Harness reference. Deploy
// validates it before applying rendered documents; it is metadata, not a
// rendered document, and never contributes to either identity digest.
type Prerequisite struct {
	Kind, Namespace, Name string
}

// TargetResolution records which runtime and namespace a rendered bundle
// targets. Like Prerequisite, it is metadata for Deploy, not digested
// content.
type TargetResolution struct {
	Runtime   ID
	Namespace string
}

// Loss records one field a strict portable decode should already have
// refused. DESIGN.md §2: "Losses are normally empty because lossy mappings
// fail" — this exists so a defensive Render still reports instead of
// silently dropping a field if that earlier validation is ever bypassed.
type Loss struct {
	Field, Reason string
}

// Document is one rendered document: the exact bytes the artifact carries,
// plus whether Deploy applies it. Both kinds are rendered output and both are
// covered by the rendered bundle digest; they differ only in what Deploy is
// allowed to do with them.
//
// A review-only document is one an adapter renders for a human to read but
// must never write — Orka's value-free Secret skeleton is the case this
// models: it names a prerequisite the operator provisions separately, and
// bulk-applying it would create an empty credential.
type Document struct {
	Bytes []byte
	Apply bool
}

// ApplyDocument is a rendered document Deploy applies, in the order given.
func ApplyDocument(bytes []byte) Document { return Document{Bytes: bytes, Apply: true} }

// ReviewDocument is a rendered document that belongs to the artifact and the
// rendered digest but is never written to a cluster.
func ReviewDocument(bytes []byte) Document { return Document{Bytes: bytes} }

// RenderedBundle is one adapter's exact rendered output: every target
// document byte-for-byte in artifact order, which of those documents Deploy
// applies, both identity digests (over the portable source and over these
// exact rendered bytes), the external prerequisites Deploy must validate,
// target resolution metadata, and any losses (normally empty). It is
// constructed only through NewRenderedBundle, which defensively copies its
// inputs and computes both digests; every accessor returns a defensive copy,
// so no caller can mutate a bundle after construction.
type RenderedBundle struct {
	adapter        ID
	documents      []Document
	portableDigest string
	renderedDigest string
	prerequisites  []Prerequisite
	target         TargetResolution
	losses         []Loss
}

// NewRenderedBundle defensively copies documents, prerequisites and losses,
// computes both identity digests — PortableBundleDigest over portable and
// RenderedBundleDigest over every document's bytes in artifact order — and
// returns an immutable RenderedBundle. portable must be the exact validated
// portable source bytes (PortableAgent.Source(), which is populated for both
// parsed and shorthand-encoded documents); an empty source has no identity to
// digest and is refused rather than hashed into a constant. documents must
// already be in final order, and at least one of them must be an
// ApplyDocument, because a bundle Deploy could only no-op on is never what an
// adapter meant to render.
func NewRenderedBundle(adapter ID, portable []byte, documents []Document, prerequisites []Prerequisite, target TargetResolution, losses []Loss) (RenderedBundle, error) {
	if adapter == "" {
		return RenderedBundle{}, fmt.Errorf("rendered bundle: adapter ID is required")
	}
	if len(portable) == 0 {
		return RenderedBundle{}, fmt.Errorf("rendered bundle: the exact portable source bytes are required for the portable digest")
	}
	if len(documents) == 0 {
		return RenderedBundle{}, fmt.Errorf("rendered bundle: at least one rendered document is required")
	}
	docsCopy := make([]Document, len(documents))
	applied := 0
	for i, doc := range documents {
		if len(doc.Bytes) == 0 {
			return RenderedBundle{}, fmt.Errorf("rendered bundle: document %d is empty", i)
		}
		cp := make([]byte, len(doc.Bytes))
		copy(cp, doc.Bytes)
		docsCopy[i] = Document{Bytes: cp, Apply: doc.Apply}
		if doc.Apply {
			applied++
		}
	}
	if applied == 0 {
		return RenderedBundle{}, fmt.Errorf("rendered bundle: no document is marked for deployment")
	}
	sourceCopy := make([]byte, len(portable))
	copy(sourceCopy, portable)

	bytesOnly := make([][]byte, len(docsCopy))
	for i, doc := range docsCopy {
		bytesOnly[i] = doc.Bytes
	}
	return RenderedBundle{
		adapter:        adapter,
		documents:      docsCopy,
		portableDigest: PortableBundleDigest(sourceCopy),
		renderedDigest: RenderedBundleDigest(bytesOnly),
		prerequisites:  append([]Prerequisite(nil), prerequisites...),
		target:         target,
		losses:         append([]Loss(nil), losses...),
	}, nil
}

// Adapter returns the ID of the runtime that produced this bundle.
func (b RenderedBundle) Adapter() ID { return b.adapter }

// Documents returns a defensive copy of every rendered document, in artifact
// order: exactly the bytes the emitted artifact carries and the rendered
// digest covers, review-only documents included. Deploy must not apply these;
// it applies DeployDocuments. Mutating the result never affects the bundle.
func (b RenderedBundle) Documents() [][]byte {
	return copyDocumentBytes(b.documents, false)
}

// DeployDocuments returns a defensive copy of exactly the documents Deploy
// applies, in deployment order. It is the only document list a Deploy
// implementation may write, so a review-only document (Orka's value-free
// Secret skeleton) can never be bulk-applied by mistake.
func (b RenderedBundle) DeployDocuments() [][]byte {
	return copyDocumentBytes(b.documents, true)
}

func copyDocumentBytes(documents []Document, deployOnly bool) [][]byte {
	out := make([][]byte, 0, len(documents))
	for _, doc := range documents {
		if deployOnly && !doc.Apply {
			continue
		}
		cp := make([]byte, len(doc.Bytes))
		copy(cp, doc.Bytes)
		out = append(out, cp)
	}
	return out
}

// PortableDigest returns the portable bundle digest computed at
// construction: lowercase 64-hex SHA-256 over the portable source, framed
// under "portable-agent.yaml".
func (b RenderedBundle) PortableDigest() string { return b.portableDigest }

// RenderedDigest returns the rendered bundle digest computed at
// construction: lowercase 64-hex SHA-256 over every rendered document,
// framed under numbered "rendered/%03d.yaml" logical paths in deployment
// order.
func (b RenderedBundle) RenderedDigest() string { return b.renderedDigest }

// Prerequisites returns a defensive copy of the external objects Deploy must
// validate before applying this bundle's documents.
func (b RenderedBundle) Prerequisites() []Prerequisite {
	return append([]Prerequisite(nil), b.prerequisites...)
}

// Target returns the runtime and namespace this bundle was rendered for.
func (b RenderedBundle) Target() TargetResolution { return b.target }

// Losses returns a defensive copy of any field this bundle's render step
// could not represent exactly. It is normally empty.
func (b RenderedBundle) Losses() []Loss {
	return append([]Loss(nil), b.losses...)
}
