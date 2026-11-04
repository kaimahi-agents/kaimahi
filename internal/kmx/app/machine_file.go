package app

import (
	"fmt"
	"sync"
	"unicode/utf8"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
)

type MachineIdentity struct {
	Availability string `json:"availability"`
	ID           string `json:"id,omitempty"`
}
type MachineUsage struct {
	Availability string `json:"availability"`
	InputTokens  *int64 `json:"inputTokens"`
	OutputTokens *int64 `json:"outputTokens"`
	Reason       string `json:"reason,omitempty"`
}
type MachineCalls struct {
	Availability string `json:"availability"`
	Requested    *int   `json:"requested"`
	Completed    *int   `json:"completed"`
	Reason       string `json:"reason,omitempty"`
}
type MachineFileCase struct {
	CaseIndex  int             `json:"caseIndex"`
	CaseID     string          `json:"caseId,omitempty"`
	State      string          `json:"state"`
	Verdict    string          `json:"verdict"`
	DurationMs *int64          `json:"durationMs"`
	Session    MachineIdentity `json:"session"`
	Chat       MachineIdentity `json:"chat"`
	Turn       MachineIdentity `json:"turn"`
	Task       MachineIdentity `json:"task"`
	Calls      MachineCalls    `json:"calls"`
	Usage      MachineUsage    `json:"usage"`
	JudgeUsage MachineUsage    `json:"judgeUsage"`
}

// MachineFileProgress is an allowlisted projection, not a raw runner action.
type MachineFileProgress struct {
	Event     string           `json:"event"`
	Stage     string           `json:"stage,omitempty"`
	Status    string           `json:"status,omitempty"`
	CaseIndex int              `json:"caseIndex,omitempty"`
	CaseID    string           `json:"caseId,omitempty"`
	Session   *MachineIdentity `json:"session,omitempty"`
	Chat      *MachineIdentity `json:"chat,omitempty"`
	Turn      *MachineIdentity `json:"turn,omitempty"`
	ToolName  string           `json:"toolName,omitempty"`
	CallID    string           `json:"callId,omitempty"`
}
type MachineFileProjection struct {
	Runner           string            `json:"runner,omitempty"`
	Selection        string            `json:"selection"`
	Evidence         string            `json:"evidence"`
	FileRevision     string            `json:"fileRevision,omitempty"`
	SnapshotRevision string            `json:"snapshotRevision,omitempty"`
	SelectedCases    int               `json:"selectedCases"`
	StartedCases     int               `json:"startedCases"`
	CompletedCases   int               `json:"completedCases"`
	SummaryCount     int               `json:"summaryCount"`
	RemoteState      string            `json:"remoteState"`
	Cases            []MachineFileCase `json:"-"`
}
type machineCaseState struct {
	row                              MachineFileCase
	rawSession, rawChat, rawTurn     string
	started, finished, traceComplete bool
	calls                            map[int]string
}

// MachineFileObserver owns only checked presentation. Holding mu across the sink
// serializes final freeze against in-flight and late receive-side observations.
type MachineFileObserver struct {
	mu                               sync.Mutex
	emit                             func(MachineFileProgress) error
	check                            func(...string) error
	runner                           string
	ids                              []string
	revision, snapshot               string
	route                            modelaccess.Route
	cases                            []machineCaseState
	current                          int
	closed, disabled, sent, prepared bool
}

