package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func machineFileFixture(t *testing.T, runner string, emit func(MachineFileProgress) error) *MachineFileObserver {
	t.Helper()
	m := NewMachineFileObserver(runner, emit)
	m.bind([]agentfile.Example{{ID: "first"}, {ID: "second"}}, "revision", "snapshot", modelaccess.Route{Provider: "copilot", Model: "gpt-4.1"})
	return m
}

type machinePreviewRunner func(context.Context, agentpreview.Request) (agentpreview.Outcome, error)

func (f machinePreviewRunner) Run(ctx context.Context, r agentpreview.Request) (agentpreview.Outcome, error) {
	return f(ctx, r)
}

func TestMachineFileAppWiringAndFinalVeto(t *testing.T) {
	for _, failure := range []string{"", "journal", "credential"} {
		t.Run(failure, func(t *testing.T) {
			var progress []MachineFileProgress
			m := NewMachineFileObserver("copilotd", func(p MachineFileProgress) error { progress = append(progress, p); return nil })
			runner := machinePreviewRunner(func(ctx context.Context, req agentpreview.Request) (agentpreview.Outcome, error) {
				if req.TelemetryReady != nil {
					if err := req.TelemetryReady(func(...string) error { return nil }); err != nil {
						return agentpreview.Outcome{}, err
					}
				}
				out, err := nativeScript(nil).Run(ctx, req)
				for i := range out.Examples {
					c := &out.Examples[i].Completion
					c.Telemetry = &agentpreview.Telemetry{Sent: true, TraceComplete: true, TurnObserved: true, Requested: 7, Completed: 7}
					if req.Observed != nil {
						_ = req.ObserveCase(i, agentpreview.Observation{Event: "case.started"})
					}
					if req.ExampleFinished != nil {
						req.ExampleFinished(out.Examples[i])
					}
				}
				if failure == "journal" {
					out.State = agentpreview.Failed
					err = agentpreview.ErrJournalFailed
				}
				if failure == "credential" {
					for i := range out.Examples {
						out.Examples[i].Completion.Native = nil
					}
					err = agentpreview.ErrCredentialContent
				}
				return out, err
			})
			r, err := (&App{FileRunner: runner}).PreviewAgentFile(t.Context(), FilePreviewOptions{Machine: m, File: previewFile(t), Operation: "test", Model: "gpt-4.1", DefaultCopilot: true, Copilotd: true})
			p := m.Final(r)
			if len(progress) == 0 || p.SelectedCases == 0 || p.StartedCases != p.SelectedCases {
				t.Fatal("observer not wired", p, progress)
			}
			if failure == "" {
				if err != nil || r.Verdict != runtime.EvaluationPass || p.RemoteState != "stopped" {
					t.Fatal(r, p, err)
				}
			} else if err == nil || r.Verdict != runtime.EvaluationUnknown {
				t.Fatal("late veto accepted as pass", r, err)
			}
			if failure == "credential" && p.RemoteState != "may_continue" {
				t.Fatal("App redaction lost send", p)
			}
			b, _ := json.Marshal(r)
			if strings.Contains(string(b), "inputTokens") || strings.Contains(string(b), "telemetry") {
				t.Fatal("legacy JSON expanded", string(b))
			}
		})
	}
}

