package copilotd

import (
	"errors"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/microsoft/agent-host-protocol/clients/go/ahptypes"
)

const observationLimit = 512

// Delivery is serialized independently of state. Terminal wake-up follows sink
// delivery, without holding the trace lock over IO or calling RPC from a sink.
func (t *trace) accept(e ahptypes.ActionEnvelope) error {
	t.delivery.Lock()
	defer t.delivery.Unlock()
	t.mu.Lock()
	err := t.acceptLocked(e)
	batch := t.immediate
	t.immediate = nil
	if len(t.models) > 0 {
		batch = append(batch, t.queued...)
		t.queued = nil
	}
	if err != nil {
		batch = nil
		t.queued = nil
	}
	t.mu.Unlock()
	for _, o := range batch {
		if t.observe != nil {
			if sinkErr := t.observe(o); sinkErr != nil {
				t.mu.Lock()
				err = t.fail(errors.Join(agentpreview.ErrDiagnosticsFailed, sinkErr))
				t.mu.Unlock()
				break
			}
		}
	}
	t.mu.Lock()
	if t.ended && !t.notified {
		close(t.done)
		t.notified = true
	}
	t.mu.Unlock()
	return err
}

// Called under mu, after the corresponding action's acceptance checks.
func (t *trace) observation(kind, name string, ordinal int) {
	if t.observe == nil {
		return
	}
	o := agentpreview.Observation{Event: kind, ToolName: name, CallID: ordinal}
	if t.meta.TurnObserved {
		o.Turn = t.turn
	}
	if kind == "session.observed" {
		t.immediate = append(t.immediate, o)
		return
	}
	if t.liveDropped {
		return
	}
	if len(t.queued) >= observationLimit {
		t.queued = nil
		t.liveDropped = true
		return
	}
	t.queued = append(t.queued, o)
}

func nonnegative(n *int64) *int64 {
	if n == nil || *n < 0 {
		return nil
	}
	v := *n
	return &v
}
func (t *trace) telemetry() agentpreview.Telemetry {
	t.mu.Lock()
	defer t.mu.Unlock()
	m := t.meta
	m.TraceComplete = m.TraceComplete && t.err == nil
	m.Requested = len(t.calls)
	m.Completed = 0
	for _, call := range t.calls {
		if call.complete {
			m.Completed++
		}
	}
	if m.UsageReports != 1 {
		m.InputTokens = nil
		m.OutputTokens = nil
	} else {
		m.InputTokens = nonnegative(m.InputTokens)
		m.OutputTokens = nonnegative(m.OutputTokens)
	}
	return m
}
