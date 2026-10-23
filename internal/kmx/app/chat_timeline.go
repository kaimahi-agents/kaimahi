package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/cliui"
)

type chatTimelineEvent struct {
	kind, agent, label, text string
	start                    bool
}

type chatTimelineJob struct {
	connect bool
	message string
	verbose bool
}

type chatTimelineDone struct {
	connect bool
	fields  []cliui.Field
	err     error
}

type chatTimelineModel struct {
	editor                              textarea.Model
	history                             viewport.Model
	entries                             []chatTimelineEvent
	fields                              []cliui.Field
	agent, kubeContext, last, initial   string
	sent                                []string
	recall                              int
	draft                               string
	width, height, frame                int
	completion                          int
	completionSelected                  bool
	busy, connecting, verbose, quitting bool
	command                             string
	commands                            []slashCommand
	started                             time.Time
	err                                 error
	jobs                                chan<- chatTimelineJob
	events                              <-chan tea.Msg
	ctx                                 context.Context
	cancel                              context.CancelFunc
}

func newChatTimeline(agent, kubeContext, initial string, verbose bool) chatTimelineModel {
	input := textarea.New()
	input.Prompt = "YOU > "
	input.Placeholder = "Message or /command"
	input.ShowLineNumbers = false
	input.CharLimit = 64 << 10
	input.MaxHeight = 0
	input.SetHeight(3)
	input.SetWidth(76)
	input.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("shift+enter", "alt+enter"))
	input.KeyMap.DeleteWordBackward = key.NewBinding(key.WithKeys("ctrl+w", "alt+backspace"))
	input.SetVirtualCursor(true)
	input.Focus()
	history := viewport.New(viewport.WithWidth(80), viewport.WithHeight(13))
	history.FillHeight = true
	return chatTimelineModel{editor: input, history: history, agent: agent, kubeContext: kubeContext,
		initial: initial, verbose: verbose, width: 80, height: 24, recall: 0, commands: commonChatCommands()}
}

func (m chatTimelineModel) waitEvent() tea.Cmd {
	return func() tea.Msg {
		select {
		case msg := <-m.events:
			return msg
		case <-m.ctx.Done():
			return createWizardCancelMsg{}
		}
	}
}

func (m chatTimelineModel) queue(job chatTimelineJob) tea.Cmd {
	return func() tea.Msg {
		select {
		case m.jobs <- job:
		case <-m.ctx.Done():
		}
		return nil
	}
}

func (m chatTimelineModel) Init() tea.Cmd {
	return tea.Batch(m.waitEvent(), quickstartTick(), m.editor.Focus(), m.queue(chatTimelineJob{connect: true, verbose: m.verbose}))
}

func (m *chatTimelineModel) resize() {
	// Header (4), input border/editor (up to 5), and footer (1) are pinned.
	inputHeight := min(3, max(1, m.height-9))
	m.editor.SetWidth(max(1, m.width-4))
	m.editor.SetHeight(inputHeight)
	m.history.SetWidth(max(1, m.width))
	m.history.SetHeight(max(1, m.height-inputHeight-7))
	m.rebuildHistory(false)
}

func (m *chatTimelineModel) rebuildHistory(follow bool) {
	wasBottom := m.history.AtBottom()
	var text strings.Builder
	width := max(1, m.width)
	for _, entry := range m.entries {
		switch entry.kind {
		case "user":
			if width >= 16 {
				ui := cliui.WithCapabilities(cliui.Capabilities{Rich: true, Color: true, Width: width})
				text.WriteString(strings.Join(ui.UserMessage(safeTerminal(entry.text), width-1), "\n") + "\n\n")
			} else {
				text.WriteString(ansi.Hardwrap("YOU > "+safeTerminal(entry.text), width, true) + "\n\n")
			}
		case "agent":
			text.WriteString(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Green).Render(ansi.Truncate("AGENT ("+safeTerminal(entry.agent)+")", width, "…")) + "\n")
		case "assistant":
			text.WriteString(timelineIndented(entry.text, 2, width) + "\n")
		case "agent-operation":
			// Indentation is applied after wrapping, including narrow windows, so
			// every tool-call line remains visibly nested under its agent.
			text.WriteString(timelineIndented("["+entry.label+"]", 4, width) + "\n" + timelineIndented(entry.text, 6, width) + "\n\n")
		case "timing":
			text.WriteString(lipgloss.NewStyle().Foreground(lipgloss.BrightBlack).Render(timelineIndented(entry.text, 2, width)) + "\n\n")
		default:
			text.WriteString(ansi.Hardwrap(safeTerminal(entry.label)+"\n"+safeTerminal(entry.text), width, true) + "\n\n")
		}
	}
	m.history.SetContent(strings.TrimSuffix(text.String(), "\n"))
	if follow || wasBottom {
		m.history.GotoBottom()
	}
}