func TestMachineFileInventoryBoundAndDeadlineFreeze(t *testing.T) {
	m := NewMachineFileObserver("embedded", nil)
	selected := make([]agentfile.Example, agentfile.MaxExamples)
	for i := range selected {
		selected[i].ID = strings.Repeat("x", 1024)
	}
	m.bind(selected, "revision", "snapshot", modelaccess.Route{})
	_ = m.ready(func(...string) error { return nil })
	p := m.Final(FilePreviewResult{})
	if len(p.Cases) != 128 || p.SummaryCount != 128 || p.StartedCases != 0 {
		t.Fatal("selected inventory lost", p)
	}
	for _, row := range p.Cases {
		b, err := json.Marshal(row)
		if err != nil || len(b)+1024 > 64*1024 {
			t.Fatal("summary record budget", len(b), err)
		}
	}
	entered, release := make(chan struct{}), make(chan struct{})
	blocked := machineFileFixture(t, "copilotd", func(p MachineFileProgress) error {
		if p.Event == "case.started" {
			close(entered)
			<-release
		}
		return nil
	})
	_ = blocked.ready(func(...string) error { return nil })
	sent := make(chan struct{})
	go func() { _ = blocked.observed(machineObservation("case.started", 1)); close(sent) }()
	<-entered
	final := make(chan MachineFileProjection, 1)
	go func() { final <- blocked.Final(FilePreviewResult{State: "cancelled"}) }()
	select {
	case <-final:
		t.Fatal("final freeze overtook in-flight sink")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	<-sent
	result := <-final
	if result.Cases[0].State != "cancelled" {
		t.Fatal(result)
	}
	_ = blocked.observed(machineObservation("case.started", 2))
	if !blocked.closed {
		t.Fatal("late deadline callback reopened collector")
	}
}

func TestMachineFileFinalStageSinkFailureVetoesPass(t *testing.T) {
	emitted := 0
	m := NewMachineFileObserver("copilotd", func(p MachineFileProgress) error {
		if p.Stage == "assertions" && p.Event == "stage.finished" {
			emitted++
			return errors.New("private sink")
		}
		return nil
	})
	runner := machinePreviewRunner(func(ctx context.Context, req agentpreview.Request) (agentpreview.Outcome, error) {
		if err := req.TelemetryReady(func(...string) error { return nil }); err != nil {
			return agentpreview.Outcome{}, err
		}
		out, err := nativeScript(nil).Run(ctx, req)
		return out, err
	})
	result, err := (&App{FileRunner: runner}).PreviewAgentFile(t.Context(), FilePreviewOptions{Machine: m, File: previewFile(t), Operation: "test", Model: "gpt-4.1", DefaultCopilot: true, Copilotd: true})
	if emitted != 1 || err == nil || result.Verdict != runtime.EvaluationUnknown || result.State != "error" || result.ReasonCode != "diagnostics_failed" {
		t.Fatal("final sink failure accepted", result, err, emitted)
	}
}

func machineObservation(event string, index int) agentpreview.Observation {
	return agentpreview.Observation{Event: event, CaseIndex: index, FileRevision: "revision", SnapshotRevision: "snapshot", Provider: "copilot", Model: "gpt-4.1"}
}

func TestMachineFileIdentityPrivacyAndBounds(t *testing.T) {
	m := machineFileFixture(t, "copilotd", nil)
	check := func(fields ...string) error {
		for _, f := range fields {
			if strings.Contains(f, "host-secret") || strings.Contains(f, "judge-secret") {
				return errors.New("private")
			}
		}
		return nil
	}
	if err := m.ready(check); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"host-secret", "judge-secret", "judge-\x01secret", strings.Repeat("a", 1025), "bad\xff"} {
		if got := m.identity(s); got.Availability != "unavailable" || got.ID != "" {
			t.Fatal("unsafe identity escaped", got)
		}
	}
	if got := m.identity(strings.Repeat("a", 1024)); got.Availability != "available" || len(got.ID) != 1024 {
		t.Fatal("safe boundary clipped", got)
	}
}

func TestMachineFileLiveAttributionAndFreeze(t *testing.T) {
	var events []MachineFileProgress
	m := machineFileFixture(t, "copilotd", func(p MachineFileProgress) error { events = append(events, p); return nil })
	_ = m.ready(func(...string) error { return nil })
	o := machineObservation("case.started", 1)
	_ = m.observed(o)
	o = machineObservation("session.observed", 1)
	o.Session = "ahp-session:/one"
	o.Chat = "ahp-chat:/one"
	o.Turn = "turn-one"
	_ = m.observed(o)
	o = machineObservation("tool.started", 1)
	o.ToolName = "read"
	o.CallID = 1
	_ = m.observed(o)
	o.Event = "tool.finished"
	_ = m.observed(o)
	m.finished(FilePreviewCaseProgress{CaseID: "first", Verdict: runtime.EvaluationPass})
	before := len(events)
	_ = m.observed(o)
	if len(events) != before {
		t.Fatal("late event after case finish")
	}
	p := m.Final(FilePreviewResult{State: "error", Verdict: runtime.EvaluationUnknown, Selection: FilePreviewSelection{Scope: "full"}})
	_ = m.observed(machineObservation("case.started", 2))
	if len(events) != before {
		t.Fatal("event after final freeze")
	}
	if len(p.Cases) != 2 || p.Cases[1].State != "not_started" || p.Cases[0].CaseIndex != 1 || p.Cases[1].CaseIndex != 2 {
		t.Fatal("lost ordered inventory", p)
	}
	b, _ := json.Marshal(events)
	for _, bad := range []string{"Host", "PendingInput", "private-call"} {
		if strings.Contains(string(b), bad) {
			t.Fatal(string(b))
		}
	}
	for _, field := range []string{"FileRevision", "SnapshotRevision", "Model", "Provider"} {
		var admitted []MachineFileProgress
		n := machineFileFixture(t, "copilotd", func(p MachineFileProgress) error { admitted = append(admitted, p); return nil })
		_ = n.ready(func(...string) error { return nil })
		o := machineObservation("case.started", 1)
		switch field {
		case "FileRevision":
			o.FileRevision = "wrong"
		case "SnapshotRevision":
			o.SnapshotRevision = "wrong"
		case "Model":
			o.Model = "wrong"
		case "Provider":
			o.Provider = "wrong"
		}
		prior := len(admitted)
		_ = n.observed(o)
		if len(admitted) != prior {
			t.Fatal("unattributed event", field)
		}
	}
}

