package copilotd

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
)

func TestRunnerTelemetryWireAndSinkFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "live", true: "sink"}[fail], func(t *testing.T) {
			fixture := &wireFixture{UsageAtEnd: true}
			r, _, _ := wireRunner(t, false, false, fixture)
			req := wireRequest()
			var mu sync.Mutex
			ready := false
			finished := 0
			seen := map[int]int{}
			req.TelemetryReady = func(func(...string) error) error { ready = true; return nil }
			req.Observed = func(o agentpreview.Observation) error {
				mu.Lock()
				defer mu.Unlock()
				if !ready || o.CaseIndex < 1 || o.CaseIndex > 2 || o.FileRevision != req.Agent.Revision || o.Provider != "copilot" || o.Model != "model" {
					t.Error("unadmitted telemetry", o)
				}
				if o.Event == "tool.finished" {
					seen[o.CaseIndex]++
					if o.CallID < 1 || o.ToolName != "bash" || o.Turn == "" {
						t.Error("uncorrelated tool", o)
					}
					if fail {
						return errors.New("private sink text")
					}
				}
				return nil
			}
			req.ExampleFinished = func(row agentpreview.ExampleOutcome) {
				mu.Lock()
				defer mu.Unlock()
				finished++
				if seen[finished] != 7 {
					t.Error("case completed before tools drained", seen)
				}
			}
			out, err := r.Run(t.Context(), req)
			if !fail {
				if err != nil || finished != 2 {
					t.Fatal(out, err, finished)
				}
				for _, row := range out.Examples {
					m := row.Completion.Telemetry
					if m == nil || !m.Sent || !m.TraceComplete || !m.TurnObserved || m.UsageReports != 1 || m.InputTokens != nil || m.Requested != 7 || m.Completed != 7 {
						t.Fatal("lost native observations", m)
					}
				}
			} else if !errors.Is(err, agentpreview.ErrDiagnosticsFailed) || fixture.Sends.Load() != 1 || finished != 0 {
				t.Fatal("sink failure lost/continued", err, fixture.Sends.Load(), finished)
			}
		})
	}
}

func TestRunnerTelemetryStopsBeforeNextCase(t *testing.T) {
	r, _, _ := wireRunner(t, false, false)
	req := wireRequest()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req.Observed = func(o agentpreview.Observation) error {
		if o.CaseIndex > 1 {
			t.Error("started a later case after cancellation")
		}
		return nil
	}
	req.ExampleFinished = func(agentpreview.ExampleOutcome) { cancel() }
	_, err := r.Run(ctx, req)
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestRunnerTelemetryLostAckAndCredentialWipe(t *testing.T) {
	t.Run("lost ack", func(t *testing.T) {
		fixture := &wireFixture{DropDispatch: true}
		r, _, _ := wireRunner(t, false, false, fixture)
		out, err := r.Run(t.Context(), wireRequest())
		if err == nil || len(out.Examples) != 1 {
			t.Fatal(out, err)
		}
		m := out.Examples[0].Completion.Telemetry
		if m == nil || !m.Sent || m.TraceComplete || fixture.Sends.Load() != 1 {
			t.Fatal("failed send became not-started", m)
		}
	})
	t.Run("credential wipe", func(t *testing.T) {
		j := &fakeJudge{check: func(fields ...string) error {
			for _, f := range fields {
				if strings.Contains(f, "opaque&private") {
					return agentpreview.ErrCredentialContent
				}
			}
			return nil
		}}
		r, _, _ := wireRunnerWith(t, false, false, withJudge(j, new(atomic.Int64), nil), &wireFixture{HostVersion: "opaque&private"})
		out, err := r.Run(t.Context(), judgeRequest())
		if !errors.Is(err, agentpreview.ErrCredentialContent) || len(out.Examples) != 1 {
			t.Fatal(out, err)
		}
		c := out.Examples[0].Completion
		if c.Native != nil || c.Telemetry == nil || !c.Telemetry.Sent {
			t.Fatal("credential redaction erased attempted send", c)
		}
	})
}
