package app

import (
	"github.com/google/uuid"
)

// Model connection selectors are nonsecret. Credential delivery and generation
// serialization belong to the owning guest endpoint, not these options.
type OrkaModelConnectOptions struct{ OperationID, Namespace, Route, Provider, Model, CredentialGeneration string }
type OrkaModelProveOptions struct{ OperationID, Namespace, Provider, Connection, CredentialGeneration string }
type OrkaModelDisconnectOptions struct{ OperationID, Namespace, Provider, Connection string }

type OrkaModelConnection struct {
	ID                   string `json:"id"`
	Route                string `json:"route"`
	Provider             string `json:"provider"`
	Model                string `json:"model"`
	CredentialGeneration string `json:"credentialGeneration"`
	State                string `json:"state"`
}
type OrkaModelProof struct {
	TaskUID        string `json:"taskUID"`
	RequestedModel string `json:"requestedModel"`
	ReportedModel  string `json:"reportedModel"`
	Route          string `json:"route"`
	ConnectionID   string `json:"connectionID"`
	TerminalState  string `json:"terminalState"`
	State          string `json:"state"`
}
type OrkaModelResult struct {
	Connection  *OrkaModelConnection `json:"connection,omitempty"`
	Proof       *OrkaModelProof      `json:"proof,omitempty"`
	RemoteState string               `json:"remoteState,omitempty"`
}

// OrkaModelError retains the cause for cancellation/exit handling, but neither
// its displayed message nor its code reflects external text or private state.
type OrkaModelError struct {
	code  string
	cause error
}

func (e *OrkaModelError) Error() string { return NewMachineError(e.Code(), "preflight").Error() }
func (e *OrkaModelError) Unwrap() error { return e.cause }
func (e *OrkaModelError) Code() string {
	switch e.code {
	case "credential_unavailable", "permission_denied", "dependency_unavailable", "execution_unknown", "invalid_arguments", "configuration_invalid", "cancelled", "deadline_exceeded", "output_failed", "unsupported":
		return e.code
	default:
		return "operation_failed"
	}
}
func modelStateError(err error) error {
	if err == nil {
		return nil
	}
	return &OrkaModelError{code: "execution_unknown", cause: err}
}
func modelUUID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id.String() == value && id.Version() == 4 && id.Variant() == uuid.RFC4122
}
