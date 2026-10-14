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

type deploymentReceiptJSON DeploymentReceipt
type retirementReceiptJSON RetirementReceipt

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
