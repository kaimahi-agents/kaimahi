package model

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

type DeployableKind string

const (
	DeployableRevision     DeployableKind = "revision"
	DeployableSandboxImage DeployableKind = "sandbox-image"
)

// DeployableRef is the durable identity of exactly one deployable input:
// authored revision bytes or a runnable sandbox image derived from an
// AgentSuite. It is a value union so copied receipts cannot alias source state.
type DeployableRef struct {
	kind     DeployableKind
	revision AgentRevisionRef
	sandbox  AgentSandboxRef
}

func NewRevisionDeployableRef(revision AgentRevisionRef) (DeployableRef, error) {
	if err := revision.Validate(); err != nil {
		return DeployableRef{}, err
	}
	return DeployableRef{kind: DeployableRevision, revision: revision}, nil
}

func NewSandboxDeployableRef(image AgentSandboxRef) (DeployableRef, error) {
	if err := image.Validate(); err != nil {
		return DeployableRef{}, err
	}
	return DeployableRef{kind: DeployableSandboxImage, sandbox: image}, nil
}

func (r DeployableRef) Kind() DeployableKind { return r.kind }

func (r DeployableRef) Revision() (AgentRevisionRef, bool) {
	return r.revision, r.kind == DeployableRevision
}

func (r DeployableRef) SandboxImage() (AgentSandboxRef, bool) {
	return r.sandbox, r.kind == DeployableSandboxImage
}

func (r DeployableRef) Validate() error {
	switch r.kind {
	case DeployableRevision:
		return r.revision.Validate()
	case DeployableSandboxImage:
		return r.sandbox.Validate()
	default:
		return fmt.Errorf("deployable kind must be revision or sandbox-image")
	}
}

func (r DeployableRef) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	switch r.kind {
	case DeployableRevision:
		return json.Marshal(struct {
			Kind     DeployableKind   `json:"kind"`
			Revision AgentRevisionRef `json:"revision"`
		}{Kind: r.kind, Revision: r.revision})
	case DeployableSandboxImage:
		return json.Marshal(struct {
			Kind         DeployableKind  `json:"kind"`
			SandboxImage AgentSandboxRef `json:"sandboxImage"`
		}{Kind: r.kind, SandboxImage: r.sandbox})
	default:
		return nil, fmt.Errorf("invalid deployable kind")
	}
}

func (r *DeployableRef) UnmarshalJSON(data []byte) error {
	if err := validateJSONInput(data); err != nil {
		return err
	}
	var envelope struct {
		Kind         DeployableKind  `json:"kind"`
		Revision     json.RawMessage `json:"revision"`
		SandboxImage json.RawMessage `json:"sandboxImage"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return err
	}
	var value DeployableRef
	switch envelope.Kind {
	case DeployableRevision:
		if len(envelope.Revision) == 0 || len(envelope.SandboxImage) != 0 {
			return fmt.Errorf("revision deployable requires only revision")
		}
		if err := json.Unmarshal(envelope.Revision, &value.revision); err != nil {
			return err
		}
		value.kind = envelope.Kind
	case DeployableSandboxImage:
		if len(envelope.SandboxImage) == 0 || len(envelope.Revision) != 0 {
			return fmt.Errorf("sandbox deployable requires only sandboxImage")
		}
		if err := json.Unmarshal(envelope.SandboxImage, &value.sandbox); err != nil {
			return err
		}
		value.kind = envelope.Kind
	default:
		return fmt.Errorf("unknown deployable kind %q", envelope.Kind)
	}
	if err := value.Validate(); err != nil {
		return err
	}
	*r = value
	return nil
}

// Deployable is an in-process deployment input. A revision retains its exact
// authored bytes; a sandbox image retains its exact OCI identity and
// AgentSuite binding.
type Deployable struct {
	kind     DeployableKind
	revision AgentRevision
	sandbox  AgentSandboxImage
}

func NewRevisionDeployable(revision AgentRevision) (Deployable, error) {
	if err := revision.Ref().Validate(); err != nil {
		return Deployable{}, err
	}
	return Deployable{kind: DeployableRevision, revision: revision}, nil
}

func NewSandboxDeployable(image AgentSandboxImage) (Deployable, error) {
	if err := image.Validate(); err != nil {
		return Deployable{}, err
	}
	return Deployable{kind: DeployableSandboxImage, sandbox: image}, nil
}

func (s Deployable) Ref() DeployableRef {
	switch s.kind {
	case DeployableRevision:
		ref, _ := NewRevisionDeployableRef(s.revision.Ref())
		return ref
	case DeployableSandboxImage:
		ref, _ := NewSandboxDeployableRef(s.sandbox.Ref())
		return ref
	}
	return DeployableRef{}
}

func (s Deployable) Revision() (AgentRevision, bool) {
	return s.revision, s.kind == DeployableRevision
}

func (s Deployable) SandboxImage() (AgentSandboxImage, bool) {
	return s.sandbox, s.kind == DeployableSandboxImage
}

func (Deployable) MarshalJSON() ([]byte, error) {
	return nil, fmt.Errorf("Deployable is an in-process value; persist DeployableRef")
}

func (*Deployable) UnmarshalJSON([]byte) error {
	return fmt.Errorf("Deployable is an in-process value; rebuild it from authored source or a sandbox image")
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

// LiftRequest binds one immutable deployable to a target-specific
// binding. An empty Runtime asks application policy to select the configured default.
// LiftRequest is an in-process command and is not a persistence format.
type LiftRequest struct {
	Operation  OperationID
	Deployable Deployable
	Binding    TargetBinding
	Runtime    RuntimeID
	Options    LiftOptions
}

func (r LiftRequest) Validate() error {
	if err := validateIdentity("operation ID", string(r.Operation)); err != nil {
		return err
	}
	if err := r.Deployable.Ref().Validate(); err != nil {
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
