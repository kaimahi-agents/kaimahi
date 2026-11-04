package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func consoleMDTestModel(t *testing.T) agentTUIModel {
	t.Helper()
	t.Setenv("KMX_HOME", t.TempDir())
	m := newAgentTUIModel(AgentTUIOptions{})
	m.discovering = false
	m.columns[0].Env = agentTUIEnvironment{Name: "files:test", Directory: t.TempDir(), Local: true}
	next, _ := m.createAgent()
	m = next.(agentTUIModel)
	p := m.mdCreation
	m.startMDJob = func(_ *consoleMDCreate, work func(context.Context) consoleMDResult) tea.Cmd {
		return func() tea.Msg { result := work(t.Context()); result.pane = p; return result }
	}
	return m
}

func TestConsoleMDCreationAndSelectedToolsNeverExecute(t *testing.T) {
	m := consoleMDTestModel(t)
	p := m.mdCreation
	p.input.SetValue("project-reader")
	m = tuiKey(m, tea.KeyEnter, "")
	p.input.SetValue("Read the local Go version and explain it.")
	m = tuiKey(m, tea.KeyEnter, "")
	m = tuiKey(m, tea.KeyEnter, "") // host default model
	if p.stage != "confirm" {
		t.Fatal(p.stage)
	}
	if entries, _ := os.ReadDir(p.root); len(entries) != 0 {
		t.Fatal("form wrote files before confirmation")
	}
	next, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(agentTUIModel)
	next, _ = m.Update(cmd())
	m = next.(agentTUIModel)
	if p.stage != "offer" || m.selected() == nil || m.selected().Ready != "created" {
		t.Fatal("new definition not selected as created")
	}
	a, err := ReadMarkdownAgent(p.path)
	if err != nil || len(a.Examples) != 0 || a.Instructions != "Read the local Go version and explain it." {
		t.Fatalf("custom draft=%+v err=%v", a, err)
	}
	before, _ := os.ReadFile(p.path)
	// Exercise the production UI suggestion request with a scripted inference result.
	_, proposal := cliSuggestionFixture(t)
	proposal.Source, proposal.AgentDigest = a.Path, a.Revision
	proposal.ToolsDigest = a.ToolsRevision
	m.startMDJob = func(_ *consoleMDCreate, _ func(context.Context) consoleMDResult) tea.Cmd {
		return func() tea.Msg { return consoleMDResult{pane: p, proposal: proposal} }
	}
	next, cmd = m.Update(tea.KeyPressMsg{Code: 'y'})
	m = next.(agentTUIModel)
	next, _ = m.Update(cmd())
	m = next.(agentTUIModel)
	if p.stage != "choices" {
		t.Fatal(p.stage)
	}
	m = tuiKey(m, tea.KeyEnd, "")
	m = tuiKey(m, 'y', "y")
	m = tuiKey(m, 'n', "n")
	after, _ := os.ReadFile(p.path)
	if !bytes.Equal(before, after) {
		t.Fatal("selecting tools saved before confirmation")
	}
	m.startMDJob = func(_ *consoleMDCreate, work func(context.Context) consoleMDResult) tea.Cmd {
		return func() tea.Msg { result := work(t.Context()); result.pane = p; return result }
	}
	next, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(agentTUIModel)
	next, _ = m.Update(cmd())
	m = next.(agentTUIModel)
	if p.err != nil || p.stage != "approve-offer" {
		t.Fatalf("save failed: %v", p.err)
	}
	a, err = ReadMarkdownAgent(p.path)
	if err != nil || len(a.CLITools) != 1 || a.CLITools[0].Name != "go_version" {
		t.Fatal("wrong accepted tools", err)
	}
	if g, _, err := readCurrentToolGrant(a.Path); err != nil || g != nil {
		t.Fatal("creation granted execution")
	}
	m = tuiKey(m, tea.KeyEnter, "") // postpone approval
	m = tuiKey(m, tea.KeyEnter, "") // close workflow
	next, _ = m.Update(agentTUIInventoryMsg{column: 0, generation: m.generation})
	m = next.(agentTUIModel)
	if m.selected() == nil || m.selected().DefinitionPath != a.Path || !m.selected().Tools[0].Disabled {
		t.Fatal("refresh lost unstarted definition or granted tools")
	}
}

