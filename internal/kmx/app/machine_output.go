package app

import "encoding/json"

// MachineEnvelope correlates opt-in output records. Its identity is not a
// resumable operation or an idempotency key.
type MachineEnvelope struct {
	SchemaVersion int    `json:"schemaVersion"`
	Kind          string `json:"kind"`
	Operation     string `json:"operation"`
	OperationID   string `json:"operationId"`
	Seq           uint64 `json:"seq"`
	ElapsedMs     int64  `json:"elapsedMs"`
}

// MachineResult closes one machine invocation without granting replay or
// evidence authority. Legacy command JSON documents do not use this envelope.
type MachineResult struct {
	MachineEnvelope
	Outcome  string        `json:"outcome"`
	Verdict  string        `json:"verdict"`
	ExitCode int           `json:"exitCode"`
	Error    *MachineError `json:"error,omitempty"`
}

// MachineError carries only fixed diagnostic vocabulary, never a provider cause.
type MachineError struct {
	code, stage string
}

// NewMachineError withholds unknown codes and stages rather than reflecting
// external diagnostics. A stage is presentation metadata, not retry authority.
func NewMachineError(code, stage string) *MachineError {
	switch stage {
	case "preflight", "cluster", "ollama", "model", "orka", "installer", "runtime",
		"result_reader", "provider", "gateway", "execution", "assertions", "finalize", "other":
	default:
		stage = ""
	}
	return &MachineError{code: code, stage: stage}
}

type machineErrorJSON struct {
	Code     string `json:"code"`
	Message  string `json:"message"`
	Stage    string `json:"stage,omitempty"`
	Recovery string `json:"recovery"`
}

func (e MachineError) projection() machineErrorJSON {
	v := machineErrorJSON{Code: e.code, Stage: e.stage}
	switch e.code {
	case "invalid_arguments":
		v.Message, v.Recovery = "The command arguments are invalid.", "check_inputs"
	case "configuration_invalid":
		v.Message, v.Recovery = "The configuration is invalid.", "check_configuration"
	case "unsupported":
		v.Message, v.Recovery = "The requested operation is unsupported.", "check_inputs"
	case "confirmation_required":
		v.Message, v.Recovery = "Target confirmation is required.", "confirm_target"
	case "credential_unavailable":
		v.Message, v.Recovery = "A required credential is unavailable.", "supply_credentials"
	case "permission_denied":
		v.Message, v.Recovery = "The operation was not permitted.", "inspect_target"
	case "dependency_unavailable":
		v.Message, v.Recovery = "A required dependency is unavailable.", "check_configuration"
	case "execution_unknown":
		v.Message, v.Recovery = "The execution outcome is unknown.", "inspect_execution"
	case "cancelled":
		v.Message, v.Recovery = "The operation was cancelled.", "inspect_execution"
	case "deadline_exceeded":
		v.Message, v.Recovery = "The operation deadline was exceeded.", "inspect_execution"
	case "output_failed":
		v.Message, v.Recovery = "Cannot write machine output.", "none"
	default:
		v.Code, v.Message, v.Recovery = "operation_failed", "The operation failed.", "inspect_target"
	}
	return v
}

func (e MachineError) Error() string { return e.projection().Message }

// MarshalJSON deliberately has no cause or arbitrary details field.
func (e MachineError) MarshalJSON() ([]byte, error) { return json.Marshal(e.projection()) }