func timelineIndented(text string, indent, width int) string {
	indent = min(indent, max(0, width-1))
	prefix := strings.Repeat(" ", indent)
	return prefix + strings.ReplaceAll(ansi.Hardwrap(safeTerminal(text), max(1, width-indent), true), "\n", "\n"+prefix)
}

func (m *chatTimelineModel) appendEvent(event chatTimelineEvent) {
	if event.kind == "assistant" && len(m.entries) > 0 && !event.start {
		last := &m.entries[len(m.entries)-1]
		if last.kind == "assistant" && last.agent == event.agent {
			last.text += event.text
			m.rebuildHistory(false)
			return
		}
	}
	m.entries = append(m.entries, event)
	m.rebuildHistory(false)
}

func (m *chatTimelineModel) recallMessage(up bool) {
	if len(m.sent) == 0 {
		return
	}
	if up {
		if m.recall == len(m.sent) {
			m.draft = m.editor.Value()
		}
		m.recall = max(0, m.recall-1)
	} else {
		m.recall = min(len(m.sent), m.recall+1)
	}
	if m.recall == len(m.sent) {
		m.editor.SetValue(m.draft)
	} else {
		m.editor.SetValue(m.sent[m.recall])
	}
	m.editor.MoveToEnd()
}

func (m *chatTimelineModel) send(message string) tea.Cmd {
	m.last = message
	m.sent = append(m.sent, message)
	m.recall = len(m.sent)
	m.draft = ""
	m.editor.Reset()
	m.history.GotoBottom()
	m.appendEvent(chatTimelineEvent{kind: "user", text: message})
	m.busy = true
	m.started = time.Now()
	return m.queue(chatTimelineJob{message: message, verbose: m.verbose})
}

