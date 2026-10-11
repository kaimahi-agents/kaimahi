package kmx

import (
	"encoding/json"
	"fmt"
	"time"
)

type MutationOutcome string

const (
	MutationSucceeded MutationOutcome = "succeeded"
)

// InfrastructureReceipt records platform-owned target infrastructure. It is
// local evidence, not authentication; a deprovisioner must verify it against
// durable ownership state before mutating anything.
type InfrastructureReceipt struct {
	ID         ReceiptID   `json:"id"`
	Operation  OperationID `json:"operation"`
	Target     TargetRef   `json:"target"`
	RecordedAt time.Time   `json:"recordedAt"`
}

func (r InfrastructureReceipt) Validate() error {
	if err := validateIdentity("infrastructure receipt ID", string(r.ID)); err != nil {
		return err
	}
	if err := validateIdentity("operation ID", string(r.Operation)); err != nil {
		return err
	}
	if err := r.Target.Validate(); err != nil {
		return err
	}
	return validateReceiptTime(r.RecordedAt)
}

// RuntimeReceipt records installation or reconciliation of a runtime on a
// target. It grants no authority over target infrastructure.
type RuntimeReceipt struct {
	ID         ReceiptID   `json:"id"`
	Operation  OperationID `json:"operation"`
	Runtime    RuntimeRef  `json:"runtime"`
	RecordedAt time.Time   `json:"recordedAt"`
}

func (r RuntimeReceipt) Validate() error {
	if err := validateIdentity("runtime receipt ID", string(r.ID)); err != nil {
		return err
	}
	if err := validateIdentity("operation ID", string(r.Operation)); err != nil {
		return err
	}
	if err := r.Runtime.Validate(); err != nil {
		return err
	}
	return validateReceiptTime(r.RecordedAt)
}

// DeploymentReceipt binds the deployed source identity to the exact target
// binding and runtime artifact. It is local evidence, not authentication; retirement
// must verify it against implementation-owned resource identity and ownership
// evidence before mutation.
type DeploymentReceipt struct {
	ID             ReceiptID     `json:"id"`
	Operation      OperationID   `json:"operation"`
	Deployment     DeploymentRef `json:"deployment"`
	BindingDigest  Digest        `json:"bindingDigest"`
	RenderedDigest Digest        `json:"renderedDigest"`
	DeployDigest   Digest        `json:"deployDigest"`
	RecordedAt     time.Time     `json:"recordedAt"`
}

func (r DeploymentReceipt) Validate() error {
	if err := validateIdentity("deployment receipt ID", string(r.ID)); err != nil {
		return err
	}
	if err := validateIdentity("operation ID", string(r.Operation)); err != nil {
		return err
	}
	if err := r.Deployment.Validate(); err != nil {
		return err
	}
	if r.BindingDigest.IsZero() || r.RenderedDigest.IsZero() || r.DeployDigest.IsZero() {
		return fmt.Errorf("deployment receipt binding, rendered, and deploy digests are required")
	}
	if r.RecordedAt.IsZero() {
		return fmt.Errorf("receipt time is required")
	}
	return nil
}

type RetirementReceipt struct {
	ID         ReceiptID       `json:"id"`
	Operation  OperationID     `json:"operation"`
	Deployment DeploymentRef   `json:"deployment"`
	Outcome    MutationOutcome `json:"outcome"`
	RecordedAt time.Time       `json:"recordedAt"`
}

func (r RetirementReceipt) Validate() error {
	if err := validateIdentity("retirement receipt ID", string(r.ID)); err != nil {
		return err
	}
	if err := validateIdentity("operation ID", string(r.Operation)); err != nil {
		return err
	}
	if err := r.Deployment.Validate(); err != nil {
		return err
	}
	return validateMutationReceipt(r.Outcome, r.RecordedAt)
}

type TeardownReceipt struct {
	ID         ReceiptID       `json:"id"`
	Operation  OperationID     `json:"operation"`
	Target     TargetRef       `json:"target"`
	Outcome    MutationOutcome `json:"outcome"`
	RecordedAt time.Time       `json:"recordedAt"`
}

