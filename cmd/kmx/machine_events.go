package main

import (
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"time"
)

// envelope is called under the writer lock so events and result share ordering.
func (m *machineOutput) envelope(kind string) app.MachineEnvelope {
	m.seq++
	return app.MachineEnvelope{SchemaVersion: 1, Kind: kind, Operation: m.operation, OperationID: m.id, Seq: m.seq, ElapsedMs: time.Since(m.started).Milliseconds()}
}

func (m *machineOutput) event(stage, status string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	event := app.MachineEvent{MachineEnvelope: m.envelope("kmx.event"), Event: "stage.finished", Stage: stage, Status: status}
	switch status {
	case "active":
		event.Event, event.Status = "stage.started", ""
	case "done":
		event.Status = "complete"
	case "cancelled", "skipped":
	default:
		event.Status = "failed"
	}
	return m.writeLocked(event, false)
}

func (m *machineOutput) setupResult(mode string, effects []app.MachineEffect, outcome string, exit int, safe *app.MachineError) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if effects == nil {
		effects = []app.MachineEffect{}
	}
	return m.writeLocked(app.MachineSetupResult{MachineResult: app.MachineResult{MachineEnvelope: m.envelope("kmx.result"), Outcome: outcome, Verdict: "not_applicable", ExitCode: exit, Error: safe}, Mode: mode, Effects: effects}, true)
}
