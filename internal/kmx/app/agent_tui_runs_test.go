package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runview"
	runorka "github.com/kaimahi-agents/kaimahi/internal/kmx/runview/orka"
)

func TestConsoleRunsKeyboardNavigationAndSafeDetails(t *testing.T) {
	m := newAgentTUIModel(AgentTUIOptions{Demo: true})
	m.width, m.height = 100, 24
	m = tuiKey(m, 'R', "R")
	if m.runs == nil || m.runs.loading || len(m.runs.list.Roots) != 2 {
		t.Fatalf("runs did not open with demo roots: %+v", m.runs)
	}
	if plain := ansi.Strip(m.View().Content); !strings.Contains(plain, "RECENT RUNS") || !strings.Contains(plain, "DEMO") {
		t.Fatal("missing or unlabelled demo run list")
	}
	m = tuiKey(m, tea.KeyEnter, "")
	if m.runs.run == nil || m.runs.run.ID != "demo-root-1" {
		t.Fatalf("root not selected: %+v", m.runs)
	}
	plain := ansi.Strip(m.View().Content)
	for _, want := range []string{"Observed Tasks", "Declared helpers (permission only)", "helper", "Succeeded", "Elapsed from Task creation"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("missing %q in run:\n%s", want, plain)
		}
	}
	m = tuiKey(m, 'j', "j")
	if m.runs.taskSelection != 1 {
		t.Fatal("j did not select the child Task")
	}
	m = tuiKey(m, 'i', "i")
	if !strings.Contains(ansi.Strip(m.View().Content), "Task details") {
		t.Fatal("detail not visible")
	}
	m = tuiKey(m, tea.KeyEsc, "")
	if m.runs.detail {
		t.Fatal("escape did not leave task detail")
	}
	m = tuiKey(m, tea.KeyEsc, "")
	if m.runs.run != nil {
		t.Fatal("escape did not return to recent runs")
	}
	m = tuiKey(m, tea.KeyEsc, "")
	if m.runs != nil {
		t.Fatal("escape did not return to agents")
	}
}

func TestConsoleRunsTimeOrdersSiblingLanesAndSelectsEach(t *testing.T) {
	m := newAgentTUIModel(AgentTUIOptions{Demo: true})
	m.width, m.height = 160, 30
	m = tuiKey(m, 'R', "R")
	m = tuiKey(m, tea.KeyEnter, "")
	m.runs.run.Tasks = append(m.runs.run.Tasks, runview.Task{ID: "demo-child-early", Name: "early", ParentTask: m.runs.run.ID, Agent: runview.Agent{Name: "other", Namespace: "orka-system"}, Status: "Failed", StartedAt: m.runs.run.StartedAt.Add(30 * time.Second)})
	m.runs.run.Tasks = append(m.runs.run.Tasks, runview.Task{ID: "demo-grandchild", Name: "grandchild", ParentTask: "demo-child-1", Agent: runview.Agent{Name: "nested", Namespace: "orka-system"}, Status: "Running", StartedAt: m.runs.run.StartedAt.Add(2 * time.Minute)})
	m.runs.run.Tasks = consoleRunTaskOrder(m.runs.run.Tasks)
	if m.runs.run.Tasks[1].Name != "early" || m.runs.run.Tasks[2].Name != "review-check" {
		t.Fatalf("sibling order: %+v", m.runs.run.Tasks)
	}
	rows, _ := m.runOverviewLines()
	nested := false
	for _, line := range rows {
		if strings.Contains(line, "grandchild") {
			nested = strings.HasPrefix(line, "      ")
		}
	}
	if !nested {
		t.Fatalf("grandchild lane lost its indentation: %v", rows)
	}
	plain := ansi.Strip(m.View().Content)
	for _, line := range strings.Split(plain, "\n") {
		if strings.Contains(line, "early") && strings.Contains(line, "review-check") {
			return
		}
	}
	t.Fatalf("wide view did not place parallel siblings in adjacent lanes:\n%s", plain)
}