func (m chatTimelineModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = max(8, msg.Width), max(10, msg.Height)
		m.resize()
		return m, nil
	case quickstartTickMsg:
		m.frame++
		return m, quickstartTick()
	case createWizardCancelMsg:
		m.quitting = true
		m.cancel()
		return m, tea.Quit
	case chatTimelineEvent:
		if msg.kind == "status" {
			m.agent, m.kubeContext = msg.agent, msg.text
		} else {
			m.appendEvent(msg)
		}
		return m, m.waitEvent()
	case chatTimelineDone:
		m.busy, m.connecting = false, false
		if msg.err != nil {
			if m.ctx.Err() != nil {
				m.quitting = true
				return m, tea.Quit
			}
			m.appendEvent(chatTimelineEvent{kind: "error", label: "ERROR", text: msg.err.Error()})
			if msg.connect {
				m.err = msg.err
				m.quitting = true
				return m, tea.Quit
			}
		} else if msg.connect {
			m.fields = msg.fields
			if m.initial != "" {
				initial := m.initial
				m.initial = ""
				return m, tea.Batch(m.waitEvent(), m.send(initial))
			}
		}
		return m, m.waitEvent()
	case tea.MouseWheelMsg:
		var cmd tea.Cmd
		m.history, cmd = m.history.Update(msg)
		return m, cmd
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			m.quitting = true
			m.cancel()
			return m, tea.Quit
		}
		switch msg.String() {
		case "pgup", "shift+up":
			m.history.PageUp()
			return m, nil
		case "pgdown", "shift+down":
			m.history.PageDown()
			return m, nil
		case "ctrl+home":
			m.history.GotoTop()
			return m, nil
		case "ctrl+end":
			m.history.GotoBottom()
			return m, nil
		}
		matches := slashMatchesFrom(m.commands, m.editor.Value())
		if m.completion >= len(matches) {
			m.completion = 0
		}
		if len(matches) > 0 {
			switch msg.Code {
			case tea.KeyUp:
				m.completion = (m.completion + len(matches) - 1) % len(matches)
				m.completionSelected = true
				return m, nil
			case tea.KeyDown:
				m.completion = (m.completion + 1) % len(matches)
				m.completionSelected = true
				return m, nil
			case tea.KeyTab:
				m.editor.SetValue(matches[m.completion].name)
				m.editor.MoveToEnd()
				m.completion = 0
				m.completionSelected = false
				return m, nil
			}
		}
		switch msg.String() {
		case "ctrl+a":
			m.editor.MoveToBegin()
			return m, nil
		case "ctrl+e":
			m.editor.MoveToEnd()
			return m, nil
		case "up":
			m.recallMessage(true)
			return m, nil
		case "down":
			m.recallMessage(false)
			return m, nil
		}
		if msg.Code == tea.KeyEnter && msg.Mod == 0 {
			if m.busy {
				return m, nil
			}
			if len(matches) > 0 && (m.completionSelected || m.editor.Value() != matches[m.completion].name) {
				m.editor.SetValue(matches[m.completion].name)
				m.editor.MoveToEnd()
				m.completion = 0
				m.completionSelected = false
				return m, nil
			}
			message := strings.TrimSpace(m.editor.Value())
			if message == "" {
				return m, nil
			}
			m.editor.Reset()
			m.completion = 0
			switch message {
			case "/exit", "/quit":
				m.quitting = true
				return m, tea.Quit
			case "/help":
				var commands []string
				for _, command := range m.commands {
					commands = append(commands, command.usage)
				}
				m.appendEvent(chatTimelineEvent{kind: "help", label: "CHAT HELP", text: strings.Join(commands, "\n")})
				return m, nil
			case "/verbose-on", "/verbose-off":
				m.verbose = message == "/verbose-on"
				m.appendEvent(chatTimelineEvent{kind: "status-message", label: "CHAT", text: fmt.Sprintf("Verbose: %t", m.verbose)})
				return m, nil
			case "/retry":
				if m.last == "" {
					m.appendEvent(chatTimelineEvent{kind: "status-message", label: "CHAT", text: "Retry: no previous message"})
					return m, nil
				}
				return m, m.send(m.last)
			default:
				if containsChatCommand(m.commands, message) {
					m.command = message
					m.quitting = true
					return m, tea.Quit
				}
				if strings.HasPrefix(message, "/") {
					m.appendEvent(chatTimelineEvent{kind: "error", label: "CHAT", text: "Unknown command. Use /help."})
					return m, nil
				}
			}
			return m, m.send(message)
		}
	}
	before := m.editor.Value()
	var cmd tea.Cmd
	m.editor, cmd = m.editor.Update(msg)
	if before != m.editor.Value() {
		m.completion = 0
		m.completionSelected = false
	}
	return m, cmd
}

func (m chatTimelineModel) View() tea.View {
	fields := map[string]string{}
	for _, f := range m.fields {
		fields[f.Label] = f.Value
	}
	location := fields["Location"]
	if location == "" {
		location = m.kubeContext
	}
	inference := fields["Inference"]
	if inference == "" {
		inference = fields["Runtime"]
	}
	width := m.width
	line := func(s string) string { return ansi.Truncate(s, width, "…") }
	header := line("KMX / "+tuiField("agent", m.agent)+" · "+tuiField("location", location)) + "\n" + line(tuiField("inference", " "+inference)) + "\n" + line(tuiField("tools", " "+fields["Tools"])) + "\n" + strings.Repeat("─", width)
	if fields["Definition"] != "" {
		header = line("KMX / "+tuiField("agent", m.agent)+" · "+tuiField("location", location)) + "\n" +
			line(tuiField("status", fields["Status"])+" · "+tuiField("def", fields["Definition"])+" · "+tuiField("tests", fields["Tests"])) + "\n" +
			line(tuiField("inference", inference)+" · "+tuiField("tools", fields["Tools"])) + "\n" + strings.Repeat("─", width)
	}
	historyRows := strings.Split(m.history.View(), "\n")
	// Popup overlays only the history viewport. Header/editor never move.
	matches := slashMatchesFrom(m.commands, m.editor.Value())
	popup := slashPopup(matches, m.completion, max(1, width-1), m.history.Height())
	if len(popup) > 0 {
		start := max(0, len(historyRows)-len(popup))
		historyRows = append(historyRows[:start], popup...)
	}
	border := lipgloss.NewStyle().Foreground(lipgloss.Blue)
	inputRows := strings.Split(m.editor.View(), "\n")
	input := border.Render("▌" + strings.Repeat("─", max(0, width-2)) + "╮")
	for _, row := range inputRows {
		row = ansi.Truncate(row, max(1, width-4), "")
		input += "\n" + border.Render("▌") + " " + row + strings.Repeat(" ", max(0, width-4-lipgloss.Width(row))) + " " + border.Render("│")
	}
	input += "\n" + border.Render("▌"+strings.Repeat("─", max(0, width-2))+"╯")
	footer := "PgUp/PgDn scroll · ↑ recall · Ctrl-W word · Ctrl-A/E · Enter send"
	if m.busy {
		state := "Responding"
		if m.connecting {
			state = "Connecting"
		}
		footer = fmt.Sprintf("%s %s · %s · PgUp/PgDn scroll · Ctrl-C cancel", []string{"⠋", "⠙", "⠹", "⠸"}[m.frame%4], state, formatElapsed(time.Since(m.started)))
	}
	if !m.history.AtBottom() {
		footer = fmt.Sprintf("History %.0f%% · Ctrl-End latest · ", m.history.ScrollPercent()*100) + footer
	}
	view := tea.NewView(header + "\n" + strings.Join(historyRows, "\n") + "\n" + input + "\n" + line(footer))
	view.AltScreen = true
	view.MouseMode = tea.MouseModeCellMotion
	return view
}

