package kmx

import "fmt"

// IDs are named string types so references from different ownership domains
// cannot be interchanged accidentally.
type (
	PlatformID            string
	RuntimeID             string
	RuntimeInstallationID string
	TargetID              string
	DeploymentID          string
	ReceiptID             string
)

// TargetRef is a durable identity, qualified by the platform implementation
// that can resolve it. Friendly aliases such as local or production are target
// configuration, not identity.
type TargetRef struct {
	Platform PlatformID `json:"platform"`
	ID       TargetID   `json:"id"`
}

func (r TargetRef) Validate() error {
	if r.Platform == "" {
		return fmt.Errorf("target platform is required")
	}
	if r.ID == "" {
		return fmt.Errorf("target ID is required")
	}
	return nil
}

// AgentRevisionRef identifies immutable authored bytes by their digest.
type AgentRevisionRef struct {
	Digest Digest `json:"digest"`
}

func (r AgentRevisionRef) Validate() error {
	if r.Digest.IsZero() {
		return fmt.Errorf("agent revision digest is required")
	}
	return nil
}

// RuntimeRef identifies one runtime installation on one exact target.
type RuntimeRef struct {
	Runtime      RuntimeID             `json:"runtime"`
	Installation RuntimeInstallationID `json:"installation"`
	Target       TargetRef             `json:"target"`
}

func (r RuntimeRef) Validate() error {
	if r.Runtime == "" {
		return fmt.Errorf("runtime ID is required")
	}
	if r.Installation == "" {
		return fmt.Errorf("runtime installation ID is required")
	}
	return r.Target.Validate()
}

// DeploymentRef is the durable identity shared by lift, status, and retire.
// Runtime identity is deployment provenance, not part of the agent revision.
type DeploymentRef struct {
	ID       DeploymentID     `json:"id"`
	Revision AgentRevisionRef `json:"revision"`
	Runtime  RuntimeRef       `json:"runtime"`
}

func (r DeploymentRef) Validate() error {
	if r.ID == "" {
		return fmt.Errorf("deployment ID is required")
	}
	if err := r.Revision.Validate(); err != nil {
		return err
	}
	return r.Runtime.Validate()
}
