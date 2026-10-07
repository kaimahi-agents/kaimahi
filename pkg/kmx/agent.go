package kmx

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// AgentSource owns the exact authored bytes passed to BuildRevision.
type AgentSource struct {
	data []byte
}

func NewAgentSource(data []byte) (AgentSource, error) {
	if len(data) == 0 {
		return AgentSource{}, fmt.Errorf("agent source is required")
	}
	return AgentSource{data: append([]byte(nil), data...)}, nil
}

func (s AgentSource) Bytes() []byte {
	return append([]byte(nil), s.data...)
}

func (AgentSource) MarshalJSON() ([]byte, error) {
	return nil, fmt.Errorf("AgentSource is an in-process value; persist its authored file")
}

func (*AgentSource) UnmarshalJSON([]byte) error {
	return fmt.Errorf("AgentSource is an in-process value; read its authored file")
}

// AgentRevision combines an immutable byte identity with its exact source. It
// does not by itself prove schema or behavior validation; BuildRevision owns
// that policy, and runtime implementations must still reject behavior they cannot
// honor. Persist Ref rather than serializing this value.
type AgentRevision struct {
	ref    AgentRevisionRef
	source []byte
}

func NewAgentRevision(source AgentSource) (AgentRevision, error) {
	data := source.Bytes()
	if len(data) == 0 {
		return AgentRevision{}, fmt.Errorf("agent source is required")
	}
	return AgentRevision{
		ref:    AgentRevisionRef{Digest: agentSourceDigest(data)},
		source: data,
	}, nil
}

func (r AgentRevision) Ref() AgentRevisionRef {
	return r.ref
}

func (r AgentRevision) Source() []byte {
	return append([]byte(nil), r.source...)
}

func (AgentRevision) MarshalJSON() ([]byte, error) {
	return nil, fmt.Errorf("AgentRevision is an in-process value; persist AgentRevisionRef")
}

func (*AgentRevision) UnmarshalJSON([]byte) error {
	return fmt.Errorf("AgentRevision is an in-process value; rebuild it from AgentSource")
}

type DeploymentSourceKind string

const (
	DeploymentSourceRevision     DeploymentSourceKind = "revision"
	DeploymentSourceSandboxImage DeploymentSourceKind = "sandbox-image"
)

// DeploymentSourceRef is the durable identity of exactly one deployable input:
// authored revision bytes or a runnable sandbox image derived from an
// AgentSuite. It is a value union so copied receipts cannot alias source state.
type DeploymentSourceRef struct {
	kind     DeploymentSourceKind
	revision AgentRevisionRef
	sandbox  AgentSandboxRef
}

func NewRevisionDeploymentSourceRef(revision AgentRevisionRef) (DeploymentSourceRef, error) {
	if err := revision.Validate(); err != nil {
		return DeploymentSourceRef{}, err
	}
	return DeploymentSourceRef{kind: DeploymentSourceRevision, revision: revision}, nil
}

func NewSandboxDeploymentSourceRef(image AgentSandboxRef) (DeploymentSourceRef, error) {
	if err := image.Validate(); err != nil {
		return DeploymentSourceRef{}, err
	}
	return DeploymentSourceRef{kind: DeploymentSourceSandboxImage, sandbox: image}, nil
}

func (r DeploymentSourceRef) Kind() DeploymentSourceKind { return r.kind }

func (r DeploymentSourceRef) Revision() (AgentRevisionRef, bool) {
	return r.revision, r.kind == DeploymentSourceRevision
}

func (r DeploymentSourceRef) SandboxImage() (AgentSandboxRef, bool) {
	return r.sandbox, r.kind == DeploymentSourceSandboxImage
}

func (r DeploymentSourceRef) Validate() error {
	switch r.kind {
	case DeploymentSourceRevision:
		return r.revision.Validate()
	case DeploymentSourceSandboxImage:
		return r.sandbox.Validate()
	default:
		return fmt.Errorf("deployment source kind must be revision or sandbox-image")
	}
}

func (r DeploymentSourceRef) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	switch r.kind {
	case DeploymentSourceRevision:
		return json.Marshal(struct {
			Kind     DeploymentSourceKind `json:"kind"`
			Revision AgentRevisionRef     `json:"revision"`
		}{Kind: r.kind, Revision: r.revision})
	case DeploymentSourceSandboxImage:
		return json.Marshal(struct {
			Kind         DeploymentSourceKind `json:"kind"`
			SandboxImage AgentSandboxRef      `json:"sandboxImage"`
		}{Kind: r.kind, SandboxImage: r.sandbox})
	default:
		return nil, fmt.Errorf("invalid deployment source kind")
	}
}

