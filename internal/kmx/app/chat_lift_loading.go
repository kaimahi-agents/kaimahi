package app

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type liftLoadingResult struct {
	data []byte
	err  error
}
type liftLoadingModel struct {
	label                string
	header               *liftHeader
	statusHeader         func(int) string
	width, height, frame int
	started              time.Time
	result               <-chan liftLoadingResult
	cancel               context.CancelFunc
	done, cancelled      bool
}

func (m liftLoadingModel) Init() tea.Cmd {
	return tea.Batch(quickstartTick(), func() tea.Msg { return <-m.result })
}
func (m liftLoadingModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case quickstartTickMsg:
		m.frame++
		return m, quickstartTick()
	case liftLoadingResult:
		m.done = true
		return m, tea.Quit
	case createWizardCancelMsg:
		m.cancelled = true
		m.cancel()
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" || msg.Code == tea.KeyEsc {
			m.cancelled = true
			m.cancel()
		}
	}
	return m, nil
}
func (m liftLoadingModel) View() tea.View {
	w, h := m.width, m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}
	head := ""
	if m.header != nil {
		head = m.header.view(w) + "\n"
		h = max(1, h-3)
	}
	if m.statusHeader != nil {
		head = m.statusHeader(w) + "\n"
		h = max(1, h-lipgloss.Height(head)+1)
	}
	inner := max(1, min(66, w-6))
	label := strings.Join(strings.Fields(safeTerminal(m.label)), " ")
	var labelRows []string
	for _, segment := range strings.Split(label, " · ") {
		labelRows = append(labelRows, strings.Split(ansi.Hardwrap(tuiDetailLine(segment), inner, true), "\n")...)
	}
	maxRows := max(1, h-7)
	if len(labelRows) > maxRows {
		labelRows = labelRows[:maxRows]
		labelRows[maxRows-1] = ansi.Truncate(labelRows[maxRows-1], max(1, inner-1), "") + "…"
	}
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	status := frames[m.frame%len(frames)] + " Loading · " + formatElapsed(time.Since(m.started))
	if m.cancelled {
		status = "Cancelling…"
	}
	rows := []string{"", lipgloss.NewStyle().Foreground(lipgloss.Cyan).Bold(true).Render(ansi.Truncate(status, inner, "…"))}
	rows = append(rows, labelRows...)
	rows = append(rows, ansi.Truncate("esc / ctrl+c cancel", inner, ""), "")
	border := lipgloss.NewStyle().Foreground(lipgloss.Blue)
	box := border.Render("╭" + strings.Repeat("─", inner+2) + "╮")
	for _, row := range rows {
		box += "\n" + border.Render("│") + " " + row + strings.Repeat(" ", max(0, inner-lipgloss.Width(row))) + " " + border.Render("│")
	}
	box += "\n" + border.Render("╰"+strings.Repeat("─", inner+2)+"╯")
	return tea.NewView(head + lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, box))
}

func runLiftLoading(parent context.Context, in io.Reader, out io.Writer, label string, header *liftHeader, fetch func(context.Context) ([]byte, error)) ([]byte, error) {
	return runStatusLoading(parent, in, out, label, header, nil, fetch)
}

func runStatusLoading(parent context.Context, in io.Reader, out io.Writer, label string, header *liftHeader, statusHeader func(int) string, fetch func(context.Context) ([]byte, error)) ([]byte, error) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	result := make(chan liftLoadingResult, 1)
	joined := make(chan struct{})
	var outcome liftLoadingResult
	w, h := prepareLiftPane(out)
	m := liftLoadingModel{label: label, header: header, width: w, height: h, started: time.Now(), result: result, cancel: cancel}
	m.statusHeader = statusHeader
	go func() { defer close(joined); outcome.data, outcome.err = fetch(ctx); result <- outcome }()
	filter := func(model tea.Model, msg tea.Msg) tea.Msg {
		switch msg.(type) {
		case tea.InterruptMsg:
			return createWizardCancelMsg{}
		case tea.QuitMsg:
			if !model.(liftLoadingModel).done {
				return createWizardCancelMsg{}
			}
		}
		return msg
	}
	final, err := tea.NewProgram(m, tea.WithInput(in), tea.WithOutput(out), tea.WithContext(parent), tea.WithFilter(filter)).Run()
	cancel()
	<-joined
	if isInteractiveTerminal(out) {
		fmt.Fprint(out, "\x1b[H\x1b[2J")
	}
	if parent.Err() != nil {
		return nil, parent.Err()
	}
	if completed, ok := final.(liftLoadingModel); ok && completed.cancelled {
		return nil, context.Canceled
	}
	if err != nil {
		return nil, err
	}
	return outcome.data, outcome.err
}
