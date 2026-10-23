package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/cliui"
)

type mdPoll struct{}
type mdDisk struct {
	agent      *MarkdownAgent
	err        error
	reschedule bool
}
type mdDone struct {
	generation int
	answer     string
	result     *MarkdownTestResult
	snapshot   *MarkdownAgent
	err        error
}
type mdProgress struct {
	generation int
	row        MarkdownCaseResult
}
type mdEditorDone struct{ err error }
type mdConversationReset struct{ err error }
type mdUpdatePlanned struct {
	plan MarkdownUpdatePlan
	err  error
}
type mdUpdateApplied struct {
	session *MarkdownSession
	agent   *MarkdownAgent
	err     error
}
type mdToolSuggested struct {
	proposal MarkdownToolProposal
	err      error
}
type mdRuntimeProgress struct {
	generation int
	text       string
}

// markdownTUI reuses the existing timeline's editor, viewport, rendering and
// navigation. Its local jobs never enter the cluster-console lifecycle.
type markdownTUI struct {
	chatTimelineModel
	session                  *MarkdownSession
	loaded, disk, lastTested *MarkdownAgent
	diskErr                  error
	result                   *MarkdownTestResult
	ctx                      context.Context
	cancel                   context.CancelFunc
	stopJob                  context.CancelFunc
	generation               int
	events                   chan tea.Msg
	autoTest                 bool
	pendingAuto              bool
	workers                  *sync.WaitGroup
	runningPair              string
	lastSave                 time.Time
	testPane                 bool
	fullWidth                int
	instanceRunning          bool
	updatePlan               *MarkdownUpdatePlan
}

