package lifecycle

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	kmx "github.com/kaimahi-agents/kaimahi/internal/kmx/lifecycle/model"
)

const renderedDocumentPattern = "rendered/%03d.yaml"
const deploymentDocumentPattern = "deployment/%03d-%s.yaml"

type RuntimeBuildInput struct {
	operation  kmx.OperationID
	deployable kmx.Deployable
	runtime    kmx.RuntimeRef
	binding    kmx.TargetBinding
}

func NewRuntimeBuildInput(operation kmx.OperationID, deployable kmx.Deployable, runtime kmx.RuntimeRef, binding kmx.TargetBinding) (RuntimeBuildInput, error) {
	if err := operation.Validate(); err != nil {
		return RuntimeBuildInput{}, err
	}
	if err := deployable.Ref().Validate(); err != nil {
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
	return RuntimeBuildInput{operation: operation, deployable: deployable, runtime: runtime, binding: binding}, nil
}

func (i RuntimeBuildInput) Operation() kmx.OperationID { return i.operation }
func (i RuntimeBuildInput) Deployable() kmx.Deployable { return i.deployable }
func (i RuntimeBuildInput) Runtime() kmx.RuntimeRef    { return i.runtime }
func (i RuntimeBuildInput) Binding() kmx.TargetBinding { return i.binding }

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

type RuntimeArtifact struct {
	operation      kmx.OperationID
	runtime        kmx.RuntimeRef
	deployable     kmx.DeployableRef
	bindingDigest  kmx.Digest
	renderedDigest kmx.Digest
	deployDigest   kmx.Digest
	documents      []RuntimeDocument
}

func NewRuntimeArtifact(input RuntimeBuildInput, documents []RuntimeDocument) (RuntimeArtifact, error) {
	if err := input.operation.Validate(); err != nil {
		return RuntimeArtifact{}, err
	}
	if err := input.runtime.Validate(); err != nil {
		return RuntimeArtifact{}, err
	}
	if err := input.deployable.Ref().Validate(); err != nil {
		return RuntimeArtifact{}, err
	}
	if input.binding.Digest().IsZero() {
		return RuntimeArtifact{}, fmt.Errorf("runtime artifact binding digest is required")
	}
	if len(documents) == 0 {
		return RuntimeArtifact{}, fmt.Errorf("runtime artifact requires at least one document")
	}
	copied := make([]RuntimeDocument, len(documents))
	rendered := make([][]byte, len(documents))
	deployFrames := make([]digestFrame, len(documents))
	deployable := false
	for i, document := range documents {
		if len(document.data) == 0 {
			return RuntimeArtifact{}, fmt.Errorf("runtime artifact document %d is empty", i)
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
		return RuntimeArtifact{}, fmt.Errorf("runtime artifact has no deployable document")
	}
	digest, err := renderedDigest(rendered)
	if err != nil {
		return RuntimeArtifact{}, err
	}
	deployDigest, err := framedDigest(deployFrames)
	if err != nil {
		return RuntimeArtifact{}, err
	}
	return RuntimeArtifact{
		operation:      input.operation,
		runtime:        input.runtime,
		deployable:     input.deployable.Ref(),
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

func (a RuntimeArtifact) Operation() kmx.OperationID    { return a.operation }
func (a RuntimeArtifact) Runtime() kmx.RuntimeRef       { return a.runtime }
func (a RuntimeArtifact) Deployable() kmx.DeployableRef { return a.deployable }
func (a RuntimeArtifact) BindingDigest() kmx.Digest     { return a.bindingDigest }
func (a RuntimeArtifact) RenderedDigest() kmx.Digest    { return a.renderedDigest }
func (a RuntimeArtifact) DeployDigest() kmx.Digest      { return a.deployDigest }

func (a RuntimeArtifact) Documents() []RuntimeDocument {
	documents := make([]RuntimeDocument, len(a.documents))
	for i, document := range a.documents {
		documents[i] = RuntimeDocument{data: append([]byte(nil), document.data...), deployable: document.deployable}
	}
	return documents
}

func (a RuntimeArtifact) DeployDocuments() [][]byte {
	var documents [][]byte
	for _, document := range a.documents {
		if document.deployable {
			documents = append(documents, append([]byte(nil), document.data...))
		}
	}
	return documents
}

func (RuntimeArtifact) MarshalJSON() ([]byte, error) {
	return nil, fmt.Errorf("RuntimeArtifact is an internal in-process value")
}

func (*RuntimeArtifact) UnmarshalJSON([]byte) error {
	return fmt.Errorf("RuntimeArtifact is an internal in-process value")
}

var _ json.Marshaler = RuntimeArtifact{}
