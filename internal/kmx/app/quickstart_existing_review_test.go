package app

import (
	"context"
	"errors"
	"testing"
)

// A reconnect must not certify readiness based only on discovery of a name.
// The read-only fixture rejects every admission/write/Task request.
func TestQuickstartExistingAgentReadinessIsObserved(t *testing.T) {
	for _, state := range []string{"ready", "not ready", "stale generation", "deleted", "cancelled"} {
		t.Run(state, func(t *testing.T) {
			a, _, f := nativeFixture(t)
			generation := 2
			observed := 2
			if state == "stale generation" {
				observed = 1
			}
			f.objects["agents.core.orka.ai/existing"] = map[string]any{
				"kind":     "Agent",
				"metadata": map[string]any{"name": "existing", "namespace": OrkaNamespace, "uid": "existing-uid", "generation": generation},
				"status":   map[string]any{"ready": state != "not ready", "conditions": []any{map[string]any{"type": "Ready", "status": "True", "observedGeneration": observed}}},
			}
			if state == "deleted" {
				delete(f.objects, "agents.core.orka.ai/existing")
			}
			ctx := t.Context()
			if state == "cancelled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			before := f.calls
			err := a.checkQuickstartExistingAgent(ctx, "existing", OrkaNamespace)
			if (err == nil) != (state == "ready") {
				t.Fatalf("readiness %s: %v", state, err)
			}
			if state == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel lost: %v", err)
			}
			if state != "cancelled" && f.calls-before != 1 {
				t.Fatalf("reconnect did more than one read: %d calls", f.calls-before)
			}
		})
	}
}
