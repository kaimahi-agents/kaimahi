package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func approvalConsole(t *testing.T, path string) agentTUIModel {
	t.Helper()
	m := newAgentTUIModel(AgentTUIOptions{})
	m.discovering = false
	m.columns[0] = agentTUIColumn{Env: agentTUIEnvironment{Name: "files:fixture", Directory: filepath.Dir(path), Local: true}, Agents: []agentTUIAgent{{Name: "cluster-reader", Runtime: "md", DefinitionPath: path, Ready: "running", InstanceID: "existing-instance"}}}
	m.startMDJob = func(p *consoleMDCreate, work func(context.Context) consoleMDResult) tea.Cmd {
		ctx, cancel := context.WithCancel(t.Context())
		p.cancel = cancel
		return func() tea.Msg { msg := work(ctx); msg.pane = p; return msg }
	}
	m.actionsOpen = true
	next, cmd := m.updateActions(tea.KeyPressMsg{Code: 'a'})
	m = next.(agentTUIModel)
	if cmd == nil {
		t.Fatal("approval action did not prepare")
	}
	next, _ = m.Update(cmd())
	return next.(agentTUIModel)
}

func TestConsoleMDApprovalReviewsBeforeProbeAndPreservesInstance(t *testing.T) {
	path, bin := cliFixture(t)
	marker := filepath.Join(t.TempDir(), "probe")
	t.Setenv("KMX_TEST_PROBE", marker)
	fakeTool(t, bin, "fixture-cli", `printf ran > "$KMX_TEST_PROBE"; printf 'fixture 2.4\n'`)
	m := approvalConsole(t, path)
	p := m.mdCreation
	if p.err != nil || p.stage != "approval-review" {
		t.Fatalf("review: %s %v", p.stage, p.err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("preparation executed probe")
	}
	if g, _, err := readCurrentToolGrant(path); err != nil || g != nil {
		t.Fatal("preparation granted access")
	}
	for _, width := range []int{64, 100, 160} {
		m.width, m.height = width, 24
		view := m.View().Content
		if lipgloss.Width(view) > width || lipgloss.Height(view) > 24 {
			t.Fatal("review overflow")
		}
	}
	next, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(agentTUIModel)
	if cmd != nil || p.running {
		t.Fatal("Enter implicitly approved execution")
	}
	m = tuiKey(m, tea.KeyEnd, "")
	next, cmd = m.Update(tea.KeyPressMsg{Code: 'y'})
	m = next.(agentTUIModel)
	if cmd == nil || p.stage != "approving" {
		t.Fatal("explicit approval did not run")
	}
	next, _ = m.Update(cmd())
	m = next.(agentTUIModel)
	if p.err != nil || !p.approved || len(p.approval.ProbeResults) != 1 {
		t.Fatalf("approval: %+v", p)
	}
	if g, _, err := readCurrentToolGrant(path); err != nil || g == nil {
		t.Fatal("successful probe did not grant")
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "ran" {
		t.Fatal("probe did not execute", err)
	}
	if m.selected().InstanceID != "existing-instance" || m.selected().Ready != "running" || m.action != nil {
		t.Fatal("approval replaced or started an instance")
	}
	next, cmd = m.Update(tea.KeyPressMsg{Code: 't'})
	m = next.(agentTUIModel)
	if cmd == nil || m.action == nil || m.action.kind != "edit-tests" || m.action.agent.TestsPath != MarkdownTestsPath(path) {
		t.Fatal("next action did not hand off to test editor")
	}
}

func TestConsoleMDApprovalFailureDriftAndCancellationNeverGrant(t *testing.T) {
	for _, scenario := range []string{"failed probe", "source drift", "cancel", "decline"} {
		t.Run(scenario, func(t *testing.T) {
			path, bin := cliFixture(t)
			body := `printf 'fixture 2.4\n'`
			if scenario == "failed probe" {
				body = `printf 'fixture 1.0\n'`
			}
			fakeTool(t, bin, "fixture-cli", body)
			m := approvalConsole(t, path)
			p := m.mdCreation
			m = tuiKey(m, tea.KeyEnd, "")
			if scenario == "decline" {
				m = tuiKey(m, 'n', "n")
			} else {
				if scenario == "source drift" {
					if err := os.WriteFile(path, append([]byte(cliToolAgent), '\n'), 0600); err != nil {
						t.Fatal(err)
					}
				}
				next, cmd := m.Update(tea.KeyPressMsg{Code: 'y'})
				m = next.(agentTUIModel)
				if scenario == "cancel" {
					m = tuiKey(m, tea.KeyEsc, "")
				}
				next, _ = m.Update(cmd())
				m = next.(agentTUIModel)
				if p.err == nil {
					t.Fatal("failure not reported")
				}
			}
			if p.approved {
				t.Fatal("failed/declined operation approved")
			}
			if g, _, err := readCurrentToolGrant(path); err != nil || g != nil {
				t.Fatal("failure wrote grant")
			}
			if !strings.Contains(m.mdCreationView(), "approval not completed") {
				t.Fatal("failure display misleading")
			}
		})
	}
}

func TestConsoleMDApprovalDemoNeverStartsWorker(t *testing.T) {
	m := newAgentTUIModel(AgentTUIOptions{Demo: true})
	m.columns[0].Agents = []agentTUIAgent{{Name: "fixture", Runtime: "md", DefinitionPath: "absent.agent.md"}}
	m.startMDJob = func(*consoleMDCreate, func(context.Context) consoleMDResult) tea.Cmd {
		t.Fatal("demo started worker")
		return nil
	}
	m.actionsOpen = true
	next, cmd := m.updateActions(tea.KeyPressMsg{Code: 'a'})
	m = next.(agentTUIModel)
	if cmd != nil || m.mdCreation != nil || !strings.Contains(m.status, "DEMO") {
		t.Fatal("demo approval was operational")
	}
}

func TestConsoleCreatedDefinitionSurvivesAliasRefresh(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "project")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	if err := CreateMarkdownDraft(alias, "example", "Say hello.", ""); err != nil {
		t.Fatal(err)
	}
	a, err := ReadMarkdownAgent(filepath.Join(alias, "example"))
	if err != nil {
		t.Fatal(err)
	}
	m := newAgentTUIModel(AgentTUIOptions{})
	m.columns[0].Env = agentTUIEnvironment{Name: "files:alias", Directory: alias, Local: true}
	m.mdCreation = &consoleMDCreate{root: alias}
	m.retainCreatedDefinition(a)
	m.mdCreation = nil
	next, _ := m.Update(agentTUIInventoryMsg{column: 0, generation: m.generation})
	m = next.(agentTUIModel)
	if m.selected() == nil || m.selected().DefinitionPath != a.Path {
		t.Fatal("created definition disappeared through alias")
	}
}