func TestConsoleRunsDenialAndEmptyListAreDifferent(t *testing.T) {
	m := newAgentTUIModel(AgentTUIOptions{Demo: true})
	m.opt.Demo = false
	m.loadRuns = func(context.Context, agentTUIEnvironment, agentTUIAgent) (consoleRunList, error) {
		return consoleRunList{}, runorka.ErrDenied
	}
	opened, cmd := m.openRuns()
	m = opened.(agentTUIModel)
	next, _ := m.Update(cmd())
	m = next.(agentTUIModel)
	if text := ansi.Strip(m.View().Content); !strings.Contains(text, "permission denied") || strings.Contains(text, "No recent root Tasks") {
		t.Fatalf("denial misrepresented:\n%s", text)
	}
	m.loadRuns = func(context.Context, agentTUIEnvironment, agentTUIAgent) (consoleRunList, error) {
		return consoleRunList{}, nil
	}
	opened, cmd = m.reloadRuns()
	m = opened.(agentTUIModel)
	next, _ = m.Update(cmd())
	m = next.(agentTUIModel)
	if text := ansi.Strip(m.View().Content); !strings.Contains(text, "No recent root Tasks") {
		t.Fatalf("empty list not clear:\n%s", text)
	}
}

func TestConsoleRunsRejectStaleResultsAndCancelOnClose(t *testing.T) {
	m := newAgentTUIModel(AgentTUIOptions{Demo: true})
	m.opt.Demo = false
	m.loadRuns = func(context.Context, agentTUIEnvironment, agentTUIAgent) (consoleRunList, error) {
		return consoleRunList{}, nil
	}
	m = tuiKey(m, 'R', "R")
	old := m.runs
	m = tuiKey(m, 'r', "r")
	if m.runs == old {
		t.Fatal("reload retained stale identity")
	}
	next, _ := m.Update(consoleRunsListed{pane: old, list: consoleRunList{Roots: []consoleRunRef{{Name: "wrong", UID: "old"}}}})
	m = next.(agentTUIModel)
	if len(m.runs.list.Roots) != 0 || !m.runs.loading {
		t.Fatal("stale list was drawn")
	}
	next, _ = m.Update(consoleRunsListed{pane: m.runs, list: consoleRunList{Roots: []consoleRunRef{{Name: "root", UID: "correct"}}}})
	m = next.(agentTUIModel)
	m.loadRun = func(context.Context, agentTUIEnvironment, consoleRunRef) (runview.Run, error) {
		return runview.Run{ID: "correct"}, nil
	}
	m = tuiKey(m, tea.KeyEnter, "")
	running := m.runs
	if !running.runLoading {
		t.Fatal("run did not start loading")
	}
	m = tuiKey(m, 'q', "q")
	if m.runs != nil {
		t.Fatal("q did not close run mode")
	}
	select {
	case <-running.ctx.Done():
	default:
		t.Fatal("closing did not cancel read")
	}
	next, _ = m.Update(consoleRunsRead{pane: running, run: runview.Run{ID: "stale"}})
	if next.(agentTUIModel).runs != nil {
		t.Fatal("late run reopened pane")
	}
}

func TestConsoleRunsLateReadCannotReopenAfterEscape(t *testing.T) {
	m := newAgentTUIModel(AgentTUIOptions{Demo: true})
	m.opt.Demo = false
	m.loadRuns = func(context.Context, agentTUIEnvironment, agentTUIAgent) (consoleRunList, error) {
		return consoleRunList{}, nil
	}
	m.loadRun = func(context.Context, agentTUIEnvironment, consoleRunRef) (runview.Run, error) {
		return runview.Run{}, nil
	}
	m = tuiKey(m, 'R', "R")
	next, _ := m.Update(consoleRunsListed{pane: m.runs, list: consoleRunList{Roots: []consoleRunRef{{Name: "root", Namespace: "orka-system", UID: "uid-root"}}, Count: 1}})
	m = next.(agentTUIModel)
	m = tuiKey(m, tea.KeyEnter, "")
	pending := m.runs
	m = tuiKey(m, tea.KeyEsc, "")
	if m.runs.run != nil || m.runs.runLoading {
		t.Fatal("escape did not return to list")
	}
	next, _ = m.Update(consoleRunsRead{pane: pending, run: runview.Run{ID: "uid-root", RootTask: "uid-root"}})
	m = next.(agentTUIModel)
	if m.runs == nil || m.runs.run != nil {
		t.Fatal("late result reopened escaped run")
	}
}

