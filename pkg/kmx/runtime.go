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
	if apiVersion == "" {
		return TargetBinding{}, fmt.Errorf("target binding API version is required")
	}
	if kind == "" {
		return TargetBinding{}, fmt.Errorf("target binding kind is required")
	}
	if len(data) == 0 {
		return TargetBinding{}, fmt.Errorf("target binding data is required")
	}
	copied := append([]byte(nil), data...)
	return TargetBinding{
		target:     target,
		apiVersion: apiVersion,
		kind:       kind,
		digest: digestParts(
			[]byte(target.Platform), []byte(target.ID),
			[]byte(apiVersion), []byte(kind), copied,
		),
		data: copied,
	}, nil
}

func (b TargetBinding) Target() TargetRef  { return b.target }
func (b TargetBinding) APIVersion() string { return b.apiVersion }
func (b TargetBinding) Kind() string       { return b.kind }
func (b TargetBinding) Digest() Digest     { return b.digest }
func (b TargetBinding) Bytes() []byte      { return append([]byte(nil), b.data...) }

func (TargetBinding) MarshalJSON() ([]byte, error) {
	return nil, fmt.Errorf("TargetBinding is an in-process value; persist its source document")
}

func (*TargetBinding) UnmarshalJSON([]byte) error {
	return fmt.Errorf("TargetBinding is an in-process value; read its source document")
}

// RuntimeBuildInput binds a revision and target binding to one runtime
// installation before runtime-native building begins.
type RuntimeBuildInput struct {
	revision AgentRevision
	runtime  RuntimeRef
	binding  TargetBinding
}

func NewRuntimeBuildInput(revision AgentRevision, runtime RuntimeRef, binding TargetBinding) (RuntimeBuildInput, error) {
	if err := revision.Ref().Validate(); err != nil {
		return RuntimeBuildInput{}, err
	}
	if err := runtime.Validate(); err != nil {
		return RuntimeBuildInput{}, err
	}
	if runtime.Target != binding.Target() {
		return RuntimeBuildInput{}, fmt.Errorf("runtime installation and target binding identify different targets")
	}
	return RuntimeBuildInput{revision: revision, runtime: runtime, binding: binding}, nil
}

func (i RuntimeBuildInput) Revision() AgentRevision { return i.revision }
func (i RuntimeBuildInput) Runtime() RuntimeRef     { return i.runtime }
func (i RuntimeBuildInput) Binding() TargetBinding  { return i.binding }

func (RuntimeBuildInput) MarshalJSON() ([]byte, error) {
	return nil, fmt.Errorf("RuntimeBuildInput is an in-process command")
}

func (*RuntimeBuildInput) UnmarshalJSON([]byte) error {
	return fmt.Errorf("RuntimeBuildInput is an in-process command")
}

// RuntimeBundle is immutable, opaque runtime-native output bound to the exact
// revision, runtime installation, and target binding used to build it.
type RuntimeBundle struct {
	runtime        RuntimeRef
	revision       AgentRevisionRef
	bindingDigest  Digest
	renderedDigest Digest
	artifact       []byte
}

func NewRuntimeBundle(input RuntimeBuildInput, artifact []byte) (RuntimeBundle, error) {
	if err := input.runtime.Validate(); err != nil {
		return RuntimeBundle{}, err
	}
	if len(artifact) == 0 {
		return RuntimeBundle{}, fmt.Errorf("runtime bundle artifact is required")
	}
	return RuntimeBundle{
		runtime:        input.runtime,
		revision:       input.revision.Ref(),
		bindingDigest:  input.binding.Digest(),
		renderedDigest: NewDigest(artifact),
		artifact:       append([]byte(nil), artifact...),
	}, nil
}

func (b RuntimeBundle) Runtime() RuntimeRef        { return b.runtime }
func (b RuntimeBundle) Revision() AgentRevisionRef { return b.revision }
func (b RuntimeBundle) BindingDigest() Digest      { return b.bindingDigest }
func (b RuntimeBundle) RenderedDigest() Digest     { return b.renderedDigest }
func (b RuntimeBundle) Artifact() []byte           { return append([]byte(nil), b.artifact...) }

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
	Ensure(context.Context, TargetRef, RuntimeOptions) (RuntimeReceipt, error)
}

// RuntimeBuilder translates an immutable KMX revision into exact native output.
type RuntimeBuilder interface {
	Build(context.Context, RuntimeBuildInput) (RuntimeBundle, error)
}

type RuntimeDeployer interface {
	Deploy(context.Context, RuntimeBundle, DeployOptions) (DeploymentReceipt, error)
}

type RuntimeObserver interface {
	Observe(context.Context, DeploymentRef) (DeploymentSnapshot, error)
}

// RuntimeRetirer removes or releases only resources belonging to the deployment
// named by its receipt. The receipt is local evidence, not authentication: an
// implementation must verify its ID, complete deployment identity, and native
// ownership evidence before mutation. It has no infrastructure authority.
type RuntimeRetirer interface {
	Retire(context.Context, DeploymentReceipt) (RetirementReceipt, error)
}
