package kmx

import "fmt"

// UnsupportedCapabilityError is returned instead of approximating an
// operation that a platform or runtime does not implement.
type UnsupportedCapabilityError struct {
	Component  string
	Capability string
}

func (e *UnsupportedCapabilityError) Error() string {
	return fmt.Sprintf("%s does not support %s", e.Component, e.Capability)
}

// OutcomeUnknownError reports that an operation may have changed remote state
// but its result could not be established. Callers recover by OperationID;
// they must not assume absence or repeat a non-idempotent action.
type OutcomeUnknownError struct {
	operation string
	id        OperationID
	err       error
}

func NewOutcomeUnknownError(operation string, id OperationID, err error) (*OutcomeUnknownError, error) {
	if err := validateIdentity("operation", operation); err != nil {
		return nil, err
	}
	if err := id.Validate(); err != nil {
		return nil, err
	}
	return &OutcomeUnknownError{operation: operation, id: id, err: err}, nil
}

func (e *OutcomeUnknownError) Error() string {
	if e.err == nil {
		return fmt.Sprintf("%s %q outcome is unknown", e.operation, e.id)
	}
	return fmt.Sprintf("%s %q outcome is unknown: %v", e.operation, e.id, e.err)
}

func (e *OutcomeUnknownError) Operation() string        { return e.operation }
func (e *OutcomeUnknownError) OperationID() OperationID { return e.id }
func (e *OutcomeUnknownError) Unwrap() error            { return e.err }
