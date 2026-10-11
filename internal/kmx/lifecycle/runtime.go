package lifecycle

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/kaimahi-agents/kaimahi/pkg/kmx"
)

const renderedDocumentPattern = "rendered/%03d.yaml"
const deploymentDocumentPattern = "deployment/%03d-%s.yaml"

type RuntimeBuildInput struct {
	operation kmx.OperationID
	source    kmx.DeploymentSource
	runtime   kmx.RuntimeRef
	binding   kmx.TargetBinding
}

func NewRuntimeBuildInput(operation kmx.OperationID, source kmx.DeploymentSource, runtime kmx.RuntimeRef, binding kmx.TargetBinding) (RuntimeBuildInput, error) {
	if err := operation.Validate(); err != nil {
		return RuntimeBuildInput{}, err
	}
	if err := source.Ref().Validate(); err != nil {
		return RuntimeBuildInput{}, err
	}
	if err := runtime.Validate(); err != nil {
		return RuntimeBuildInput{}, err
	}
	if err := binding.Validate(); err != nil {
		return RuntimeBuildInput{}, err
	}
	if runtime.Target != binding.Target() {
		return RuntimeBuildInput{}, fmt.Errorf("runtime installation and target binding identify different targets")
	}
	return RuntimeBuildInput{operation: operation, source: source, runtime: runtime, binding: binding}, nil
}

func (i RuntimeBuildInput) Operation() kmx.OperationID   { return i.operation }
func (i RuntimeBuildInput) Source() kmx.DeploymentSource { return i.source }
func (i RuntimeBuildInput) Runtime() kmx.RuntimeRef      { return i.runtime }
func (i RuntimeBuildInput) Binding() kmx.TargetBinding   { return i.binding }

func (RuntimeBuildInput) MarshalJSON() ([]byte, error) {
	return nil, fmt.Errorf("RuntimeBuildInput is an internal in-process command")
}

func (*RuntimeBuildInput) UnmarshalJSON([]byte) error {
	return fmt.Errorf("RuntimeBuildInput is an internal in-process command")
}

type RuntimeDocument struct {
	data       []byte
	deployable bool
}

func ApplyDocument(data []byte) RuntimeDocument {
	return RuntimeDocument{data: append([]byte(nil), data...), deployable: true}
}

func ReviewDocument(data []byte) RuntimeDocument {
	return RuntimeDocument{data: append([]byte(nil), data...)}
}

func (d RuntimeDocument) Bytes() []byte    { return append([]byte(nil), d.data...) }
func (d RuntimeDocument) Deployable() bool { return d.deployable }

func (RuntimeDocument) MarshalJSON() ([]byte, error) {
	return nil, fmt.Errorf("RuntimeDocument is an internal in-process value")
}

func (*RuntimeDocument) UnmarshalJSON([]byte) error {
	return fmt.Errorf("RuntimeDocument is an internal in-process value")
}

type RuntimeBundle struct {
	operation      kmx.OperationID
	runtime        kmx.RuntimeRef
	source         kmx.DeploymentSourceRef
	bindingDigest  kmx.Digest
	renderedDigest kmx.Digest
	deployDigest   kmx.Digest
	documents      []RuntimeDocument
}

func NewRuntimeBundle(input RuntimeBuildInput, documents []RuntimeDocument) (RuntimeBundle, error) {
	if err := input.operation.Validate(); err != nil {
		return RuntimeBundle{}, err
	}
	if err := input.runtime.Validate(); err != nil {
		return RuntimeBundle{}, err
	}
	if err := input.source.Ref().Validate(); err != nil {
		return RuntimeBundle{}, err
	}
	if input.binding.Digest().IsZero() {
		return RuntimeBundle{}, fmt.Errorf("runtime bundle binding digest is required")
	}
	if len(documents) == 0 {
		return RuntimeBundle{}, fmt.Errorf("runtime bundle requires at least one document")
	}
	copied := make([]RuntimeDocument, len(documents))
	rendered := make([][]byte, len(documents))
	deployFrames := make([]digestFrame, len(documents))
	deployable := false
	for i, document := range documents {
		if len(document.data) == 0 {
			return RuntimeBundle{}, fmt.Errorf("runtime bundle document %d is empty", i)
		}
		deployable = deployable || document.deployable
		copied[i] = RuntimeDocument{data: append([]byte(nil), document.data...), deployable: document.deployable}
		rendered[i] = document.data
		disposition := "review"
		if document.deployable {
			disposition = "apply"
		}
		deployFrames[i] = digestFrame{
			path: fmt.Sprintf(deploymentDocumentPattern, i, disposition), data: document.data,
		}
	}
	if !deployable {
		return RuntimeBundle{}, fmt.Errorf("runtime bundle has no deployable document")
	}
	digest, err := renderedDigest(rendered)
	if err != nil {
		return RuntimeBundle{}, err
	}
	deployDigest, err := framedDigest(deployFrames)
	if err != nil {
		return RuntimeBundle{}, err
	}
	return RuntimeBundle{
		operation:      input.operation,
		runtime:        input.runtime,
		source:         input.source.Ref(),
		bindingDigest:  input.binding.Digest(),
		renderedDigest: digest,
		deployDigest:   deployDigest,
		documents:      copied,
	}, nil
}

func renderedDigest(documents [][]byte) (kmx.Digest, error) {
	frames := make([]digestFrame, len(documents))
	for i, document := range documents {
		frames[i] = digestFrame{path: fmt.Sprintf(renderedDocumentPattern, i), data: document}
	}
	return framedDigest(frames)
}

type digestFrame struct {
	path string
	data []byte
}

func framedDigest(entries []digestFrame) (kmx.Digest, error) {
	var frames bytes.Buffer
	for _, entry := range entries {
		fmt.Fprintf(&frames, "%s %d\n", entry.path, len(entry.data))
		frames.Write(entry.data)
		frames.WriteByte('\n')
	}
	sum := sha256.Sum256(frames.Bytes())
	return kmx.ParseDigest(hex.EncodeToString(sum[:]))
}

func (b RuntimeBundle) Operation() kmx.OperationID      { return b.operation }
func (b RuntimeBundle) Runtime() kmx.RuntimeRef         { return b.runtime }
func (b RuntimeBundle) Source() kmx.DeploymentSourceRef { return b.source }
func (b RuntimeBundle) BindingDigest() kmx.Digest       { return b.bindingDigest }
func (b RuntimeBundle) RenderedDigest() kmx.Digest      { return b.renderedDigest }
func (b RuntimeBundle) DeployDigest() kmx.Digest        { return b.deployDigest }

func (b RuntimeBundle) Documents() []RuntimeDocument {
	documents := make([]RuntimeDocument, len(b.documents))
	for i, document := range b.documents {
		documents[i] = RuntimeDocument{data: append([]byte(nil), document.data...), deployable: document.deployable}
	}
	return documents
}

func (b RuntimeBundle) DeployDocuments() [][]byte {
	var documents [][]byte
	for _, document := range b.documents {
		if document.deployable {
			documents = append(documents, append([]byte(nil), document.data...))
		}
	}
	return documents
}

func (RuntimeBundle) MarshalJSON() ([]byte, error) {
	return nil, fmt.Errorf("RuntimeBundle is an internal in-process value")
}

func (*RuntimeBundle) UnmarshalJSON([]byte) error {
	return fmt.Errorf("RuntimeBundle is an internal in-process value")
}

var _ json.Marshaler = RuntimeBundle{}