func TestConsoleRunsLiveTitleUsesRootNameAndUIDRemainsVisible(t *testing.T) {
	m := newAgentTUIModel(AgentTUIOptions{Demo: true})
	m.opt.Demo = false
	m.width, m.height = 90, 25
	m.loadRuns = func(context.Context, agentTUIEnvironment, agentTUIAgent) (consoleRunList, error) {
		return consoleRunList{}, nil
	}
	m.loadRun = func(context.Context, agentTUIEnvironment, consoleRunRef) (runview.Run, error) {
		return runview.Run{}, nil
	}
	m = tuiKey(m, 'R', "R")
	next, _ := m.Update(consoleRunsListed{pane: m.runs, list: consoleRunList{Roots: []consoleRunRef{{Name: "invoice-check", Namespace: "orka-system", UID: "uid-root"}}, Count: 1}})
	m = next.(agentTUIModel)
	m = tuiKey(m, tea.KeyEnter, "")
	next, _ = m.Update(consoleRunsRead{pane: m.runs, run: runview.Run{ID: "uid-root", RootTask: "uid-root", Tasks: []runview.Task{{ID: "uid-root", Name: "invoice-check"}}}})
	m = next.(agentTUIModel)
	plain := ansi.Strip(m.View().Content)
	if !strings.Contains(plain, "KMX · RUN · invoice-check") || !strings.Contains(plain, "Root Task UID: uid-root") {
		t.Fatalf("root name/UID conflated:\n%s", plain)
	}
}

func TestConsoleRunsDemoSnapshots(t *testing.T) {
	for _, tc := range []struct {
		name          string
		width, height int
		open, detail  bool
		want          string
	}{
		{name: "recent list", width: 64, height: 18, want: `KMX · DEMO · RECENT RUNS · kind-local-demo · orka-system/assist…
Recent root Tasks for this agent
Showing 2 of 2 recent roots
› 2026-10-01 12:00:00Z · Succeeded · invoice-check · UID demo-r…
2026-10-01 11:00:00Z · Running · invoice-review · UID demo-root…
↑/↓ select · <enter> open · r reload · <esc> back · q agents`},
		{name: "wide activity", width: 100, height: 26, open: true, want: `KMX · DEMO · RUN · invoice-check · Succeeded
Root Task UID: demo-root-1 │ Task details · invoice-check
Status: Succeeded · Task created: 2026-10-01 12:00:00Z │ UID: demo-root-1
Elapsed from Task creation: 3m0s │ Agent: orka-system/assistant
Finished: 2026-10-01 12:03:00Z │ Status: Succeeded
Freshness: available │ Summary: Task activity (content reda…
Discovery: available │ Task created: 2026-10-01 12:00:00Z
│ Finished: 2026-10-01 12:03:00Z
Observed Tasks (root first; children are verified hand-offs) │ Revision at execution: historical bi…
› 2026-10-01 12:00:00Z · Succeeded · orka-system/assistant … │
2026-10-01 12:01:00Z · Succeeded · orka-system/helper · rev… │ Recent safe events
│ 2026-10-01 12:00:00Z · #1 · Task cre…
Declared helpers (permission only) │
Not observed hand-offs │
permission only: orka-system/assistant → orka-system/helper │
permission only: orka-system/assistant → orka-system/idle │
↑/↓ select Task · i focus details · r refresh · <esc> recent runs · q agents`},
		{name: "narrow task detail", width: 40, height: 12, open: true, detail: true, want: `KMX · DEMO · RUN · invoice-check · Succ…
Task details · invoice-check
UID: demo-root-1
Agent: orka-system/assistant
Status: Succeeded
Summary: Task activity (content redacte…
Task created: 2026-10-01 12:00:00Z
Finished: 2026-10-01 12:03:00Z
Revision at execution: historical bindi…
Recent safe events
↑/↓ scroll · <esc> tasks · q agents`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newAgentTUIModel(AgentTUIOptions{Demo: true})
			m.width, m.height = tc.width, tc.height
			m = tuiKey(m, 'R', "R")
			if tc.open {
				m = tuiKey(m, tea.KeyEnter, "")
			}
			if tc.detail {
				m = tuiKey(m, 'i', "i")
			}
			var lines []string
			for _, line := range strings.Split(ansi.Strip(m.View().Content), "\n") {
				if clean := strings.Join(strings.Fields(line), " "); clean != "" {
					lines = append(lines, clean)
				}
			}
			if got := strings.Join(lines, "\n"); got != tc.want {
				t.Fatalf("snapshot changed:\n%s", got)
			}
		})
	}
}

