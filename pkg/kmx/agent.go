package kmx

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// AgentSource owns the exact authored bytes passed to a RevisionBuilder.
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
// does not by itself prove schema or behavior validation; RevisionBuilder owns
// that policy, and runtime builders must still reject behavior they cannot
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

// RevisionBuilder validates authored source and creates an immutable KMX
// revision. It does not select a target or understand runtime-native resources.
type RevisionBuilder interface {
	Build(context.Context, AgentSource) (AgentRevision, BuildReport, error)
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

// LiftRequest binds one immutable revision to a target-specific binding. An
// empty Runtime asks application policy to select the configured default.
// LiftRequest is an in-process command and is not a persistence format.
type LiftRequest struct {
	Operation OperationID
	Revision  AgentRevision
	Binding   TargetBinding
	Runtime   RuntimeID
	Options   LiftOptions
}

func (r LiftRequest) Validate() error {
	if err := validateIdentity("operation ID", string(r.Operation)); err != nil {
		return err
	}
	if err := r.Revision.Ref().Validate(); err != nil {
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

// AgentService is the author-facing workload workflow. It has no operation
// capable of provisioning or deprovisioning target infrastructure. Retire
// derives its subject from deployment evidence instead of accepting another
// independently supplied deployment reference.
type AgentService interface {
	Build(context.Context, BuildRequest) (BuildResult, error)
	Lift(context.Context, LiftRequest) (DeploymentReceipt, error)
	RecoverLift(context.Context, OperationID) (DeploymentReceipt, error)
	Status(context.Context, DeploymentRef) (DeploymentSnapshot, error)
	Retire(context.Context, RetireRequest) (RetirementReceipt, error)
	RecoverRetire(context.Context, OperationID) (RetirementReceipt, error)
}

var _ json.Marshaler = AgentSource{}