func TestConsoleMDCancelResizeAndStaleResult(t *testing.T) {
	m := consoleMDTestModel(t)
	p := m.mdCreation
	for _, size := range [][2]int{{64, 18}, {80, 24}, {160, 40}} {
		next, _ := m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m = next.(agentTUIModel)
		if view := m.View().Content; lipgloss.Width(view) > size[0] || lipgloss.Height(view) > size[1] {
			t.Fatal("creation panel overflow")
		}
	}
	p.running = true
	called := false
	p.cancel = func() { called = true }
	m = tuiKey(m, tea.KeyEsc, "")
	if !called || !p.cancelling || m.mdCreation == nil {
		t.Fatal("did not wait for cancellation")
	}
	next, _ := m.Update(consoleMDResult{pane: &consoleMDCreate{}, err: context.Canceled})
	m = next.(agentTUIModel)
	if !p.running {
		t.Fatal("stale result changed pane")
	}
	next, _ = m.Update(consoleMDResult{pane: p, err: context.Canceled})
	m = next.(agentTUIModel)
	if p.running || p.stage != "done" {
		t.Fatal("cancel did not settle")
	}
	if entries, _ := os.ReadDir(p.root); len(entries) != 0 {
		t.Fatal("cancel created files")
	}
}

func TestMarkdownCustomDraftValidationAndCollision(t *testing.T) {
	root := t.TempDir()
	if err := CreateMarkdownDraft(root, "test", "", "default"); err == nil {
		t.Fatal("empty instructions accepted")
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Fatal("invalid draft wrote files")
	}
	if err := CreateMarkdownDraft(root, "test", "Explain Go tests", "gpt-test"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "test", "test.agent.md")
	before, _ := os.ReadFile(path)
	if err := CreateMarkdownDraft(root, "test", "overwrite", "gpt-test"); err == nil {
		t.Fatal("collision overwritten")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("source changed")
	}
}

type pathSuggestionExecutor struct {
	t      *testing.T
	output string
}

func (e *pathSuggestionExecutor) Start(context.Context) (string, error) { return "fixture", nil }
func (e *pathSuggestionExecutor) Reset()                                {}
func (e *pathSuggestionExecutor) Close()                                {}
func (e *pathSuggestionExecutor) Answer(_ context.Context, a *MarkdownAgent, input string, mocks []MarkdownMock) (markdownAnswer, error) {
	var request struct {
		PathAvailability map[string]string `json:"pathAvailability"`
	}
	if err := json.Unmarshal([]byte(input), &request); err != nil {
		e.t.Fatal(err)
	}
	if request.PathAvailability["go"] != "found on PATH (not executed)" || request.PathAvailability["gh"] != "not found on PATH" {
		e.t.Fatal("incorrect PATH observations")
	}
	if len(a.Tools) != 0 || a.Grant != nil || mocks != nil {
		e.t.Fatal("suggestion had executable tools")
	}
	return markdownAnswer{Text: e.output}, nil
}

func TestToolSuggestionPATHObservationDoesNotExecute(t *testing.T) {
	a, p := cliSuggestionFixture(t)
	bin := t.TempDir()
	marker := filepath.Join(t.TempDir(), "executed")
	t.Setenv("KMX_TEST_MARKER", marker)
	fakeTool(t, bin, "go", `printf executed > "$KMX_TEST_MARKER"`)
	t.Setenv("PATH", bin)
	raw, _ := json.Marshal(map[string]any{"tools": p.CLIChoices, "gaps": p.Gaps})
	proposal, err := suggestCLICommandTools(t.Context(), a.Path, "", &pathSuggestionExecutor{t, string(raw)})
	if err != nil {
		t.Fatal(err)
	}
	if proposal.CLIChoices[0].Availability != "found on PATH (not executed)" {
		t.Fatal("proposal missing host observation")
	}
	var view strings.Builder
	if err := RenderCLIChoice(&view, proposal.CLIChoices[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("PATH discovery or rendering ran candidate")
	}
}