func (r *DeploymentSourceRef) UnmarshalJSON(data []byte) error {
	if err := validateJSONInput(data); err != nil {
		return err
	}
	var envelope struct {
		Kind         DeploymentSourceKind `json:"kind"`
		Revision     json.RawMessage      `json:"revision"`
		SandboxImage json.RawMessage      `json:"sandboxImage"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return err
	}
	var value DeploymentSourceRef
	switch envelope.Kind {
	case DeploymentSourceRevision:
		if len(envelope.Revision) == 0 || len(envelope.SandboxImage) != 0 {
			return fmt.Errorf("revision deployment source requires only revision")
		}
		if err := json.Unmarshal(envelope.Revision, &value.revision); err != nil {
			return err
		}
		value.kind = envelope.Kind
	case DeploymentSourceSandboxImage:
		if len(envelope.SandboxImage) == 0 || len(envelope.Revision) != 0 {
			return fmt.Errorf("sandbox deployment source requires only sandboxImage")
		}
		if err := json.Unmarshal(envelope.SandboxImage, &value.sandbox); err != nil {
			return err
		}
		value.kind = envelope.Kind
	default:
		return fmt.Errorf("unknown deployment source kind %q", envelope.Kind)
	}
	if err := value.Validate(); err != nil {
		return err
	}
	*r = value
	return nil
}

// DeploymentSource is an in-process deployable input. A revision retains its
// exact authored bytes; a sandbox source retains its exact OCI identity and
// AgentSuite binding.
type DeploymentSource struct {
	kind     DeploymentSourceKind
	revision AgentRevision
	sandbox  AgentSandboxImage
}

func NewRevisionDeploymentSource(revision AgentRevision) (DeploymentSource, error) {
	if err := revision.Ref().Validate(); err != nil {
		return DeploymentSource{}, err
	}
	return DeploymentSource{kind: DeploymentSourceRevision, revision: revision}, nil
}

func NewSandboxDeploymentSource(image AgentSandboxImage) (DeploymentSource, error) {
	if err := image.Validate(); err != nil {
		return DeploymentSource{}, err
	}
	return DeploymentSource{kind: DeploymentSourceSandboxImage, sandbox: image}, nil
}

func (s DeploymentSource) Ref() DeploymentSourceRef {
	switch s.kind {
	case DeploymentSourceRevision:
		ref, _ := NewRevisionDeploymentSourceRef(s.revision.Ref())
		return ref
	case DeploymentSourceSandboxImage:
		ref, _ := NewSandboxDeploymentSourceRef(s.sandbox.Ref())
		return ref
	}
	return DeploymentSourceRef{}
}

func (s DeploymentSource) Revision() (AgentRevision, bool) {
	return s.revision, s.kind == DeploymentSourceRevision
}

func (s DeploymentSource) SandboxImage() (AgentSandboxImage, bool) {
	return s.sandbox, s.kind == DeploymentSourceSandboxImage
}

func (DeploymentSource) MarshalJSON() ([]byte, error) {
	return nil, fmt.Errorf("DeploymentSource is an in-process value; persist DeploymentSourceRef")
}

func (*DeploymentSource) UnmarshalJSON([]byte) error {
	return fmt.Errorf("DeploymentSource is an in-process value; rebuild it from source")
}

type DiagnosticLevel string

const (
	DiagnosticWarning DiagnosticLevel = "warning"
	DiagnosticError   DiagnosticLevel = "error"
)

type Diagnostic struct {
	Level   DiagnosticLevel `json:"level"`
	Code    string          `json:"code,omitempty"`
	Message string          `json:"message"`
}

type BuildReport struct {
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}

type BuildRequest struct {
	Source AgentSource
}

type BuildResult struct {
	Revision AgentRevision
	Report   BuildReport
}

type DeploymentState string

const (
	DeploymentAbsent   DeploymentState = "absent"
	DeploymentPending  DeploymentState = "pending"
	DeploymentReady    DeploymentState = "ready"
	DeploymentDegraded DeploymentState = "degraded"
	DeploymentUnknown  DeploymentState = "unknown"
)

type Condition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

// DeploymentSnapshot is an observation, not mutation evidence. Absent is a
// successful observation; an unreadable deployment must return an error.
type DeploymentSnapshot struct {
	Deployment DeploymentRef   `json:"deployment"`
	State      DeploymentState `json:"state"`
	ObservedAt time.Time       `json:"observedAt"`
	Conditions []Condition     `json:"conditions,omitempty"`
}

// LiftRequest binds one immutable deployment source to a target-specific
// binding. An empty Runtime asks application policy to select the configured default.
// LiftRequest is an in-process command and is not a persistence format.
type LiftRequest struct {
	Operation OperationID
	Source    DeploymentSource
	Binding   TargetBinding
	Runtime   RuntimeID
	Options   LiftOptions
}

func (r LiftRequest) Validate() error {
	if err := validateIdentity("operation ID", string(r.Operation)); err != nil {
		return err
	}
	if err := r.Source.Ref().Validate(); err != nil {
		return err
	}
	if err := r.Binding.Validate(); err != nil {
		return err
	}
	if r.Runtime != "" {
		return validateIdentity("runtime ID", string(r.Runtime))
	}
	return nil
}

func (LiftRequest) MarshalJSON() ([]byte, error) {
	return nil, fmt.Errorf("LiftRequest is an in-process command")
}

func (*LiftRequest) UnmarshalJSON([]byte) error {
	return fmt.Errorf("LiftRequest is an in-process command")
}

type LiftOptions struct {
	Reconcile bool
}

type RetireRequest struct {
	Operation  OperationID
	Deployment DeploymentReceipt
}

func (r RetireRequest) Validate() error {
	if err := validateIdentity("operation ID", string(r.Operation)); err != nil {
		return err
	}
	return r.Deployment.Validate()
}

// AgentDeployments manages runtime placements in an AgentEnvironment. It has no
// operation capable of provisioning or deprovisioning target infrastructure.
// Retire derives its subject from deployment evidence instead of accepting
// another independently supplied deployment reference.
type AgentDeployments interface {
	BuildRevision(context.Context, BuildRequest) (BuildResult, error)
	Lift(context.Context, LiftRequest) (DeploymentReceipt, error)
	RecoverLift(context.Context, OperationID) (DeploymentReceipt, error)
	Status(context.Context, DeploymentRef) (DeploymentSnapshot, error)
	Retire(context.Context, RetireRequest) (RetirementReceipt, error)
	RecoverRetire(context.Context, OperationID) (RetirementReceipt, error)
}

var _ json.Marshaler = AgentSource{}