func (a *App) runChatTimeline(backend interactiveChatBackend, initial string) error {
	parent, cancel := signal.NotifyContext(a.operationContext(), os.Interrupt)
	defer cancel()
	if closer, ok := backend.(interface{ Close() }); ok {
		defer closer.Close()
	}
	kubeContext := ""
	if a.Cfg != nil {
		kubeContext = a.Cfg.KubeContext
	}
	m := newChatTimeline(backend.Agent(), kubeContext, initial, a.chatVerbose)
	m.commands = chatBackendCommands(backend)
	for {
		ctx, stop := context.WithCancel(parent)
		events := make(chan tea.Msg, 64)
		jobs := make(chan chatTimelineJob, 1)
		joined := make(chan struct{})
		emit := func(msg tea.Msg) {
			select {
			case events <- msg:
			case <-ctx.Done():
			}
		}
		go func() {
			defer close(joined)
			for {
				select {
				case <-ctx.Done():
					return
				case job := <-jobs:
					r := newChatRenderer(io.Discard)
					r.verbose = job.verbose
					r.timeline = func(event chatTimelineEvent) { emit(event) }
					if job.connect {
						fields, err := backend.Connect(ctx, r)
						emit(chatTimelineDone{connect: true, fields: fields, err: err})
					} else {
						err := sendInteractiveChatMessage(ctx, backend, job.message, r)
						emit(chatTimelineDone{err: err})
					}
				}
			}
		}()
		m.ctx, m.cancel, m.events, m.jobs = ctx, stop, events, jobs
		m.quitting, m.command = false, ""
		m.busy, m.connecting = true, true
		m.started = time.Now()
		filter := func(model tea.Model, msg tea.Msg) tea.Msg {
			switch msg.(type) {
			case tea.InterruptMsg:
				return createWizardCancelMsg{}
			case tea.QuitMsg:
				if !model.(chatTimelineModel).quitting {
					return createWizardCancelMsg{}
				}
			}
			return msg
		}
		result, err := tea.NewProgram(m, tea.WithInput(a.Stdin), tea.WithOutput(a.Out), tea.WithContext(parent), tea.WithFilter(filter)).Run()
		stop()
		<-joined
		if err != nil {
			return err
		}
		m = result.(chatTimelineModel)
		if m.err != nil {
			return m.err
		}
		if m.command == "" {
			return nil
		}
		controls, ok := backend.(configurableChatBackend)
		if !ok {
			m.appendEvent(chatTimelineEvent{kind: "error", label: "CHAT", text: "Agent configuration is unavailable for this backend."})
			continue
		}
		r := newChatRenderer(a.Out)
		r.verbose = m.verbose
		r.enterFullScreen()
		reset, err := controls.Configure(parent, m.command, r)
		r.leaveFullScreen()
		if err != nil {
			if parent.Err() != nil || err == context.Canceled {
				return nil
			}
			m.appendEvent(chatTimelineEvent{kind: "error", label: "CHAT", text: err.Error()})
		}
		if reset {
			m.entries = nil
			m.sent = nil
			m.last = ""
			m.recall = 0
			m.draft = ""
			m.editor.Reset()
			m.rebuildHistory(true)
		}
		m.agent = backend.Agent()
		m.commands = chatBackendCommands(backend)
	}
}
