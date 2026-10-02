package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runview"
	runorka "github.com/kaimahi-agents/kaimahi/internal/kmx/runview/orka"
)

// A run pane is tied to the environment and agent captured when it opened.
// Replacing the pane invalidates late reads without silently changing context.
type consoleRunsPane struct {
	env                                    agentTUIEnvironment
	agent                                  agentTUIAgent
	ctx                                    context.Context
	cancel                                 context.CancelFunc
	loading, runLoading                    bool
	list                                   consoleRunList
	listErr, runErr                        string
	readAt                                 time.Time
	selection, taskSelection, detailScroll int
	run                                    *runview.Run
	detail                                 bool
}

func (p *consoleRunsPane) stop() {
	if p != nil && p.cancel != nil {
		p.cancel()
	}
}

type consoleRunsListed struct {
	pane *consoleRunsPane
	list consoleRunList
	err  error
}
type consoleRunsRead struct {
	pane *consoleRunsPane
	run  runview.Run
	err  error
}

// Never display an external command's stderr or a native Task payload in the
// console. The absence of list permission is distinct from a successful empty list.
func consoleRunError(err error) string {
	if err == nil {
		return ""
	}
	if strings.Contains(err.Error(), "cannot establish destination cluster identity (kube-system UID)") {
		return "kube-system Namespace identity unavailable (get access required)"
	}
	if errors.Is(err, runorka.ErrDenied) || strings.Contains(err.Error(), "access forbidden") {
		return "permission denied"
	}
	if strings.Contains(err.Error(), "root Task UID changed") {
		return "root Task identity changed"
	}
	return "read unavailable"
}
func (m agentTUIModel) openRuns() (tea.Model, tea.Cmd) {
	agent := m.selected()
	if agent == nil {
		return m, nil
	}
	if agent.External {
		m.status = "Runs are available for native Orka agents only"
		return m, nil
	}
	return m.openRunsFor(m.columns[m.focus].Env, *agent)
}
func (m agentTUIModel) openRunsFor(env agentTUIEnvironment, agent agentTUIAgent) (tea.Model, tea.Cmd) {
	m.runs.stop()
	p := &consoleRunsPane{env: env, agent: agent, loading: true}
	m.runs = p
	if m.opt.Demo {
		p.loading, p.list = false, consoleDemoRunList(env, agent)
		return m, nil
	}
	if m.loadRuns == nil {
		p.loading, p.listErr = false, "run listing unavailable"
		return m, nil
	}
	parent := m.inferenceContext
	if parent == nil {
		parent = context.Background()
	}
	p.ctx, p.cancel = context.WithCancel(parent)
	load := m.loadRuns
	return m, func() tea.Msg {
		list, err := load(p.ctx, p.env, p.agent)
		return consoleRunsListed{pane: p, list: list, err: err}
	}
}
func (m agentTUIModel) reloadRuns() (tea.Model, tea.Cmd) {
	if m.runs == nil {
		return m, nil
	}
	return m.openRunsFor(m.runs.env, m.runs.agent)
}
func (m agentTUIModel) openRunRef(ref consoleRunRef) (tea.Model, tea.Cmd) {
	old := m.runs
	old.stop()
	next := *old
	p := &next
	p.cancel = nil
	p.ctx = nil
	p.run = nil
	p.runErr = ""
	p.runLoading = true
	p.taskSelection, p.detailScroll = 0, 0
	p.detail = false
	m.runs = p
	if m.opt.Demo {
		run := consoleDemoRun(ref)
		run.Tasks = consoleRunTaskOrder(run.Tasks)
		p.run = &run
		p.readAt = ref.CreatedAt.Add(10 * time.Minute)
		p.runLoading = false
		return m, nil
	}
	if m.loadRun == nil {
		p.runLoading, p.runErr = false, "run read unavailable"
		return m, nil
	}
	parent := m.inferenceContext
	if parent == nil {
		parent = context.Background()
	}
	p.ctx, p.cancel = context.WithCancel(parent)
	load := m.loadRun
	return m, func() tea.Msg {
		run, err := load(p.ctx, p.env, ref)
		return consoleRunsRead{pane: p, run: run, err: err}
	}
}
func (m agentTUIModel) updateRuns(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	p := m.runs
	if key.String() == "ctrl+c" {
		p.stop()
		m.runs = nil
		return m, tea.Quit
	}
	if key.String() == "q" {
		p.stop()
		m.runs = nil
		return m, nil
	}
	if p.detail && (key.String() == "esc" || key.String() == "i") {
		p.detail = false
		p.detailScroll = 0
		return m, nil
	}
	if p.detail {
		switch key.String() {
		case "enter":
			if m.width < 100 {
				p.detail = false
				p.detailScroll = 0
			}
		case "r":
			return m.openRunRef(p.list.Roots[p.selection])
		case "j", "down":
			p.detailScroll++
		case "k", "up":
			p.detailScroll = max(0, p.detailScroll-1)
		case "pgdown", "ctrl+d":
			p.detailScroll += max(1, m.height-4)
		case "pgup", "ctrl+u":
			p.detailScroll = max(0, p.detailScroll-max(1, m.height-4))
		case "g", "home":
			p.detailScroll = 0
		case "G", "end":
			p.detailScroll = 1 << 20
		}
		return m, nil
	}
	if p.run != nil || p.runLoading || p.runErr != "" {
		switch key.String() {
		case "esc":
			p.stop()
			p.run = nil
			p.runErr = ""
			p.runLoading = false
			p.detail = false
			return m, nil
		case "r":
			return m.openRunRef(p.list.Roots[p.selection])
		case "j", "down":
			if p.run != nil && len(p.run.Tasks) > 0 {
				p.taskSelection = min(p.taskSelection+1, len(p.run.Tasks)-1)
			}
		case "k", "up":
			p.taskSelection = max(0, p.taskSelection-1)
		case "g", "home":
			p.taskSelection = 0
		case "G", "end":
			if p.run != nil {
				p.taskSelection = max(0, len(p.run.Tasks)-1)
			}
		case "i", "enter":
			if p.run != nil && len(p.run.Tasks) > 0 {
				p.detail = true
				p.detailScroll = 0
			}
		case "pgdown", "ctrl+d":
			if p.run != nil {
				p.taskSelection = min(p.taskSelection+max(1, m.height-8), max(0, len(p.run.Tasks)-1))
			}
		case "pgup", "ctrl+u":
			p.taskSelection = max(0, p.taskSelection-max(1, m.height-8))
		}
		return m, nil
	}
	switch key.String() {
	case "esc":
		p.stop()
		m.runs = nil
	case "r":
		return m.reloadRuns()
	case "j", "down":
		p.selection = min(p.selection+1, max(0, len(p.list.Roots)-1))
	case "k", "up":
		p.selection = max(0, p.selection-1)
	case "g", "home":
		p.selection = 0
	case "G", "end":
		p.selection = max(0, len(p.list.Roots)-1)
	case "enter":
		if !p.loading && p.listErr == "" && p.selection < len(p.list.Roots) {
			return m.openRunRef(p.list.Roots[p.selection])
		}
	}
	return m, nil
}