func NewMachineFileObserver(runner string, emit func(MachineFileProgress) error) *MachineFileObserver {
	return &MachineFileObserver{runner: runner, emit: emit}
}
func (m *MachineFileObserver) bind(selected []agentfile.Example, rev, snapshot string, route modelaccess.Route) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.revision, m.snapshot, m.route = rev, snapshot, route
	for i, ex := range selected {
		m.ids = append(m.ids, ex.ID)
		unavailable := MachineIdentity{Availability: "unavailable"}
		m.cases = append(m.cases, machineCaseState{row: MachineFileCase{CaseIndex: i + 1, State: "not_started", Verdict: "unknown", Session: unavailable, Chat: unavailable, Turn: unavailable, Task: unavailable, Calls: MachineCalls{Availability: "unavailable", Reason: "not_reported"}, Usage: MachineUsage{Availability: "unavailable", Reason: "not_reported"}, JudgeUsage: MachineUsage{Availability: "unavailable", Reason: "not_reported"}}, calls: map[int]string{}})
	}
}
func (m *MachineFileObserver) identity(raw string) MachineIdentity {
	unavailable := MachineIdentity{Availability: "unavailable"}
	if m.check == nil || raw == "" || len(raw) > 1024 || !utf8.ValidString(raw) {
		return unavailable
	}
	views, safe, ok := suiteDiagnosticViews(raw, len(raw))
	if !ok || safe != raw || m.check(views...) != nil {
		return unavailable
	}
	if checked, ok := suiteDiagnosticText(raw, len(raw), nil); !ok || checked != raw {
		return unavailable
	}
	return MachineIdentity{Availability: "available", ID: raw}
}
func (m *MachineFileObserver) stage(stage, status string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || !m.prepared {
		return nil
	}
	p := MachineFileProgress{Event: "stage.finished", Stage: stage, Status: status}
	if status == "active" {
		p.Event = "stage.started"
		p.Status = ""
	}
	return m.publish(p)
}
func (m *MachineFileObserver) publish(p MachineFileProgress) error {
	p.CaseID = m.identity(p.CaseID).ID
	for _, field := range []**MachineIdentity{&p.Session, &p.Chat, &p.Turn} {
		if *field != nil {
			v := m.identity((*field).ID)
			*field = &v
		}
	}
	if m.emit != nil {
		return m.emit(p)
	}
	return nil
}
func (m *MachineFileObserver) ready(check func(...string) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.check = check
	m.prepared = true
	for i := range m.cases {
		m.cases[i].row.CaseID = m.identity(m.ids[i]).ID
	}
	if err := m.publish(MachineFileProgress{Event: "stage.finished", Stage: "preflight", Status: "complete"}); err != nil {
		return err
	}
	return m.publish(MachineFileProgress{Event: "stage.started", Stage: "execution"})
}
func (m *MachineFileObserver) observed(o agentpreview.Observation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.disabled || !m.prepared {
		return nil
	}
	if o.FileRevision != m.revision || o.SnapshotRevision != m.snapshot || o.Provider != m.route.Provider || o.Model != m.route.Model || o.CaseIndex < 1 || o.CaseIndex > len(m.cases) {
		m.disabled = true
		return nil
	}
	c := &m.cases[o.CaseIndex-1]
	if o.Event == "case.started" {
		if o.CaseIndex != m.current+1 {
			m.disabled = true
			return nil
		}
		m.current = o.CaseIndex
		c.started = true
		c.row.State = "unknown"
	} else if o.CaseIndex != m.current || c.finished {
		return nil
	}
	p := MachineFileProgress{Event: o.Event, CaseIndex: o.CaseIndex, CaseID: c.row.CaseID}
	switch o.Event {
	case "case.started":
	case "session.observed":
		if m.runner != "copilotd" {
			return nil
		}
		for i := range m.cases {
			if i != o.CaseIndex-1 && o.Session != "" && m.cases[i].rawSession == o.Session {
				m.disabled = true
				return nil
			}
		}
		for _, pair := range []struct {
			raw      string
			previous *string
			dst      *MachineIdentity
			event    **MachineIdentity
		}{{o.Session, &c.rawSession, &c.row.Session, &p.Session}, {o.Chat, &c.rawChat, &c.row.Chat, &p.Chat}, {o.Turn, &c.rawTurn, &c.row.Turn, &p.Turn}} {
			if pair.raw == "" {
				continue
			}
			if *pair.previous != "" && *pair.previous != pair.raw {
				m.disabled = true
				return nil
			}
			*pair.previous = pair.raw
			v := m.identity(pair.raw)
			*pair.dst = v
			*pair.event = &v
		}
	case "tool.started", "tool.finished":
		if m.runner != "copilotd" || o.CallID < 1 {
			return nil
		}
		if o.Event == "tool.started" {
			if _, exists := c.calls[o.CallID]; exists {
				return nil
			}
			c.calls[o.CallID] = o.ToolName
		} else if c.calls[o.CallID] != o.ToolName {
			return nil
		}
		p.ToolName = m.identity(o.ToolName).ID
		if p.ToolName == "" {
			return nil
		}
		p.CallID = fmt.Sprintf("call-%d", o.CallID)
	default:
		return nil
	}
	return m.publish(p)
}
func (m *MachineFileObserver) finished(p FilePreviewCaseProgress) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.disabled || m.current < 1 || m.ids[m.current-1] != p.CaseID {
		return
	}
	c := &m.cases[m.current-1]
	c.finished = true
	c.row.Verdict = string(p.Verdict)
	_ = m.publish(MachineFileProgress{Event: "case.finished", CaseIndex: m.current, CaseID: c.row.CaseID}) // shared writer latches/cancels
}