func TestConsoleRunsTerminalSizesAndNoSensitiveText(t *testing.T) {
	for _, size := range [][2]int{{40, 12}, {64, 18}, {100, 30}, {160, 45}} {
		m := newAgentTUIModel(AgentTUIOptions{Demo: true})
		m.width, m.height = size[0], size[1]
		m = tuiKey(m, 'R', "R")
		m = tuiKey(m, tea.KeyEnter, "")
		if m.runs.run == nil {
			t.Fatal("missing demo run")
		}
		m.runs.run.Tasks[0].Name = "root\x1b]52;c;secret\a\n"
		m.runs.run.Tasks[0].Summary = "Task activity (content redacted)"
		for _, detail := range []bool{false, true} {
			m.runs.detail = detail
			content := m.View().Content
			if strings.Contains(content, "\x1b]52") || strings.Contains(content, "PRIVATE PROMPT") {
				t.Fatalf("untrusted data escaped: %q", content)
			}
			for i, line := range strings.Split(content, "\n") {
				if width := ansi.StringWidth(line); width > size[0] {
					t.Fatalf("%dx%d detail=%v row %d width=%d", size[0], size[1], detail, i, width)
				}
			}
			if rows := len(strings.Split(content, "\n")); rows > size[1] {
				t.Fatalf("%dx%d detail=%v rows=%d", size[0], size[1], detail, rows)
			}
		}
	}
}

func TestConsoleRunsDetailsShowApprovedFailureReason(t *testing.T) {
	m := newAgentTUIModel(AgentTUIOptions{Demo: true})
	m.width, m.height = 80, 28
	m = tuiKey(m, 'R', "R")
	m = tuiKey(m, tea.KeyEnter, "")
	m.runs.run.Tasks[0].Status = "Failed"
	m.runs.run.Tasks[0].FailureReason = "DeadlineExceeded"
	m.runs.run.Tasks[0].FailureMissing = nil
	m = tuiKey(m, 'i', "i")
	if text := ansi.Strip(m.View().Content); !strings.Contains(text, "Failure reason: DeadlineExceeded") {
		t.Fatalf("approved failure reason hidden:\n%s", text)
	}
	m.runs.run.Tasks[0].Status = "Succeeded"
	if text := ansi.Strip(m.View().Content); strings.Contains(text, "Failure reason: DeadlineExceeded") {
		t.Fatalf("successful Task acquired a failure reason:\n%s", text)
	}
}

func TestConsoleRunsExplainsClusterIdentityAccessWithoutStderr(t *testing.T) {
	err := errors.New("cannot establish destination cluster identity (kube-system UID): kubectl request failed: access forbidden")
	if got := consoleRunError(err); got != "kube-system Namespace identity unavailable (get access required)" {
		t.Fatalf("read error=%q", got)
	}
}

func TestConsoleRunsPartialListLabelsScannedSubset(t *testing.T) {
	m := newAgentTUIModel(AgentTUIOptions{Demo: true})
	m.width, m.height = 90, 24
	m = tuiKey(m, 'R', "R")
	m.runs.list.Missing = "scan limit reached"
	text := ansi.Strip(m.View().Content)
	if !strings.Contains(text, "List incomplete: scan limit reached") || !strings.Contains(text, "Showing 2 of 2 scanned roots") || strings.Contains(text, "Showing 2 of 2 recent roots") {
		t.Fatalf("partial list claimed global recency:\n%s", text)
	}
}

func TestConsoleRunsDetailDistinguishesMissingAndStale(t *testing.T) {
	m := newAgentTUIModel(AgentTUIOptions{Demo: true})
	m.width, m.height = 90, 28
	m = tuiKey(m, 'R', "R")
	m = tuiKey(m, tea.KeyEnter, "")
	m.runs.run.Status = "Running"
	m.runs.run.FreshnessMissing = &runview.Missing{Reason: "connection lost", Source: "Orka events"}
	m.runs.run.DiscoveryMissing = &runview.Missing{Reason: "permission denied", Source: "Kubernetes Task list"}
	m.runs.run.Tasks[0].EventsMissing = &runview.Missing{Reason: "history gap", Source: "Orka events"}
	m.runs.run.StartedAt = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	text := ansi.Strip(m.View().Content)
	for _, want := range []string{"Running", "connection lost", "permission denied", "history gap"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, "Failed") {
		t.Fatal("stale run invented a failure")
	}
}