// All drawn text is a short, sanitized projection. No Task spec, model output,
// event content, or unfiltered command error reaches this renderer.
func (m agentTUIModel) runsView() string {
	w, h := max(1, m.width), max(1, m.height)
	fit := func(s string, width int) string { return ansi.Truncate(tuiOneLine(s), max(1, width), "…") }
	if w < 30 || h < 8 {
		return fit("Resize to at least 30×8 · ctrl+c quits", w)
	}
	p := m.runs
	prefix := "KMX · "
	if m.opt.Demo {
		prefix += "DEMO · "
	}
	title := prefix + "RECENT RUNS · " + p.env.Name + " · " + p.agent.Namespace + "/" + p.agent.Name + " · READ ONLY"
	if p.run != nil {
		title = prefix + "RUN · " + p.list.Roots[p.selection].Name + " · " + p.run.Status
	}
	header := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Cyan).Render(fit(title, w))
	footer := "↑/↓ select · <enter> open · r reload · <esc> back · q agents"
	var rows []string
	anchor := 0
	switch {
	case p.run == nil && (p.runLoading || p.runErr != ""):
		if p.runLoading {
			rows = []string{"Reading authorized run evidence…"}
		} else {
			rows = []string{"Run: " + p.runErr, "Press r to retry or <esc> for recent runs."}
		}
		footer = "r retry · <esc> recent runs · q agents"
	case p.run == nil:
		rows, anchor = m.recentRunLines()
	case p.detail && w < 100:
		rows = m.runDetailLines()
		footer = "↑/↓ scroll · <esc> tasks · q agents"
	default:
		rows, anchor = m.runOverviewLines()
		if w >= 100 && len(p.run.Tasks) > 0 {
			leftWidth := max(55, w*3/5)
			rightWidth := w - leftWidth - 3
			details := m.runDetailLines()
			if p.detail && len(details) > 0 {
				details[0] = "› " + details[0]
			}
			detailStart := min(max(0, len(details)-(h-2)), p.detailScroll)
			if p.detail {
				details = details[detailStart:]
			}
			joined := make([]string, max(len(rows), len(details)))
			for i := range joined {
				left, right := "", ""
				if i < len(rows) {
					left = fit(rows[i], leftWidth)
				}
				if i < len(details) {
					right = fit(details[i], rightWidth)
				}
				joined[i] = left + strings.Repeat(" ", max(0, leftWidth-ansi.StringWidth(left))) + " │ " + right
			}
			rows = joined
		}
		footer = "↑/↓ select Task · i focus details · r refresh · <esc> recent runs · q agents"
		if p.detail {
			footer = "↑/↓ scroll details · r refresh · <esc> tasks · q agents"
		}
	}
	page := h - 2
	start := 0
	if p.detail && w < 100 {
		start = min(max(0, len(rows)-page), p.detailScroll)
	} else if anchor >= page {
		start = anchor - page + 1
	}
	start = min(start, max(0, len(rows)-page))
	visible := rows[start:min(len(rows), start+page)]
	rendered := make([]string, 0, h)
	rendered = append(rendered, header)
	for _, row := range visible {
		rendered = append(rendered, fit(row, w))
	}
	for len(rendered) < h-1 {
		rendered = append(rendered, "")
	}
	rendered = append(rendered, fit(footer, w))
	return strings.Join(rendered, "\n")
}
func (m agentTUIModel) recentRunLines() ([]string, int) {
	p := m.runs
	heading := "Recent root Tasks for this agent"
	if p.list.Missing != "" {
		heading = "Root Tasks among scanned pages"
	}
	rows := []string{heading}
	if p.loading {
		return append(rows, "Reading Tasks using the selected Kubernetes context…"), 0
	}
	if p.listErr != "" {
		return append(rows, "Run list: "+p.listErr, "The list is unavailable, not empty. Press r to retry."), 0
	}
	if p.list.Missing != "" {
		rows = append(rows, "List incomplete: "+p.list.Missing)
	}
	if len(p.list.Roots) == 0 {
		if p.list.Missing == "" {
			rows = append(rows, "No recent root Tasks found for this agent.")
		}
		return rows, 0
	}
	noun := "recent roots"
	if p.list.Missing != "" {
		noun = "scanned roots"
	}
	rows = append(rows, fmt.Sprintf("Showing %d of %d %s", len(p.list.Roots), p.list.Count, noun), "")
	anchor := 0
	for i, ref := range p.list.Roots {
		mark := "  "
		if i == p.selection {
			mark = "› "
			anchor = len(rows)
		}
		rows = append(rows, mark+consoleRunTime(ref.CreatedAt)+" · "+ref.Status+" · "+ref.Name+" · UID "+ref.UID)
	}
	return rows, anchor
}
func (m agentTUIModel) runOverviewLines() ([]string, int) {
	p := m.runs
	r := p.run
	rows := []string{"Root Task UID: " + r.ID, "Status: " + r.Status + " · Task created: " + consoleRunTime(r.StartedAt)}
	end := r.FinishedAt
	if end.IsZero() {
		end = p.readAt
	}
	if !r.StartedAt.IsZero() && !end.IsZero() && !end.Before(r.StartedAt) {
		label := "Elapsed from Task creation"
		if r.FinishedAt.IsZero() {
			label += " (at read)"
		}
		rows = append(rows, label+": "+end.Sub(r.StartedAt).Round(time.Second).String())
	}
	if !r.FinishedAt.IsZero() {
		rows = append(rows, "Finished: "+consoleRunTime(r.FinishedAt))
	}
	rows = append(rows, consoleRunEvidence("Freshness", r.FreshnessMissing), consoleRunEvidence("Discovery", r.DiscoveryMissing))
	if r.EventsMissing != nil {
		rows = append(rows, consoleRunEvidence("Events", r.EventsMissing))
	}
	if r.TraceMissing != nil {
		rows = append(rows, consoleRunEvidence("Trace", r.TraceMissing))
	}
	rows = append(rows, "", "Observed Tasks (root first; children are verified hand-offs)")
	anchor := len(rows)
	depths := make(map[string]int, len(r.Tasks))
	for i := 0; i < len(r.Tasks); i++ {
		depth := 0
		if r.Tasks[i].ParentTask != "" {
			depth = min(3, depths[r.Tasks[i].ParentTask]+1)
		}
		depths[r.Tasks[i].ID] = depth
		line := consoleRunTaskRow(r.Tasks[i], i == p.taskSelection, depth)
		if i == p.taskSelection {
			anchor = len(rows)
		}
		// At wider widths, neighbouring siblings share a row. Their separate
		// selection markers keep both lanes keyboard-identifiable.
		if m.width >= 145 && i+1 < len(r.Tasks) && r.Tasks[i].ParentTask != "" && r.Tasks[i].ParentTask == r.Tasks[i+1].ParentTask {
			leftWidth := max(55, m.width*3/5)
			half := (leftWidth - 3) / 2
			first := ansi.Truncate(tuiOneLine(consoleRunCompactTaskRow(r.Tasks[i], i == p.taskSelection)), half, "…")
			second := ansi.Truncate(tuiOneLine(consoleRunCompactTaskRow(r.Tasks[i+1], i+1 == p.taskSelection)), leftWidth-half-3, "…")
			if i+1 == p.taskSelection {
				anchor = len(rows)
			}
			depths[r.Tasks[i+1].ID] = depth
			line = first + strings.Repeat(" ", max(0, half-ansi.StringWidth(first))) + " │ " + second
			i++
		}
		rows = append(rows, line)
	}
	if len(r.Tasks) == 0 {
		rows = append(rows, "No Tasks readable")
	}
	if p.taskSelection < len(r.Tasks) && r.Tasks[p.taskSelection].EventsMissing != nil {
		rows = append(rows, consoleRunEvidence("Selected Task events", r.Tasks[p.taskSelection].EventsMissing))
	}
	rows = append(rows, "", "Declared helpers (permission only)", "Not observed hand-offs")
	if r.DeclaredMissing != nil {
		rows = append(rows, consoleRunEvidence("Policy", r.DeclaredMissing))
	}
	if len(r.DeclaredHelpers) == 0 {
		rows = append(rows, "No named helpers shown · policy "+r.DeclaredState)
	}
	for _, link := range r.DeclaredHelpers {
		rows = append(rows, "  permission only: "+link.From.Namespace+"/"+link.From.Name+" → "+link.To.Namespace+"/"+link.To.Name)
	}
	return rows, anchor
}

