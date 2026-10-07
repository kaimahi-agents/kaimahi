package kmx

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
	"unicode/utf8"
)

// TargetSpec names a target and the platform implementation that can resolve
// or provision it. Profile names implementation-owned configuration; target
// mechanics and credentials do not cross this contract.
type TargetSpec struct {
	Name     string     `json:"name"`
	Platform PlatformID `json:"platform"`
	Profile  string     `json:"profile,omitempty"`
}

type EnvironmentMode string

const (
	EnvironmentResolve            EnvironmentMode = "resolve"
	EnvironmentProvision          EnvironmentMode = "provision"
	EnvironmentResolveOrProvision EnvironmentMode = "resolve-or-provision"
)

func (s TargetSpec) Validate() error {
	if !utf8.ValidString(s.Name) || !utf8.ValidString(s.Profile) {
		return fmt.Errorf("target name and profile must be valid UTF-8")
	}
	if s.Name == "" {
		return fmt.Errorf("target name is required")
	}
	return validateIdentity("target platform", string(s.Platform))
}

type UpRequest struct {
	Operation OperationID
	Mode      EnvironmentMode
	Target    TargetSpec
	Runtime   RuntimeID
	Options   RuntimeOptions
}

func (r UpRequest) Validate() error {
	if err := validateIdentity("operation ID", string(r.Operation)); err != nil {
		return err
	}
	switch r.Mode {
	case EnvironmentResolve, EnvironmentProvision, EnvironmentResolveOrProvision:
	default:
		return fmt.Errorf("environment mode must be resolve, provision, or resolve-or-provision")
	}
	if err := r.Target.Validate(); err != nil {
		return err
	}
	return validateIdentity("runtime ID", string(r.Runtime))
}

type UpResult struct {
	Operation      OperationID            `json:"operation"`
	Target         TargetRef              `json:"target"`
	Runtime        RuntimeRef             `json:"runtime"`
	Infrastructure *InfrastructureReceipt `json:"infrastructure,omitempty"`
	RuntimeReceipt RuntimeReceipt         `json:"runtimeReceipt"`
}

// UpProgress is durable recovery state for a composed setup. Runtime is nil
// until installation succeeds; a non-nil Runtime with Complete false means the
// installation succeeded but a later verification or persistence step did not.
// Infrastructure remains available whenever target provisioning completed.
type UpProgress struct {
	Operation      OperationID            `json:"operation"`
	Target         TargetRef              `json:"target"`
	Infrastructure *InfrastructureReceipt `json:"infrastructure,omitempty"`
	Runtime        *RuntimeReceipt        `json:"runtime,omitempty"`
	Complete       bool                   `json:"complete"`
}

func (p UpProgress) Validate() error {
	if err := p.Operation.Validate(); err != nil {
		return err
	}
	if err := p.Target.Validate(); err != nil {
		return err
	}
	if p.Infrastructure != nil {
		if err := p.Infrastructure.Validate(); err != nil {
			return err
		}
		if p.Infrastructure.Operation != p.Operation || p.Infrastructure.Target != p.Target {
			return fmt.Errorf("infrastructure receipt does not match setup progress")
		}
	}
	if p.Runtime != nil {
		if err := p.Runtime.Validate(); err != nil {
			return err
		}
		if p.Runtime.Operation != p.Operation || p.Runtime.Runtime.Target != p.Target {
			return fmt.Errorf("runtime receipt does not match setup progress")
		}
	}
	if p.Complete && p.Runtime == nil {
		return fmt.Errorf("complete setup progress requires a runtime receipt")
	}
	return nil
}

type upProgressJSON UpProgress

func (p UpProgress) MarshalJSON() ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(upProgressJSON(p))
}

func (p *UpProgress) UnmarshalJSON(data []byte) error {
	if err := validateJSONInput(data); err != nil {
		return err
	}
	var decoded upProgressJSON
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	value := UpProgress(decoded)
	if err := value.Validate(); err != nil {
		return err
	}
	*p = value
	return nil
}

func (r UpResult) Validate() error {
	if err := validateIdentity("operation ID", string(r.Operation)); err != nil {
		return err
	}
	if err := r.Target.Validate(); err != nil {
		return err
	}
	if err := r.Runtime.Validate(); err != nil {
		return err
	}
	if r.Runtime.Target != r.Target {
		return fmt.Errorf("runtime installation and setup result identify different targets")
	}
	if err := r.RuntimeReceipt.Validate(); err != nil {
		return err
	}
	if r.RuntimeReceipt.Operation != r.Operation || r.RuntimeReceipt.Runtime != r.Runtime {
		return fmt.Errorf("runtime receipt does not match the setup result")
	}
	if r.Infrastructure != nil {
		if err := r.Infrastructure.Validate(); err != nil {
			return err
		}
		if r.Infrastructure.Operation != r.Operation || r.Infrastructure.Target != r.Target {
			return fmt.Errorf("infrastructure receipt does not match the setup result")
		}
	}
	return nil
}

type upResultJSON UpResult

func (r UpResult) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(upResultJSON(r))
}

func (r *UpResult) UnmarshalJSON(data []byte) error {
	if err := validateJSONInput(data); err != nil {
		return err
	}
	var decoded upResultJSON
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	value := UpResult(decoded)
	if err := value.Validate(); err != nil {
		return err
	}
	*r = value
	return nil
}

type DownRequest struct {
	Operation      OperationID
	Infrastructure InfrastructureReceipt
}

func (r DownRequest) Validate() error {
	if err := validateIdentity("operation ID", string(r.Operation)); err != nil {
		return err
	}
	return r.Infrastructure.Validate()
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

// AgentEnvironment is the consumer-facing destination workflow. Up composes
// target resolution or provisioning with runtime installation. Down derives its
// target from infrastructure evidence; Forget removes only local registration.
type AgentEnvironment interface {
	Up(context.Context, UpRequest) (UpResult, error)
	RecoverUp(context.Context, OperationID) (UpProgress, error)
	Register(context.Context, TargetSpec) (TargetRef, error)
	Inspect(context.Context, TargetRef) (TargetSnapshot, error)
	Down(context.Context, DownRequest) (TeardownReceipt, error)
	RecoverDown(context.Context, OperationID) (TeardownReceipt, error)
	Forget(context.Context, TargetRef) error
}
