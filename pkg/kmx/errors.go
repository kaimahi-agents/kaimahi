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
// but its result could not be established. Callers must inspect or reconcile;
// they must not assume absence or automatically repeat a non-idempotent action.
type OutcomeUnknownError struct {
	Operation string
	Err       error
}

func (e *OutcomeUnknownError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("%s outcome is unknown", e.Operation)
	}
	return fmt.Sprintf("%s outcome is unknown: %v", e.Operation, e.Err)
}

func (e *OutcomeUnknownError) Unwrap() error { return e.Err }