// Keep the root first and sort each sibling group by its observed Task time.
// The projection is copied for display only; Task UID identity is unchanged.
func consoleRunTaskOrder(tasks []runview.Task) []runview.Task {
	byParent := make(map[string][]runview.Task)
	for _, task := range tasks {
		byParent[task.ParentTask] = append(byParent[task.ParentTask], task)
	}
	for parent := range byParent {
		sort.SliceStable(byParent[parent], func(i, j int) bool {
			a, b := byParent[parent][i], byParent[parent][j]
			if a.StartedAt.Equal(b.StartedAt) {
				return a.ID < b.ID
			}
			return a.StartedAt.Before(b.StartedAt)
		})
	}
	queue := append([]runview.Task(nil), byParent[""]...)
	ordered := make([]runview.Task, 0, len(tasks))
	seen := make(map[string]bool)
	for len(queue) > 0 {
		task := queue[0]
		queue = queue[1:]
		if seen[task.ID] {
			continue
		}
		seen[task.ID] = true
		ordered = append(ordered, task)
		queue = append(queue, byParent[task.ID]...)
	}
	// Malformed ancestry cannot erase an otherwise readable Task.
	for _, task := range tasks {
		if !seen[task.ID] {
			ordered = append(ordered, task)
		}
	}
	return ordered
}
func consoleRunTaskRow(task runview.Task, selected bool, depth int) string {
	mark := "  "
	if selected {
		mark = "› "
	}
	return mark + strings.Repeat("  ", depth) + consoleRunTime(task.StartedAt) + " · " + task.Status + " · " + task.Agent.Namespace + "/" + task.Agent.Name + " · " + task.Name
}

