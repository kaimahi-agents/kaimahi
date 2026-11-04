package localpreview

import (
	"errors"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"testing"
	"time"
)

func TestRunnerTelemetryReadyAndCaseEntry(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{true: "sink", false: "observed"}[fail], func(t *testing.T) {
			e := &runnerExecutor{}
			r := injectedRunner(e, time.Minute)
			req := runnerRequest(t)
			ready := false
			started := 0
			req.TelemetryReady = func(check func(...string) error) error {
				ready = true
				if check("safe") != nil {
					t.Fatal("safe metadata rejected")
				}
				return nil
			}
			req.Observed = func(o agentpreview.Observation) error {
				if !ready || o.Event != "case.started" || o.CaseIndex != started+1 || o.FileRevision != req.Agent.Revision || o.Model != "fake" || o.Session != "" || o.Turn != "" {
					t.Error("invented or unadmitted embedded telemetry", o)
				}
				started++
				if fail {
					return errors.New("private sink")
				}
				return nil
			}
			out, err := r.Run(t.Context(), req)
			if !fail {
				if err != nil || !ready || started != 2 || len(e.turns) != 2 {
					t.Fatal(out, err, ready, started)
				}
			} else if !errors.Is(err, agentpreview.ErrDiagnosticsFailed) || len(e.turns) != 0 {
				t.Fatal("sink failure did not prevent send", err, len(e.turns))
			}
		})
	}
}