func (r TeardownReceipt) Validate() error {
	if err := validateIdentity("teardown receipt ID", string(r.ID)); err != nil {
		return err
	}
	if err := validateIdentity("operation ID", string(r.Operation)); err != nil {
		return err
	}
	if err := r.Target.Validate(); err != nil {
		return err
	}
	return validateMutationReceipt(r.Outcome, r.RecordedAt)
}

func validateReceiptTime(recordedAt time.Time) error {
	if recordedAt.IsZero() {
		return fmt.Errorf("receipt time is required")
	}
	return nil
}

func validateMutationReceipt(outcome MutationOutcome, recordedAt time.Time) error {
	if outcome != MutationSucceeded {
		return fmt.Errorf("receipt outcome must be %q", MutationSucceeded)
	}
	if recordedAt.IsZero() {
		return fmt.Errorf("receipt time is required")
	}
	return nil
}

// Validated receipt JSON is the cross-process persistence boundary. Invalid
// identities fail both encoding and decoding rather than circulating as evidence.
func marshalReceipt(value any, validate func() error) ([]byte, error) {
	if err := validate(); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

type infrastructureReceiptJSON InfrastructureReceipt
type runtimeReceiptJSON RuntimeReceipt
type deploymentReceiptJSON DeploymentReceipt
type retirementReceiptJSON RetirementReceipt
type teardownReceiptJSON TeardownReceipt

func (r InfrastructureReceipt) MarshalJSON() ([]byte, error) {
	return marshalReceipt(infrastructureReceiptJSON(r), r.Validate)
}
func (r *InfrastructureReceipt) UnmarshalJSON(data []byte) error {
	if err := validateJSONInput(data); err != nil {
		return err
	}
	var decoded infrastructureReceiptJSON
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	value := InfrastructureReceipt(decoded)
	if err := value.Validate(); err != nil {
		return err
	}
	*r = value
	return nil
}

func (r RuntimeReceipt) MarshalJSON() ([]byte, error) {
	return marshalReceipt(runtimeReceiptJSON(r), r.Validate)
}
func (r *RuntimeReceipt) UnmarshalJSON(data []byte) error {
	if err := validateJSONInput(data); err != nil {
		return err
	}
	var decoded runtimeReceiptJSON
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	value := RuntimeReceipt(decoded)
	if err := value.Validate(); err != nil {
		return err
	}
	*r = value
	return nil
}

func (r DeploymentReceipt) MarshalJSON() ([]byte, error) {
	return marshalReceipt(deploymentReceiptJSON(r), r.Validate)
}
func (r *DeploymentReceipt) UnmarshalJSON(data []byte) error {
	if err := validateJSONInput(data); err != nil {
		return err
	}
	var decoded deploymentReceiptJSON
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	value := DeploymentReceipt(decoded)
	if err := value.Validate(); err != nil {
		return err
	}
	*r = value
	return nil
}

func (r RetirementReceipt) MarshalJSON() ([]byte, error) {
	return marshalReceipt(retirementReceiptJSON(r), r.Validate)
}
func (r *RetirementReceipt) UnmarshalJSON(data []byte) error {
	if err := validateJSONInput(data); err != nil {
		return err
	}
	var decoded retirementReceiptJSON
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	value := RetirementReceipt(decoded)
	if err := value.Validate(); err != nil {
		return err
	}
	*r = value
	return nil
}

func (r TeardownReceipt) MarshalJSON() ([]byte, error) {
	return marshalReceipt(teardownReceiptJSON(r), r.Validate)
}
func (r *TeardownReceipt) UnmarshalJSON(data []byte) error {
	if err := validateJSONInput(data); err != nil {
		return err
	}
	var decoded teardownReceiptJSON
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	value := TeardownReceipt(decoded)
	if err := value.Validate(); err != nil {
		return err
	}
	*r = value
	return nil
}