func consoleRunCompactTaskRow(task runview.Task, selected bool) string {
	mark := "  "
	if selected {
		mark = "› "
	}
	clock := "time unknown"
	if !task.StartedAt.IsZero() {
		clock = task.StartedAt.UTC().Format("15:04:05")
	}
	return mark + task.Name + " · " + task.Status + " · " + clock
}

func (m agentTUIModel) runDetailLines() []string {
	p := m.runs
	if p.run == nil || len(p.run.Tasks) == 0 {
		return []string{"Task details unavailable"}
	}
	task := p.run.Tasks[min(p.taskSelection, len(p.run.Tasks)-1)]
	rows := []string{"Task details · " + task.Name, "UID: " + task.ID, "Agent: " + task.Agent.Namespace + "/" + task.Agent.Name, "Status: " + task.Status, "Summary: " + task.Summary}
	if task.ParentTask != "" {
		rows = append(rows, "Observed parent Task UID: "+task.ParentTask)
	}
	if !task.StartedAt.IsZero() {
		rows = append(rows, "Task created: "+consoleRunTime(task.StartedAt))
	}
	if !task.FinishedAt.IsZero() {
		rows = append(rows, "Finished: "+consoleRunTime(task.FinishedAt))
	}
	for _, e := range []struct {
		label   string
		missing *runview.Missing
	}{{"Events", task.EventsMissing}, {"Trace", task.TraceMissing}, {"Failure reason", task.FailureMissing}, {"Revision at execution", task.RevisionMissing}} {
		if e.missing != nil {
			rows = append(rows, consoleRunEvidence(e.label, e.missing))
		}
	}
	if task.Status == "Failed" && task.FailureReason != "" {
		rows = append(rows, "Failure reason: "+task.FailureReason)
	}
	rows = append(rows, "", "Recent safe events")
	count := 0
	for i := len(p.run.Events) - 1; i >= 0 && count < 5; i-- {
		e := p.run.Events[i]
		if e.TaskID == task.ID {
			rows = append(rows, fmt.Sprintf("%s · #%d · %s", consoleRunTime(e.At), e.Seq, e.Summary))
			count++
		}
	}
	if count == 0 {
		rows = append(rows, "No readable events")
	}
	return rows
}
func consoleRunEvidence(label string, missing *runview.Missing) string {
	if missing == nil {
		return label + ": available"
	}
	return label + ": " + missing.Reason + " (" + missing.Source + ")"
}
func consoleRunTime(at time.Time) string {
	if at.IsZero() {
		return "time unknown"
	}
	return at.UTC().Format("2006-01-02 15:04:05Z")
}