// Sent is conservative even if a credential veto erased Native. Other fields
// need the admitted source/route and existing native validation before retention.
func (m *MachineFileObserver) returned(out agentpreview.Outcome) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, r := range out.Examples {
		c := r.Completion
		if c.Telemetry != nil && c.Telemetry.Sent {
			m.sent = true
		}
		if i >= len(m.cases) || r.ID != m.ids[i] || c.FileRevision != m.revision || c.SnapshotRevision != m.snapshot || c.Provider != m.route.Provider || c.Model != m.route.Model {
			continue
		}
		row := &m.cases[i]
		if c.DurationMs < 0 || c.DurationMs > 300000 {
			continue
		}
		if m.runner != "copilotd" || c.Native == nil || c.ID != c.Native.Session || !validNative(c.Native) {
			continue
		}
		n := c.Native
		row.started = true
		row.row.State = "unknown"
		row.row.DurationMs = &c.DurationMs
		row.row.Session = m.identity(n.Session)
		row.row.Chat = m.identity(n.Chat)
		if c.Telemetry == nil {
			continue
		}
		t := c.Telemetry
		if t.TurnObserved {
			row.row.Turn = m.identity(n.Turn)
		}
		validModel := len(n.ActualModels) > 0
		for _, model := range n.ActualModels {
			validModel = validModel && model == m.route.Model
		}
		row.traceComplete = t.TraceComplete && validModel
		if row.traceComplete && t.Requested >= 0 && t.Completed >= 0 && t.Completed <= t.Requested {
			a, b := t.Requested, t.Completed
			row.row.Calls = MachineCalls{Availability: "available", Requested: &a, Completed: &b}
		} else {
			row.row.Calls = MachineCalls{Availability: "unavailable", Reason: "incomplete_trace"}
		}
		if t.UsageReports > 1 {
			row.row.Usage = MachineUsage{Availability: "unavailable", Reason: "ambiguous"}
		} else if t.UsageReports == 1 && validModel {
			u := MachineUsage{Availability: "unavailable", Reason: "not_reported"}
			if t.InputTokens != nil && *t.InputTokens >= 0 {
				v := *t.InputTokens
				u.InputTokens = &v
			}
			if t.OutputTokens != nil && *t.OutputTokens >= 0 {
				v := *t.OutputTokens
				u.OutputTokens = &v
			}
			if u.InputTokens != nil || u.OutputTokens != nil {
				u.Availability = "available"
				u.Reason = ""
			}
			row.row.Usage = u
		}
	}
}

// Final freezes live events after required execution/judge/source/close checks.
// This projection never grants a verdict; the existing App result owns it.
func (m *MachineFileObserver) Final(r FilePreviewResult) MachineFileProjection {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	p := MachineFileProjection{Runner: m.runner, Selection: r.Selection.Scope, Evidence: "diagnostic_preview", SelectedCases: len(m.cases), SummaryCount: len(m.cases), RemoteState: "not_started", Cases: []MachineFileCase{}}
	if p.Selection == "" {
		p.Selection = "full"
	}
	redacted := r.ReasonCode == "credential_content" || r.ReasonCode == "unsafe_tool_content"
	if !redacted {
		p.FileRevision = m.identity(m.revision).ID
		p.SnapshotRevision = m.identity(m.snapshot).ID
	}
	allTerminal := true
	for i := range m.cases {
		c := &m.cases[i]
		if i < len(r.Examples) && r.Examples[i].ID == m.ids[i] && r.Examples[i].State == "complete" {
			c.started = true
			c.row.State = "complete"
			c.row.Verdict = string(r.Examples[i].Verdict)
			v := r.Examples[i].DurationMs
			c.row.DurationMs = &v
			if m.runner == "embedded" {
				a, b := 0, r.Examples[i].ToolCalls
				for _, tool := range r.Examples[i].Tools {
					a += tool.RequestCount
				}
				c.row.Calls = MachineCalls{Availability: "available", Requested: &a, Completed: &b}
				c.traceComplete = r.State == "complete"
			}
		}
		if c.started {
			p.StartedCases++
			allTerminal = allTerminal && c.traceComplete
			if c.row.State == "complete" {
				p.CompletedCases++
			} else if r.State == "cancelled" {
				c.row.State = "cancelled"
			}
		}
		if redacted {
			c.row.CaseID = ""
			c.row.Session = MachineIdentity{Availability: "unavailable"}
			c.row.Chat = c.row.Session
			c.row.Turn = c.row.Session
			c.row.Calls = MachineCalls{Availability: "unavailable", Reason: "incomplete_trace"}
			c.row.Usage = MachineUsage{Availability: "unavailable", Reason: "not_reported"}
			c.traceComplete = false
			allTerminal = false
		}
		c.row.CaseID = m.identity(c.row.CaseID).ID
		c.row.Session = m.identity(c.row.Session.ID)
		c.row.Chat = m.identity(c.row.Chat.ID)
		c.row.Turn = m.identity(c.row.Turn.ID)
		p.Cases = append(p.Cases, c.row)
	}
	if m.sent {
		p.RemoteState = "may_continue"
		if allTerminal && !redacted {
			p.RemoteState = "stopped"
		}
	} else if p.StartedCases > 0 {
		p.RemoteState = "unknown"
		if allTerminal {
			p.RemoteState = "stopped"
		}
	}
	return p
}
