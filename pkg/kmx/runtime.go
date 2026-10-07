package kmx

import (
	"context"
	"fmt"
)

type RuntimeDescriptor struct {
	ID          RuntimeID `json:"id"`
	DisplayName string    `json:"displayName"`
}

type Runtime interface {
	Describe() RuntimeDescriptor
}

type RuntimeOptions struct {
	Profile string `json:"profile,omitempty"`
}

type EnsureRuntimeRequest struct {
	Operation OperationID
	Target    TargetRef
	Options   RuntimeOptions
}

func (r EnsureRuntimeRequest) Validate() error {
	if err := validateIdentity("operation ID", string(r.Operation)); err != nil {
		return err
	}
	return r.Target.Validate()
}

// TargetBinding is versioned, opaque deployment input for one exact target.
// Generic KMX workflows include it in identity and pass it unchanged; only the
// selected runtime interprets its bytes.
type TargetBinding struct {
	target     TargetRef
	apiVersion string
	kind       string
	digest     Digest
	data       []byte
}

func NewTargetBinding(target TargetRef, apiVersion, kind string, data []byte) (TargetBinding, error) {
	if err := target.Validate(); err != nil {
		return TargetBinding{}, err
	}
	if err := validateIdentity("target binding API version", apiVersion); err != nil {
		return TargetBinding{}, err
	}
	if err := validateIdentity("target binding kind", kind); err != nil {
		return TargetBinding{}, err
	}
	if len(data) == 0 {
		return TargetBinding{}, fmt.Errorf("target binding data is required")
	}
	copied := append([]byte(nil), data...)
	return TargetBinding{
		target:     target,
		apiVersion: apiVersion,
		kind:       kind,
		digest: framedDigest(
			digestEntry{path: targetPlatformPath, data: []byte(target.Platform)},
			digestEntry{path: targetIDPath, data: []byte(target.ID)},
			digestEntry{path: bindingAPIVersionPath, data: []byte(apiVersion)},
			digestEntry{path: bindingKindPath, data: []byte(kind)},
			digestEntry{path: bindingDataPath, data: copied},
		),
		data: copied,
	}, nil
}

func (b TargetBinding) Target() TargetRef  { return b.target }
func (b TargetBinding) APIVersion() string { return b.apiVersion }
func (b TargetBinding) Kind() string       { return b.kind }
func (b TargetBinding) Digest() Digest     { return b.digest }
func (b TargetBinding) Bytes() []byte      { return append([]byte(nil), b.data...) }

func (b TargetBinding) Validate() error {
	if err := b.target.Validate(); err != nil {
		return err
	}
	if err := validateIdentity("target binding API version", b.apiVersion); err != nil {
		return err
	}
	if err := validateIdentity("target binding kind", b.kind); err != nil {
		return err
	}
	if len(b.data) == 0 || b.digest.IsZero() {
		return fmt.Errorf("target binding data and digest are required")
	}
	return nil
}

func (TargetBinding) MarshalJSON() ([]byte, error) {
	return nil, fmt.Errorf("TargetBinding is an in-process value; persist its source document")
}

func (*TargetBinding) UnmarshalJSON([]byte) error {
	return fmt.Errorf("TargetBinding is an in-process value; read its source document")
}

// RuntimeBuildInput binds a deployment source and target binding to one runtime
// installation before runtime-native building begins.
type RuntimeBuildInput struct {
	operation OperationID
	source    DeploymentSource
	runtime   RuntimeRef
	binding   TargetBinding
}

