package agentpreview

import "errors"

// ObserveCase binds metadata to the admitted request, never a host-supplied case.
func (r Request) ObserveCase(index int, o Observation) error {
	if r.Observed == nil || index < 0 {
		return nil
	}
	o.CaseIndex = index + 1
	o.FileRevision = r.Agent.Revision
	o.SnapshotRevision = r.SnapshotRevision
	o.Provider = r.Route.Provider
	o.Model = r.Route.Model
	if err := r.Observed(o); err != nil {
		return errors.Join(ErrDiagnosticsFailed, err)
	}
	return nil
}

// Observation is private runner metadata, not a wire record or execution proof.
// Source attribution comes from the admitted request, never from host payloads.
type Observation struct {
	Event                                           string
	CaseIndex                                       int
	FileRevision, SnapshotRevision, Provider, Model string
	Session, Chat, Turn                             string
	ToolName                                        string
	CallID                                          int
}

// Telemetry retains uncertainty independently of Native's display/content veto.
// In particular Sent survives credential redaction; it is set before Dispatch.
type Telemetry struct {
	Sent, TurnObserved, TraceComplete  bool
	Requested, Completed, UsageReports int
	InputTokens, OutputTokens          *int64
}
