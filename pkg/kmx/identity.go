package kmx

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"
)

// IDs are named string types so references from different ownership domains
// cannot be interchanged accidentally.
type (
	PlatformID            string
	RuntimeID             string
	RuntimeInstallationID string
	TargetID              string
	DeploymentID          string
	ReceiptID             string
	OperationID           string
)

func (id OperationID) Validate() error {
	return validateIdentity("operation ID", string(id))
}

func validateIdentity(name, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", name)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s must be valid UTF-8", name)
	}
	return nil
}

func validateJSONInput(data []byte) error {
	if !utf8.Valid(data) {
		return fmt.Errorf("JSON must be valid UTF-8")
	}
	return nil
}

// TargetRef is a durable identity, qualified by the platform implementation
// that can resolve it. Friendly aliases such as local or production are target
// configuration, not identity.
type TargetRef struct {
	Platform PlatformID `json:"platform"`
	ID       TargetID   `json:"id"`
}

func (r TargetRef) Validate() error {
	if err := validateIdentity("target platform", string(r.Platform)); err != nil {
		return err
	}
	return validateIdentity("target ID", string(r.ID))
}

type targetRefJSON TargetRef

func (r TargetRef) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(targetRefJSON(r))
}

func (r *TargetRef) UnmarshalJSON(data []byte) error {
	if err := validateJSONInput(data); err != nil {
		return err
	}
	var decoded targetRefJSON
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	value := TargetRef(decoded)
	if err := value.Validate(); err != nil {
		return err
	}
	*r = value
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

type agentRevisionRefJSON AgentRevisionRef

func (r AgentRevisionRef) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(agentRevisionRefJSON(r))
}

func (r *AgentRevisionRef) UnmarshalJSON(data []byte) error {
	if err := validateJSONInput(data); err != nil {
		return err
	}
	var decoded agentRevisionRefJSON
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	value := AgentRevisionRef(decoded)
	if err := value.Validate(); err != nil {
		return err
	}
	*r = value
	return nil
}

// RuntimeRef identifies one runtime installation on one exact target.
type RuntimeRef struct {
	Runtime      RuntimeID             `json:"runtime"`
	Installation RuntimeInstallationID `json:"installation"`
	Target       TargetRef             `json:"target"`
}

func (r RuntimeRef) Validate() error {
	if err := validateIdentity("runtime ID", string(r.Runtime)); err != nil {
		return err
	}
	if err := validateIdentity("runtime installation ID", string(r.Installation)); err != nil {
		return err
	}
	return r.Target.Validate()
}

type runtimeRefJSON RuntimeRef

func (r RuntimeRef) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(runtimeRefJSON(r))
}

func (r *RuntimeRef) UnmarshalJSON(data []byte) error {
	if err := validateJSONInput(data); err != nil {
		return err
	}
	var decoded runtimeRefJSON
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	value := RuntimeRef(decoded)
	if err := value.Validate(); err != nil {
		return err
	}
	*r = value
	return nil
}

// DeploymentRef is the durable identity shared by lift, status, and retire.
// Runtime identity is deployment provenance, not part of the deployment source.
type DeploymentRef struct {
	ID      DeploymentID        `json:"id"`
	Source  DeploymentSourceRef `json:"source"`
	Runtime RuntimeRef          `json:"runtime"`
}

func (r DeploymentRef) Validate() error {
	if err := validateIdentity("deployment ID", string(r.ID)); err != nil {
		return err
	}
	if err := r.Source.Validate(); err != nil {
		return err
	}
	return r.Runtime.Validate()
}

type deploymentRefJSON DeploymentRef

func (r DeploymentRef) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(deploymentRefJSON(r))
}

func (r *DeploymentRef) UnmarshalJSON(data []byte) error {
	if err := validateJSONInput(data); err != nil {
		return err
	}
	var decoded deploymentRefJSON
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	value := DeploymentRef(decoded)
	if err := value.Validate(); err != nil {
		return err
	}
	*r = value
	return nil
}