func NewRuntimeBuildInput(operation OperationID, source DeploymentSource, runtime RuntimeRef, binding TargetBinding) (RuntimeBuildInput, error) {
	if err := validateIdentity("operation ID", string(operation)); err != nil {
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

func (i RuntimeBuildInput) Operation() OperationID   { return i.operation }
func (i RuntimeBuildInput) Source() DeploymentSource { return i.source }
func (i RuntimeBuildInput) Runtime() RuntimeRef      { return i.runtime }
func (i RuntimeBuildInput) Binding() TargetBinding   { return i.binding }

func (RuntimeBuildInput) MarshalJSON() ([]byte, error) {
	return nil, fmt.Errorf("RuntimeBuildInput is an in-process command")
}

func (*RuntimeBuildInput) UnmarshalJSON([]byte) error {
	return fmt.Errorf("RuntimeBuildInput is an in-process command")
}

// RuntimeBundle is immutable, opaque runtime-native output bound to the exact
// source, runtime installation, and target binding used to build it.
type RuntimeBundle struct {
	operation      OperationID
	runtime        RuntimeRef
	source         DeploymentSourceRef
	bindingDigest  Digest
	renderedDigest Digest
	documents      []RuntimeDocument
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
	return nil, fmt.Errorf("RuntimeDocument is an in-process value; persist DeploymentReceipt")
}

func (*RuntimeDocument) UnmarshalJSON([]byte) error {
	return fmt.Errorf("RuntimeDocument is an in-process value; rebuild it from source")
}

func NewRuntimeBundle(input RuntimeBuildInput, documents []RuntimeDocument) (RuntimeBundle, error) {
	if err := validateIdentity("operation ID", string(input.operation)); err != nil {
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
	entries := make([]digestEntry, len(documents))
	deployable := false
	for i, document := range documents {
		if len(document.data) == 0 {
			return RuntimeBundle{}, fmt.Errorf("runtime bundle document %d is empty", i)
		}
		deployable = deployable || document.deployable
		copied[i] = RuntimeDocument{data: append([]byte(nil), document.data...), deployable: document.deployable}
		entries[i] = digestEntry{path: fmt.Sprintf(renderedDocumentPattern, i), data: document.data}
	}
	if !deployable {
		return RuntimeBundle{}, fmt.Errorf("runtime bundle has no deployable document")
	}
	return RuntimeBundle{
		operation:      input.operation,
		runtime:        input.runtime,
		source:         input.source.Ref(),
		bindingDigest:  input.binding.Digest(),
		renderedDigest: framedDigest(entries...),
		documents:      copied,
	}, nil
}

func (b RuntimeBundle) Operation() OperationID      { return b.operation }
func (b RuntimeBundle) Runtime() RuntimeRef         { return b.runtime }
func (b RuntimeBundle) Source() DeploymentSourceRef { return b.source }
func (b RuntimeBundle) BindingDigest() Digest       { return b.bindingDigest }
func (b RuntimeBundle) RenderedDigest() Digest      { return b.renderedDigest }
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
	return nil, fmt.Errorf("RuntimeBundle is an in-process value; persist DeploymentReceipt")
}

func (*RuntimeBundle) UnmarshalJSON([]byte) error {
	return fmt.Errorf("RuntimeBundle is an in-process value; rebuild it from source")
}

type DeployOptions struct {
	Reconcile bool
}

// RuntimeInstaller ensures a runtime exists on a target. Runtime installation
// remains separate from both target provisioning and agent deployment.
type RuntimeInstaller interface {
	Ensure(context.Context, EnsureRuntimeRequest) (RuntimeReceipt, error)
}

type RuntimeInstallRecoverer interface {
	RecoverEnsure(context.Context, OperationID) (RuntimeReceipt, error)
}

// RuntimeBuilder validates one deployment source and translates it into exact
// native output. For authored revisions it parses the exact source and refuses
// behavior it cannot honor; for sandbox images it verifies the OCI identity and
// AgentSuite binding required by the runtime.
type RuntimeBuilder interface {
	Build(context.Context, RuntimeBuildInput) (RuntimeBundle, error)
}

type RuntimeDeployer interface {
	Deploy(context.Context, RuntimeBundle, DeployOptions) (DeploymentReceipt, error)
}

type RuntimeDeployRecoverer interface {
	RecoverDeploy(context.Context, OperationID) (DeploymentReceipt, error)
}

type RuntimeObserver interface {
	Observe(context.Context, DeploymentRef) (DeploymentSnapshot, error)
}

// RuntimeRetirer removes or releases only resources belonging to the deployment
// named by its receipt. The receipt is local evidence, not authentication: an
// implementation must verify its ID, complete deployment identity, and native
// ownership evidence before mutation. It has no infrastructure authority.
type RuntimeRetirer interface {
	Retire(context.Context, RetireRequest) (RetirementReceipt, error)
}

type RuntimeRetireRecoverer interface {
	RecoverRetire(context.Context, OperationID) (RetirementReceipt, error)
}