// Demo evidence is entirely synthetic, not mixed with real cluster data.
func consoleDemoRunList(env agentTUIEnvironment, agent agentTUIAgent) consoleRunList {
	if agent.Name != "assistant" {
		return consoleRunList{}
	}
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	return consoleRunList{Roots: []consoleRunRef{{Name: "invoice-check", UID: "demo-root-1", Namespace: agent.Namespace, Status: "Succeeded", CreatedAt: now}, {Name: "invoice-review", UID: "demo-root-2", Namespace: agent.Namespace, Status: "Running", CreatedAt: now.Add(-time.Hour)}}, Count: 2}
}
func consoleDemoRun(ref consoleRunRef) runview.Run {
	root := runview.Task{ID: ref.UID, Name: ref.Name, Agent: runview.Agent{Name: "assistant", Namespace: ref.Namespace}, Status: ref.Status, StartedAt: ref.CreatedAt, Summary: "Task activity (content redacted)", RevisionMissing: &runview.Missing{Reason: "historical binding unavailable", Source: "Orka Task"}}
	run := runview.Run{ID: ref.UID, Namespace: ref.Namespace, RootTask: ref.UID, Status: ref.Status, StartedAt: ref.CreatedAt, Tasks: []runview.Task{root}, DeclaredState: "enabled", DeclaredSource: "current Orka Agent coordination"}
	if ref.Status == "Succeeded" {
		run.FinishedAt = ref.CreatedAt.Add(3 * time.Minute)
		run.Tasks[0].FinishedAt = run.FinishedAt
	}
	if ref.UID == "demo-root-1" {
		run.Tasks = append(run.Tasks, runview.Task{ID: "demo-child-1", Name: "review-check", Agent: runview.Agent{Name: "helper", Namespace: ref.Namespace}, ParentTask: ref.UID, Status: "Succeeded", StartedAt: ref.CreatedAt.Add(time.Minute), Summary: "Task activity (content redacted)"})
		run.DeclaredHelpers = []runview.DeclaredHelper{{From: root.Agent, To: run.Tasks[1].Agent}, {From: root.Agent, To: runview.Agent{Name: "idle", Namespace: ref.Namespace}}}
		run.HandOffs = []runview.HandOff{{FromTask: ref.UID, ToTask: "demo-child-1", ObservedAt: run.Tasks[1].StartedAt}}
		run.Events = []runview.Event{{TaskID: ref.UID, Seq: 1, At: ref.CreatedAt, Summary: "Task created"}, {TaskID: "demo-child-1", Seq: 1, At: run.Tasks[1].StartedAt, Summary: "Task succeeded"}}
	}
	return run
}
