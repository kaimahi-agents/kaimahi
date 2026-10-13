package app

import (
	"context"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

func TestAgentTUICreationRetainsDiscoveredServerWithoutReload(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	dir := t.TempDir()
	fakeTool(t, dir, "kubectl", `case "$*" in
 *'config view'*) printf '%s' '{"contexts":[{"name":"remote","context":{"cluster":"r"}}],"clusters":[{"name":"r","cluster":{"server":"https://reviewed.example.com"}}]}' ;;
 *) exit 1 ;;
esac`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	a := &App{Cfg: &config.Config{}, Run: &run.Runner{}}
	envs, err := a.agentTUIEnvironments(t.Context())
	if err != nil || len(envs) != 1 {
		t.Fatalf("discovery: %v %+v", err, envs)
	}
	// No executable remains available when opening the read-only creation pane.
	t.Setenv("PATH", t.TempDir())
	m := newAgentTUIModel(AgentTUIOptions{Demo: true})
	m.columns[1].Env = envs[0]
	m.focus = 1
	m = tuiKey(m, 'n', "n")
	view := ansi.Strip(m.creationView())
	if !strings.Contains(view, "reviewed.example.com") || !strings.Contains(view, "remote") {
		t.Fatal(view)
	}
}

func TestAgentTUICreationSourceFirstReviewNoDeploy(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	opt := nativeEvaluationOptions(t)
	m := newAgentTUIModel(AgentTUIOptions{Demo: true, Namespace: "agents"})
	m.focus = 1
	m = tuiKey(m, 'n', "n")
	m.opt.Demo = false
	m.creation.server = "reviewed.example.com"
	m.startCreate = func(_ agentTUIEnvironment, _ string, root string, previous *suiteWorkspaceReview) (<-chan agentTUICreateResult, context.CancelFunc) {
		result := make(chan agentTUICreateResult, 1)
		var r *suiteWorkspaceReview
		var err error
		if previous == nil {
			r, err = (&App{}).reviewSuiteWorkspace(t.Context(), root)
		} else {
			r = previous
			err = r.checkCurrent(t.Context())
		}
		result <- agentTUICreateResult{review: r, err: err, pane: m.creation}
		return result, func() {}
	}
	m.creation.input.SetValue(opt.Workspace)
	_, cmd := m.updateCreatePane(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil || !m.creation.running {
		t.Fatal("source selection did not start review")
	}
	// Execute the read-only callback directly; Bubble Tea batches its delivery.
	ch, _ := m.startCreate(m.creation.env, m.creation.server, opt.Workspace, nil)
	next, _ := m.Update(<-ch)
	m = next.(agentTUIModel)
	if m.creation.review == nil || m.creation.finished || m.action != nil {
		t.Fatal("review not retained")
	}
	view := ansi.Strip(m.creationView())
	for _, want := range []string{"WORKSPACE", "packaged-model", "Instructions", "public", "Tasks"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q: %s", want, view)
		}
	}
	for _, size := range [][2]int{{64, 18}, {80, 24}, {120, 40}} {
		m.width, m.height = size[0], size[1]
		v := m.View().Content
		if lipgloss.Width(v) > m.width || lipgloss.Height(v) > m.height {
			t.Fatalf("overflow %v", size)
		}
	}
	// Enter cannot skip the end of a long page, then visits EVERY case.
	for m.creation.page <= len(m.creation.review.inventory) {
		m.creation.scroll = len(m.creation.reviewLines(76))
		m = tuiKey(m, tea.KeyEnter, "")
	}
	if m.creation.page != 3 || m.creation.finished {
		t.Fatal("did not reach separate final acknowledgement")
	}
	m = tuiKey(m, tea.KeyEnter, "")
	if m.creation.stage != "provider" || m.creation.running || m.creation.finished || m.action != nil {
		t.Fatal("source review did not lead to separate destination selection")
	}
	view = ansi.Strip(m.creationView())
	if !strings.Contains(view, "Provider name") || strings.Contains(view, "Agent created") {
		t.Fatal(view)
	}
	m = tuiKey(m, tea.KeyEsc, "")
	if m.creation != nil {
		t.Fatal("did not close")
	}
}

func TestAgentTUICreationReviewCancelAndStaleResult(t *testing.T) {
	m := newAgentTUIModel(AgentTUIOptions{Demo: true})
	m = tuiKey(m, 'n', "n")
	old := m.creation
	m = tuiKey(m, 'j', "j")
	if m.creation.input.Value() != "j" || m.columns[0].Selection != 0 {
		t.Fatal("selection input reached inventory")
	}
	m = tuiKey(m, tea.KeyEsc, "")
	if m.creation != nil {
		t.Fatal("selection did not cancel")
	}
	m = tuiKey(m, 'n', "n")
	next, _ := m.Update(agentTUICreateResult{err: context.Canceled, pane: old})
	m = next.(agentTUIModel)
	if m.creation.finished {
		t.Fatal("stale result affected new selection")
	}
	cancelled := false
	m.creation.running = true
	m.creation.cancel = func() { cancelled = true }
	m = tuiKey(m, tea.KeyEsc, "")
	if !cancelled || !m.creation.running || !m.creation.cancelling {
		t.Fatal("cancel did not wait")
	}
	next, _ = m.Update(agentTUICreateResult{err: context.Canceled, pane: m.creation})
	m = next.(agentTUIModel)
	if !m.creation.finished || m.creation.running {
		t.Fatal("cancel result not finished")
	}
	if strings.Contains(ansi.Strip(m.creationView()), "Agent created") {
		t.Fatal("cancel claimed creation")
	}
}

func TestAgentTUICreationRejectsDriftOnConfirmation(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	opt := nativeEvaluationOptions(t)
	r, err := (&App{}).reviewSuiteWorkspace(t.Context(), opt.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	m := newAgentTUIModel(AgentTUIOptions{Demo: true})
	m = tuiKey(m, 'n', "n")
	m.opt.Demo = false
	m.creation.review = r
	m.creation.page = len(r.inventory) + 1
	writeFile(t, opt.Workspace+"/eval/second.yaml", `id: second
input: drift
expectContains: [drift]
`)
	// Real consumer refuses before reading cluster configuration or evaluating.
	out := (&App{}).consoleWorkspaceLift(t.Context(), consoleWorkspaceLiftRequest{
		env:       agentTUIEnvironment{Name: "kind-test", Local: true},
		namespace: OrkaNamespace, provider: "inference", review: r,
	})
	if out.err == nil || !strings.Contains(out.err.Error(), "workspace changed since preparation") || out.prepared != nil || out.verified {
		t.Fatalf("drift accepted: %+v", out)
	}
}