func TestMachineFilePartialTelemetryAndRedaction(t *testing.T) {
	for _, scenario := range []string{"complete", "partial", "credential", "duplicate-usage", "embedded"} {
		t.Run(scenario, func(t *testing.T) {
			runner := "copilotd"
			if scenario == "embedded" {
				runner = "embedded"
			}
			m := machineFileFixture(t, runner, nil)
			_ = m.ready(func(...string) error { return nil })
			_ = m.observed(machineObservation("case.started", 1))
			n := fileNativeMetadata()
			c := agentpreview.Completion{ID: n.Session, Native: n, FileRevision: "revision", SnapshotRevision: "snapshot", Provider: "copilot", Model: "gpt-4.1", State: agentpreview.Complete, DurationMs: 2, Telemetry: &agentpreview.Telemetry{Sent: true, TraceComplete: true, TurnObserved: true, Requested: 7, Completed: 7, UsageReports: 1, InputTokens: new(int64)}}
			if scenario == "partial" {
				c.State = agentpreview.Cancelled
				c.Telemetry.TraceComplete = false
			}
			if scenario == "credential" {
				c.Native = nil
				c.State = agentpreview.Failed
			}
			if scenario == "duplicate-usage" {
				c.Telemetry.UsageReports = 2
				c.Telemetry.OutputTokens = new(int64)
			}
			m.returned(agentpreview.Outcome{Examples: []agentpreview.ExampleOutcome{{ID: "first", Completion: c}}})
			result := FilePreviewResult{State: "error", Verdict: runtime.EvaluationUnknown, Selection: FilePreviewSelection{Scope: "full"}, Examples: []FilePreviewExample{{ID: "first", FilePreviewCompletion: FilePreviewCompletion{State: "complete", DurationMs: 2}, Verdict: runtime.EvaluationPass}, {ID: "second", FilePreviewCompletion: FilePreviewCompletion{State: "not_run"}}}}
			if scenario == "partial" {
				result.State = "cancelled"
				result.Examples[0].State = "error"
			}
			if scenario == "credential" {
				result.ReasonCode = "credential_content"
				result.Examples = nil
				result.Selection = FilePreviewSelection{}
			}
			p := m.Final(result)
			row := p.Cases[0]
			if p.SelectedCases != 2 || p.StartedCases != 1 || p.Cases[1].State != "not_started" {
				t.Fatal(p)
			}
			if scenario == "credential" {
				if p.RemoteState != "may_continue" || row.CaseID != "" || row.Session.Availability != "unavailable" || p.FileRevision != "" {
					t.Fatal("redaction erased uncertainty/leaked", p)
				}
				return
			}
			if scenario == "embedded" {
				if row.Session.Availability != "unavailable" || row.Chat.Availability != "unavailable" || row.Turn.Availability != "unavailable" || row.Usage.Availability != "unavailable" {
					t.Fatal(row)
				}
				return
			}
			if scenario == "partial" {
				if p.RemoteState != "may_continue" || row.Calls.Availability != "unavailable" || row.Calls.Requested != nil || row.Calls.Reason != "incomplete_trace" {
					t.Fatal(p)
				}
				return
			}
			if p.RemoteState != "stopped" || row.Calls.Requested == nil || *row.Calls.Requested != 7 || row.Task.Availability != "unavailable" {
				t.Fatal(p)
			}
			if scenario == "duplicate-usage" {
				if row.Usage.Availability != "unavailable" || row.Usage.InputTokens != nil || row.Usage.OutputTokens != nil || row.Usage.Reason != "ambiguous" {
					t.Fatal(row)
				}
			} else if row.Usage.Availability != "available" || row.Usage.InputTokens == nil || *row.Usage.InputTokens != 0 || row.Usage.OutputTokens != nil {
				t.Fatal("zero/absent conflated", row)
			}
		})
	}
}
