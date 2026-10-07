package kmx

import (
	"context"
	"time"
)

// TargetSpec names a target and the platform implementation that can resolve
// or provision it. Profile names implementation-owned configuration; target
// mechanics and credentials do not cross this contract.
type TargetSpec struct {
	Name     string     `json:"name"`
	Platform PlatformID `json:"platform"`
	Profile  string     `json:"profile,omitempty"`
}

type TargetState string

const (
	TargetAbsent      TargetState = "absent"
	TargetReady       TargetState = "ready"
	TargetUnavailable TargetState = "unavailable"
)

type TargetOwnership string

const (
	TargetManaged    TargetOwnership = "managed"
	TargetRegistered TargetOwnership = "registered"
)

// TargetObservation contains only facts a platform implementation owns.
// Absent is a successful observation; authentication, authorization, timeout,
// malformed response, and network failures must return an error.
type TargetObservation struct {
	Target     TargetRef   `json:"target"`
	State      TargetState `json:"state"`
	ObservedAt time.Time   `json:"observedAt"`
}

// TargetSnapshot is the application-level view composed from platform facts,
// local ownership records, and runtime discovery.
type TargetSnapshot struct {
	Target     TargetRef       `json:"target"`
	Name       string          `json:"name,omitempty"`
	State      TargetState     `json:"state"`
	Ownership  TargetOwnership `json:"ownership"`
	Runtimes   []RuntimeRef    `json:"runtimes,omitempty"`
	ObservedAt time.Time       `json:"observedAt"`
}

// TargetService is the author-facing target workflow. Down derives its target
// from infrastructure evidence; Forget removes only local registration and
// never mutates the target.
type TargetService interface {
	Up(context.Context, TargetSpec) (InfrastructureReceipt, error)
	Register(context.Context, TargetSpec) (TargetRef, error)
	Inspect(context.Context, TargetRef) (TargetSnapshot, error)
	Down(context.Context, InfrastructureReceipt) (TeardownReceipt, error)
	Forget(context.Context, TargetRef) error
}

type PlatformDescriptor struct {
	ID          PlatformID `json:"id"`
	DisplayName string     `json:"displayName"`
}

type Platform interface {
	Describe() PlatformDescriptor
}

// PlatformResolver binds a TargetSpec to an existing destination. Resolution
// does not establish infrastructure ownership.
type PlatformResolver interface {
	Resolve(context.Context, TargetSpec) (TargetRef, error)
}

// PlatformProvisioner creates or reconciles target infrastructure and returns
// the evidence needed for later deprovisioning.
type PlatformProvisioner interface {
	Provision(context.Context, TargetSpec) (InfrastructureReceipt, error)
}

type PlatformInspector interface {
	Inspect(context.Context, TargetRef) (TargetObservation, error)
}

// PlatformDeprovisioner accepts only infrastructure evidence and derives the
// target from it. Implementations must verify durable ownership rather than
// trust the receipt or infer ownership from a target name.
type PlatformDeprovisioner interface {
	Deprovision(context.Context, InfrastructureReceipt) (TeardownReceipt, error)
}