func mdPollCmd() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg { return mdPoll{} })
}
func (m markdownTUI) wait() tea.Cmd {
	return func() tea.Msg {
		select {
		case event := <-m.events:
			return event
		case <-m.ctx.Done():
			return tea.QuitMsg{}
		}
	}
}
func (m markdownTUI) Init() tea.Cmd {
	return tea.Batch(m.editor.Focus(), mdPollCmd(), m.wait(), quickstartTick())
}
func (m *markdownTUI) note(label, text string) {
	m.appendEvent(chatTimelineEvent{kind: "operation", label: label, text: text})
}
func (m *markdownTUI) refreshHeader() {
	status := "INVALID"
	if m.diskErr == nil && m.disk != nil {
		status = MarkdownStatus(m.session.effective(m.disk), m.result)
		if m.result != nil && m.lastTested == nil {
			status = "HISTORICAL " + status
		}
	}
	if m.busy {
		status = "RUNNING"
		if m.diskErr != nil || m.disk == nil || m.disk.PairRevision != m.runningPair {
			status += " · disk changed"
		}
	}
	a := m.loaded
	testStatus := status
	status = "stopped"
	if m.instanceRunning {
		status = "running"
	}
	if _, ok := m.session.executor.(*attachedAgentExecutor); ok {
		status = "attached · running"
	}
	if m.busy {
		status += " · busy"
	}
	if m.diskErr != nil {
		status += " · source invalid"
	} else if m.disk != nil && m.disk.Revision != a.Revision {
		status += " · definition changed"
	}
	tools := "none"
	if len(a.Tools) > 0 {
		tools = strings.Join(a.Tools, ", ") + " (mock tests only)"
	}
	if a.Grant != nil {
		if a.Grant.Adapter == cliToolAdapter {
			tools = strings.Join(a.Grant.Tools, ", ") + " · approved local executables"
		} else {
			tools = strings.Join(a.Grant.Tools, ", ") + " · " + a.Grant.Repository + " · read-only"
		}
	}
	inference := "Copilot · " + mdModelLabel(m.session.effective(a).Model)
	if m.session.conversationDisplay != "" {
		inference += " · session " + m.session.conversationDisplay
	}
	m.fields = []cliui.Field{{Label: "Location", Value: "local · " + a.Path}, {Label: "Inference", Value: inference}, {Label: "Status", Value: status}, {Label: "Definition", Value: mdShort(a.Revision)}, {Label: "Tests", Value: testStatus}, {Label: "Tools", Value: tools}}
}
func mdShort(hash string) string {
	if hash == "" {
		return "none"
	}
	return strings.TrimPrefix(hash, "sha256:")[:min(12, len(strings.TrimPrefix(hash, "sha256:")))]
}
func (m *markdownTUI) startTest(id string) tea.Cmd {
	if m.busy {
		return nil
	}
	if m.diskErr != nil {
		m.note("INVALID", m.diskErr.Error())
		return nil
	}
	snapshot := m.disk
	m.generation++
	g := m.generation
	m.busy = true
	m.runningPair = snapshot.PairRevision
	m.started = time.Now()
	ctx, cancel := context.WithCancel(m.ctx)
	m.stopJob = cancel
	m.note("TEST", snapshot.PairRevision+" · "+id)
	// A separate SDK session prevents tests from altering the current chat.
	provider, model := m.session.Provider, m.session.Model
	events := m.events
	parent := m.ctx
	m.workers.Add(1)
	return func() tea.Msg {
		defer m.workers.Done()
		s := NewMarkdownSession(io.Discard, io.Discard)
		s.Provider, s.Model = provider, model
		defer s.Close()
		result, err := s.Evaluate(ctx, snapshot, id, func(row MarkdownCaseResult) {
			select {
			case events <- mdProgress{g, row}:
			case <-parent.Done():
			}
		})
		return mdDone{generation: g, result: &result, snapshot: snapshot, err: err}
	}
}
func (m *markdownTUI) sendLocal(input string) tea.Cmd {
	if m.busy {
		return nil
	}
	if m.diskErr != nil {
		m.note("INVALID", m.diskErr.Error())
		return nil
	}
	if m.disk.Revision != m.loaded.Revision {
		m.note("CHANGED", "Definition changed. /reload starts a fresh conversation before sending.")
		return nil
	}
	m.loaded = m.disk // test-only edits do not reset the conversation
	m.sent = append(m.sent, input)
	m.recall = len(m.sent)
	m.draft = ""
	m.appendEvent(chatTimelineEvent{kind: "user", text: input})
	m.generation++
	g := m.generation
	m.busy = true
	m.runningPair = aPair(m.loaded)
	m.started = time.Now()
	ctx, cancel := context.WithCancel(m.ctx)
	m.stopJob = cancel
	a := m.loaded
	s := m.session
	m.workers.Add(1)
	return func() tea.Msg {
		defer m.workers.Done()
		if executor, ok := s.executor.(*markdownCopilot); ok {
			executor.progress = func(text string) {
				select {
				case m.events <- mdRuntimeProgress{g, text}:
				case <-ctx.Done():
				}
			}
			defer func() { executor.progress = nil }()
		}
		answer, err := s.AnswerAgent(ctx, a, input)
		return mdDone{generation: g, answer: answer.Text, err: err}
	}
}
func (m *markdownTUI) localCommand(input string) tea.Cmd {
	command, arg, _ := strings.Cut(input, " ")
	arg = strings.TrimSpace(arg)
	switch command {
	case "/quit", "/exit", "/back":
		if m.stopJob != nil {
			m.stopJob()
		}
		return tea.Quit
	case "/help":
		m.note("COMMANDS", "/show /test [id] /tests /reload /update [yes|cancel] /reset /edit /edit-tests /suggest-tools [OWNER/REPO] /review-tools PROPOSAL_ID /diff /auto-test on|off /settings /cancel /quit")
	case "/suggest-tools":
		if m.busy {
			m.note("BUSY", "Finish the current operation first.")
			break
		}
		if err := validateGithubRepo(arg); arg != "" && err != nil {
			m.note("SUGGEST TOOLS", err.Error())
			break
		}
		m.busy = true
		m.started = time.Now()
		ctx, cancel := context.WithCancel(m.ctx)
		m.stopJob = cancel
		m.workers.Add(1)
		path, model := m.loaded.Path, m.session.Model
		m.note("SUGGEST TOOLS", "Sending definition and capability catalog to inference. No tools execute and no access is granted.")
		return func() tea.Msg {
			defer m.workers.Done()
			p, err := SuggestMarkdownTools(ctx, path, arg, model)
			return mdToolSuggested{p, err}
		}
	case "/review-tools":
		if m.busy {
			m.note("BUSY", "Finish the current operation first.")
			break
		}
		p, err := ReadMarkdownToolProposal(arg)
		if err != nil {
			m.note("REVIEW TOOLS", err.Error())
			break
		}
		if p.SchemaVersion == 2 {
			var text strings.Builder
			for _, choice := range p.CLIChoices {
				_ = RenderCLIChoice(&text, choice)
			}
			m.note("REVIEW TOOLS", text.String()+"\nAdd selected declarations: kmx tools review "+p.ID+" --apply\nThen tools approve FILE --apply before /update.")
			break
		}
		g, err := PrepareToolGrant(p, "", nil)
		if err != nil {
			m.note("REVIEW TOOLS", err.Error())
			break
		}
		var text strings.Builder
		_ = RenderToolReview(&text, p, g)
		m.note("REVIEW TOOLS", text.String()+"\nTo choose a subset, adjust scope and approve, use: kmx tools review "+p.ID+" --apply\nThen /update to activate the reviewed binding.")
	case "/cancel":
		if m.stopJob != nil {
			m.stopJob()
		}
	case "/show":
		m.note("DEFINITION", m.loaded.Instructions)
	case "/tests":
		m.testPane = !m.testPane
		if m.result == nil {
			m.note("TESTS", "No test result yet.")
			break
		}
		for _, r := range m.result.Cases {
			m.note(strings.ToUpper(r.Verdict)+" "+r.ID, mdCaseDetails(m.lastTested, r))
		}
	case "/test":
		return m.startTest(arg)
	case "/reload":
		if _, ok := m.session.executor.(*attachedAgentExecutor); ok && m.session.ownedManaged == "" {
			m.note("RUNNING VERSION", "This chat uses the worker's cached version. Use kmx stop then kmx run FILE to update it.")
			break
		}
		if m.busy {
			m.note("BUSY", "Cancel or finish the current operation before reload.")
			break
		}
		if m.diskErr != nil {
			m.note("INVALID", m.diskErr.Error())
			break
		}
		m.loaded = m.disk
		m.session.Reset()
		m.instanceRunning = false
		m.note("RELOADED", m.loaded.PairRevision+" · new conversation; earlier messages belong to the old revision")
	case "/update":
		if m.busy {
			m.note("BUSY", "Finish or cancel the current job before updating.")
			break
		}
		if arg == "cancel" {
			m.updatePlan = nil
			m.note("UPDATE", "Cancelled; running version unchanged.")
			break
		}
		if arg == "yes" {
			if m.updatePlan == nil {
				m.note("UPDATE", "Use /update first to review the replacement.")
				break
			}
			plan := *m.updatePlan
			m.updatePlan = nil
			m.busy = true
			m.workers.Add(1)
			ctx := m.ctx
			return func() tea.Msg {
				defer m.workers.Done()
				worker := NewMarkdownSession(io.Discard, io.Discard)
				worker.Provider = "copilot"
				r, err := worker.ApplyAgentUpdate(ctx, plan)
				if err != nil {
					return mdUpdateApplied{err: err}
				}
				s, a, err := NewSelectedAgentSession(ctx, r.ID, "", false, "", io.Discard, io.Discard)
				return mdUpdateApplied{session: s, agent: a, err: err}
			}
		}
		if arg != "" {
			m.note("UPDATE", "Use /update, /update yes or /update cancel.")
			break
		}
		m.busy = true
		m.workers.Add(1)
		path, tests := m.loaded.Path, m.loaded.TestsPath
		ctx := m.ctx
		return func() tea.Msg {
			defer m.workers.Done()
			a, err := LoadMarkdownAgent(path, tests)
			if err != nil {
				return mdUpdatePlanned{err: err}
			}
			worker := NewMarkdownSession(io.Discard, io.Discard)
			worker.Provider = "copilot"
			p, err := worker.PlanAgentUpdate(ctx, a)
			return mdUpdatePlanned{plan: p, err: err}
		}
	case "/reset":
		if !m.busy {
			m.busy = true
			s, ctx := m.session, m.ctx
			m.workers.Add(1)
			return func() tea.Msg { defer m.workers.Done(); return mdConversationReset{s.ResetConversation(ctx)} }
		}
	case "/edit", "/edit-tests":
		path := m.loaded.Path
		if command == "/edit-tests" {
			path = m.loaded.TestsPath
		}
		if path == "" {
			m.note("EDIT", "Supply --tests for a definition outside .github/agents.")
			break
		}
		argv, err := markdownEditor()
		if err != nil {
			m.note("EDITOR", err.Error())
			break
		}
		return tea.ExecProcess(exec.Command(argv[0], append(argv[1:], path)...), func(err error) tea.Msg { return mdEditorDone{err} })
	case "/diff":
		if m.lastTested == nil {
			m.note("DIFF", "Previous source bytes are unavailable; test once in this session or use Git history.")
			break
		}
		if m.diskErr != nil {
			m.note("INVALID", m.diskErr.Error())
			break
		}
		m.note("DEFINITION DIFF", mdLineDiff(string(m.lastTested.Source), string(m.disk.Source)))
		m.note("TEST DIFF", mdLineDiff(string(m.lastTested.TestSource), string(m.disk.TestSource)))
	case "/auto-test":
		if arg != "on" && arg != "off" {
			m.note("AUTO TEST", "Use /auto-test on or /auto-test off (default off).")
			break
		}
		m.autoTest = arg == "on"
		m.note("AUTO TEST", arg+" · full tests after saved edits")
	case "/settings":
		if arg == "" {
			m.note("SETTINGS", "Use /settings copilot|none|reset. This changes the saved default for later runs.")
			break
		}
		choice := ""
		switch arg {
		case "copilot":
			choice = "accepted"
		case "none":
			choice = "declined"
		case "reset":
		default:
			m.note("SETTINGS", "Use copilot, none or reset.")
			return nil
		}
		if err := SetMarkdownProvider(choice); err != nil {
			m.note("SETTINGS", err.Error())
		} else {
			m.note("SETTINGS", "Saved default: "+arg)
		}
	default:
		m.note("COMMAND", "Unknown command. /help lists available actions.")
	}
	return nil
}
func mdCaseDetails(a *MarkdownAgent, r MarkdownCaseResult) string {
	var b strings.Builder
	if a != nil {
		fmt.Fprintf(&b, "%s:%d\n", a.TestsPath, a.Lines[r.ID])
		for _, c := range a.Examples {
			if c.ID == r.ID {
				for _, check := range c.Assertions {
					fmt.Fprintf(&b, "Expected %s: %s %q %q\n", check.ID, check.Type, check.Value, check.Pattern)
				}
			}
		}
	}
	for _, check := range r.Checks {
		fmt.Fprintf(&b, "%s: %s (%s)\n", check.ID, check.Verdict, check.Reason)
	}
	fmt.Fprintf(&b, "%s\nAnswer: %s", r.Reason, r.Answer)
	return b.String()
}
func (m markdownTUI) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case mdToolSuggested:
		m.busy = false
		if m.stopJob != nil {
			m.stopJob()
			m.stopJob = nil
		}
		if v.err != nil {
			m.note("SUGGEST TOOLS", v.err.Error())
		} else {
			var text strings.Builder
			for _, c := range v.proposal.Choices {
				fmt.Fprintf(&text, "%s — %s\n", c.Template, c.Reason)
			}
			for _, c := range v.proposal.CLIChoices {
				_ = RenderCLIChoice(&text, c)
			}
			for _, gap := range v.proposal.Gaps {
				fmt.Fprintln(&text, "Unsupported:", gap)
			}
			fmt.Fprintf(&text, "\n/review-tools %s", v.proposal.ID)
			m.note("TOOL PROPOSAL", text.String())
		}
		m.refreshHeader()
		return m, nil
	case mdUpdatePlanned:
		m.busy = false
		if v.err != nil {
			m.note("UPDATE", v.err.Error())
		} else if v.plan.Noop {
			m.note("UPDATE", "Already running this version; no sessions changed.")
		} else {
			m.updatePlan = &v.plan
			var text strings.Builder
			_ = ConfirmAgentUpdate(m.ctx, &text, nil, v.plan, true)
			m.note("UPDATE PLAN", text.String()+"\n/update yes applies; /update cancel leaves it unchanged.")
		}
		m.refreshHeader()
		return m, nil
	case mdUpdateApplied:
		m.busy = false
		if v.err != nil {
			m.note("UPDATE", v.err.Error())
			return m, nil
		}
		// The manager has already replaced the old generation. Never let cleanup
		// of the former foreground owner stop this new instance by the same ID.
		m.session.ownedManaged = ""
		m.session.executor.Close()
		m.session.executor = v.session.executor
		m.session.Model = v.session.Model
		m.session.conversationDisplay = m.session.conversationLabel()
		m.loaded, m.disk = v.agent, v.agent
		m.diskErr = nil
		m.instanceRunning = true
		m.result = nil
		m.lastTested = nil
		m.note("UPDATED", "New version "+mdShort(v.agent.Revision)+" · fresh session. Earlier messages belong to the previous version.")
		m.refreshHeader()
		return m, nil
	case mdConversationReset:
		m.busy = false
		m.session.conversationDisplay = m.session.conversationLabel()
		if v.err != nil {
			m.note("RESET", v.err.Error())
		} else {
			m.note("NEW SESSION", "Fresh conversation; agent version/generation unchanged. Earlier messages belong to the previous session.")
		}
		m.refreshHeader()
		return m, nil
	case mdRuntimeProgress:
		if v.generation == m.generation {
			m.note("RUNTIME", v.text)
		}
		return m, m.wait()
	case tea.WindowSizeMsg:
		m.fullWidth = v.Width
		if v.Width >= 100 {
			m.width = v.Width * 3 / 5
		} else {
			m.width = v.Width
		}
		m.height = v.Height
		m.resize()
		return m, nil
	case mdPoll:
		if _, ok := m.session.executor.(*attachedAgentExecutor); ok && m.session.ownedManaged == "" {
			return m, mdPollCmd()
		}
		path, tests := m.loaded.Path, m.loaded.TestsPath
		return m, func() tea.Msg {
			a, err := LoadMarkdownAgent(path, tests)
			return mdDisk{agent: a, err: err, reschedule: true}
		}
	case mdDisk:
		changed := v.err == nil && (m.disk == nil || v.agent.PairRevision != m.disk.PairRevision)
		if v.err != nil && (m.diskErr == nil || v.err.Error() != m.diskErr.Error()) {
			m.note("INVALID", v.err.Error())
		}
		m.disk, m.diskErr = v.agent, v.err
		if changed {
			m.note("SAVED", "Source changed · "+v.agent.PairRevision)
			m.pendingAuto = m.autoTest
			m.lastSave = time.Now()
		}
		m.refreshHeader()
		var poll tea.Cmd
		if v.reschedule {
			poll = mdPollCmd()
		}
		if m.pendingAuto && !m.busy && m.diskErr == nil && time.Since(m.lastSave) >= 750*time.Millisecond {
			m.pendingAuto = false
			return m, tea.Batch(poll, m.startTest(""))
		}
		return m, poll
	case mdProgress:
		if v.generation == m.generation {
			m.note("TEST "+v.row.ID, strings.ToUpper(v.row.Verdict)+" · "+v.row.Reason)
		}
		return m, m.wait()
	case mdDone:
		if v.generation != m.generation {
			return m, nil
		}
		m.busy = false
		m.session.conversationDisplay = m.session.conversationLabel()
		if v.result == nil {
			m.instanceRunning = v.err == nil
		}
		if m.stopJob != nil {
			m.stopJob()
			m.stopJob = nil
		}
		if v.result != nil && v.result.ID != "" {
			m.result = v.result
			m.lastTested = v.snapshot
			m.note("TEST RESULT", strings.ToUpper(v.result.Status)+" · "+v.result.Mode+" · /tests shows expected/actual")
		}
		if v.err != nil {
			m.note("ERROR", v.err.Error())
		} else if v.result == nil {
			m.appendEvent(chatTimelineEvent{kind: "agent", agent: m.loaded.Name})
			m.appendEvent(chatTimelineEvent{kind: "assistant", agent: m.loaded.Name, text: v.answer})
		}
		m.refreshHeader()
		return m, nil
	case mdEditorDone:
		if v.err != nil {
			m.note("EDITOR", v.err.Error())
		}
		return m, func() tea.Msg {
			a, e := LoadMarkdownAgent(m.loaded.Path, m.loaded.TestsPath)
			return mdDisk{agent: a, err: e}
		}
	case tea.KeyPressMsg:
		if v.String() == "ctrl+c" {
			if m.busy {
				if m.stopJob != nil {
					m.stopJob()
					m.note("CANCEL", "Cancelling current operation…")
				} else {
					m.note("BUSY", "Finishing the current lifecycle operation; its result will be reported.")
				}
				return m, nil
			}
			return m, tea.Quit
		}
		if v.Code == tea.KeyEnter && v.Mod == 0 {
			input := strings.TrimSpace(m.editor.Value())
			m.editor.Reset()
			if input == "" {
				return m, nil
			}
			if strings.HasPrefix(input, "/") {
				cmd := m.localCommand(input)
				m.refreshHeader()
				return m, cmd
			}
			cmd := m.sendLocal(input)
			m.refreshHeader()
			return m, cmd
		}
	}
	// Delegate text editing, resizing and scrolling to the established chat view.
	updated, cmd := m.chatTimelineModel.Update(msg)
	m.chatTimelineModel = updated.(chatTimelineModel)
	return m, cmd
}
func (m markdownTUI) View() tea.View {
	if m.fullWidth < 100 && !m.testPane {
		return m.chatTimelineModel.View()
	}
	width := m.fullWidth - m.width - 2
	if m.fullWidth < 100 {
		width = m.width
	}
	var text strings.Builder
	text.WriteString("TESTS · /tests toggles details\n\n")
	if m.result == nil {
		text.WriteString("No completed attempt. Use /test.")
	} else {
		fmt.Fprintf(&text, "%s · %s\n%s\n\n", strings.ToUpper(m.result.Status), m.result.Mode, m.result.Completed.Local().Format(time.RFC822))
		for _, row := range m.result.Cases {
			fmt.Fprintf(&text, "%s %s\n", strings.ToUpper(row.Verdict), row.ID)
			if m.testPane {
				text.WriteString(mdCaseDetails(m.lastTested, row) + "\n\n")
			}
		}
	}
	panel := viewport.New(viewport.WithWidth(max(10, width)), viewport.WithHeight(max(5, m.height-2)))
	panel.SetContent(safeTerminal(text.String()))
	if m.fullWidth < 100 {
		v := tea.NewView(panel.View() + "\n/tests returns to chat")
		v.AltScreen = true
		return v
	}
	base := m.chatTimelineModel.View()
	base.SetContent(lipgloss.JoinHorizontal(lipgloss.Top, base.Content, " │ ", panel.View()))
	return base
}
func (s *MarkdownSession) ChatTUI(ctx context.Context, a *MarkdownAgent, in *os.File) error {
	if !s.InitialTest {
		if err := s.StartAgent(ctx, a); err != nil {
			return err
		}
	}
	defer s.Reset()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	timeline := newChatTimeline(a.Name, "local", "", false)
	timeline.commands = []slashCommand{}
	for _, name := range []string{"/show", "/test", "/tests", "/edit", "/edit-tests", "/reload", "/update", "/reset", "/diff", "/auto-test", "/settings", "/suggest-tools", "/review-tools", "/cancel", "/quit"} {
		timeline.commands = append(timeline.commands, slashCommand{name: name, usage: name})
	}
	timeline.ctx = ctx
	timeline.cancel = cancel
	workers := &sync.WaitGroup{}
	m := markdownTUI{chatTimelineModel: timeline, session: s, loaded: a, disk: a, ctx: ctx, cancel: cancel, events: make(chan tea.Msg, 64), workers: workers}
	m.instanceRunning = s.instance != nil
	s.conversationDisplay = s.conversationLabel()
	rows, err := LoadMarkdownHistory(a)
	if err != nil {
		m.note("HISTORY", err.Error())
	} else if len(rows) > 0 {
		m.result = &rows[len(rows)-1]
	}
	m.refreshHeader()
	m.note("LOCAL AGENT", "/edit instructions · /edit-tests expectations · /test · /tests failures · /reload · /help")
	if s.conversationDisplay != "" {
		m.note("SESSION", s.conversationDisplay+" · reconnect retains model context; previous transcript is not displayed")
	}
	if s.InitialTest {
		m.pendingAuto = true
		m.lastSave = time.Now().Add(-time.Second)
	}
	_, err = tea.NewProgram(m, tea.WithInput(in), tea.WithOutput(s.Out), tea.WithContext(ctx)).Run()
	cancel()
	workers.Wait()
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func aPair(a *MarkdownAgent) string {
	if a == nil {
		return ""
	}
	return a.PairRevision
}
func markdownEditor() ([]string, error) {
	p, err := ReadMarkdownPreferences()
	if err != nil {
		return nil, err
	}
	if len(p.LocalAgents.Editor) > 0 {
		return p.LocalAgents.Editor, nil
	}
	value := os.Getenv("VISUAL")
	if value == "" {
		value = os.Getenv("EDITOR")
	}
	if value == "" {
		value = "vi"
	}
	return mdSplitArgs(value)
}

// Editor command parsing supports quotes/backslash without shell evaluation.
func mdSplitArgs(value string) ([]string, error) {
	var args []string
	var b strings.Builder
	var quote rune
	escape := false
	started := false
	for _, r := range value {
		if escape {
			b.WriteRune(r)
			escape = false
			started = true
			continue
		}
		if r == '\\' && quote != '\'' {
			escape = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				b.WriteRune(r)
			}
			started = true
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			started = true
			continue
		}
		if r == ' ' || r == '\t' {
			if started {
				args = append(args, b.String())
				b.Reset()
				started = false
			}
			continue
		}
		b.WriteRune(r)
		started = true
	}
	if quote != 0 || escape {
		return nil, errors.New("invalid quoted VISUAL/EDITOR command")
	}
	if started {
		args = append(args, b.String())
	}
	if len(args) == 0 || args[0] == "" {
		return nil, errors.New("set VISUAL or EDITOR")
	}
	return args, nil
}
func mdLineDiff(before, after string) string {
	if before == after {
		return "Unchanged"
	}
	a, b := strings.Split(before, "\n"), strings.Split(after, "\n")
	var out strings.Builder
	for i := 0; i < max(len(a), len(b)); i++ {
		if i < len(a) && i < len(b) && a[i] == b[i] {
			continue
		}
		if i < len(a) {
			fmt.Fprintf(&out, "-%d %s\n", i+1, a[i])
		}
		if i < len(b) {
			fmt.Fprintf(&out, "+%d %s\n", i+1, b[i])
		}
		if out.Len() > 32<<10 {
			out.WriteString("… diff truncated")
			break
		}
	}
	return out.String()
}
