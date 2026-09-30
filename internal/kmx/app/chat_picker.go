package app

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type chatPickerItem struct {
	name, detail    string
	enabled, locked bool
}

type chatPicker struct {
	title, query                               string
	items                                      []chatPickerItem
	selection                                  int
	multiple, accepted, cancelled, interrupted bool
	width                                      int
	height                                     int
	vim, searching                             bool
	searchEnabled, searchDefault               bool
	action                                     string
	header                                     *liftHeader
	statusHeader                               func(int) string
}

func (m chatPicker) matches() []int {
	type match struct{ index, score int }
	var matches []match
	for i, item := range m.items {
		query := m.query
		if !m.searchEnabled {
			query = ""
		}
		if score, ok := pickerFuzzyScore(item.name+" "+item.detail, query); ok {
			matches = append(matches, match{i, score})
		}
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].score < matches[j].score })
	found := make([]int, 0, len(matches))
	for _, match := range matches {
		found = append(found, match.index)
	}
	return found
}

// Match query words as case-insensitive subsequences. Prefer contiguous matches
// and shorter gaps while retaining source order for ties and empty searches.
func pickerFuzzyScore(text, query string) (int, bool) {
	text = strings.ToLower(text)
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return 0, true
	}
	score := 0
	for _, word := range strings.Fields(query) {
		if index := strings.Index(text, word); index >= 0 {
			score += index
			continue
		}
		runes := []rune(text)
		pos, first, last := 0, -1, -1
		for _, r := range word {
			for pos < len(runes) && runes[pos] != r {
				pos++
			}
			if pos == len(runes) {
				return 0, false
			}
			if first < 0 {
				first = pos
			}
			last = pos
			pos++
		}
		score += 100 + first + last - first - len([]rune(word))
	}
	return score, true
}

func (m chatPicker) Init() tea.Cmd { return nil }
func (m chatPicker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		m.width = size.Width
		m.height = size.Height
		return m, nil
	}
	if _, ok := msg.(createWizardCancelMsg); ok {
		m.cancelled, m.interrupted = true, true
		return m, tea.Quit
	}
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	if key.String() == "ctrl+c" {
		m.cancelled, m.interrupted = true, true
		return m, tea.Quit
	}
	if m.searchEnabled && !m.searching && key.Code == '/' {
		m.searching = true
		return m, nil
	}
	if m.vim {
		if !m.searching {
			switch key.Code {
			case 'j':
				key.Code = tea.KeyDown
				key.Text = ""
			case 'k':
				key.Code = tea.KeyUp
				key.Text = ""
			case '/':
				m.searching = m.searchEnabled
				return m, nil
			case 'g':
				m.selection = 0
				return m, nil
			case 'G':
				m.selection = max(0, len(m.matches())-1)
				return m, nil
			}
		} else if key.Code == tea.KeyEsc {
			m.searching = false
			return m, nil
		}
	}
	found := m.matches()
	switch key.Code {
	case tea.KeyEsc:
		m.cancelled = true
		return m, tea.Quit
	case tea.KeyUp:
		if len(found) > 0 {
			m.selection = (m.selection + len(found) - 1) % len(found)
		}
	case tea.KeyDown, tea.KeyTab:
		if len(found) > 0 {
			m.selection = (m.selection + 1) % len(found)
		}
	case tea.KeyEnter:
		if m.multiple || len(found) > 0 {
			m.accepted = true
			return m, tea.Quit
		}
	case tea.KeySpace:
		if m.multiple && len(found) > 0 {
			item := &m.items[found[m.selection]]
			if !item.locked {
				item.enabled = !item.enabled
			}
		} else {
			m.query += " "
			m.selection = 0
		}
	case tea.KeyBackspace:
		if !m.searchEnabled || !m.searching {
			return m, nil
		}
		_, size := utf8.DecodeLastRuneInString(m.query)
		m.query = m.query[:len(m.query)-size]
		m.selection = 0
	default:
		if key.Text != "" && len(m.query) < 200 && m.searchEnabled && m.searching {
			m.query += key.Text
			m.selection = 0
		}
	}
	return m, nil
}

func (m chatPicker) View() tea.View {
	if m.accepted || m.cancelled {
		return tea.NewView("")
	}
	width := m.width
	if width <= 0 {
		width = 88
	}
	panelWidth := max(6, min(96, width))
	contentWidth := panelWidth - 4
	height := 24
	if m.height > 0 {
		height = max(8, min(24, m.height-1))
	}
	header := ""
	if m.header != nil {
		header = m.header.view(width) + "\n"
		height = max(8, height-3)
	}
	if m.statusHeader != nil {
		header = m.statusHeader(width) + "\n"
		height = max(8, height-lipgloss.Height(header)+1)
	}
	// All rows have fixed cell widths and the list has a fixed row budget.
	// Neither query wrapping nor match count can change the renderer's footprint.
	fit := func(text string) string { return ansi.Truncate(text, contentWidth, "…") }
	titles := chatPickerTitleLines(m.title, contentWidth)
	maxTitles := max(1, height-8)
	if len(titles) > maxTitles {
		titles = titles[:maxTitles]
	}
	rows := make([]string, 0, height-2)
	for _, title := range titles {
		rows = append(rows, fit(title))
	}
	if m.searchEnabled {
		rows = append(rows, fit(tuiField("Search", " "+m.query)))
	}
	rows = append(rows, "")
	found := m.matches()
	count := max(1, height-2-len(rows)-3)
	start := max(0, m.selection-count+1)
	for slot := 0; slot < count; slot++ {
		pos := start + slot
		if pos >= len(found) {
			row := ""
			if slot == 0 && len(found) == 0 {
				row = "No matches"
			}
			rows = append(rows, fit(row))
			continue
		}
		item := m.items[found[pos]]
		cursor, check := "  ", ""
		if pos == m.selection {
			cursor = "› "
		}
		if m.multiple {
			check = "[ ] "
			if item.enabled {
				check = "[x] "
			}
		}
		row := cursor + check + safeTerminal(item.name) + "  " + strings.Join(strings.Fields(safeTerminal(item.detail)), " ")
		row = ansi.Truncate(row, contentWidth, "…")
		if pos == m.selection {
			row = pickerSelectedStyle().Width(contentWidth).Render(row)
		}
		rows = append(rows, row)
	}
	help := ""
	if m.multiple {
		help = "space toggle · enter save"
	} else {
		action := m.action
		if action == "" {
			action = "connect (resets chat)"
		}
		help = "enter " + action
	}
	if m.vim {
		help += " · ↑/↓ or j/k"
	} else {
		help += " · arrows select"
	}
	if m.searchEnabled {
		help += " · / search"
	}
	status := fmt.Sprintf("%d options · esc back · ctrl+c exit", len(found))
	if m.searching {
		status = fmt.Sprintf("%d options · SEARCH · esc navigation · ctrl+c exit", len(found))
	}
	rows = append(rows, "", fit(help), fit(status))
	border := lipgloss.NewStyle().Foreground(lipgloss.Blue)
	var body strings.Builder
	body.WriteString(border.Render("╭" + strings.Repeat("─", panelWidth-2) + "╮"))
	for _, row := range rows {
		padding := strings.Repeat(" ", max(0, contentWidth-lipgloss.Width(row)))
		body.WriteString("\n" + border.Render("│") + " " + row + padding + " " + border.Render("│"))
	}
	body.WriteString("\n" + border.Render("╰"+strings.Repeat("─", panelWidth-2)+"╯"))
	return tea.NewView(header + body.String())
}

// A plan must not be confirmable when this picker would silently drop any
// of its decisions. Use the same wrapping and title budget as View.
func chatPickerTitleFits(title string, width, height int, header bool) bool {
	if width <= 0 {
		width = 88
	}
	available := 24
	if height > 0 {
		available = max(8, min(24, height-1))
	}
	if header {
		available = max(8, available-3)
	}
	return len(chatPickerTitleLines(title, max(6, min(96, width))-4)) <= max(1, available-8)
}

func chatPickerTitleLines(title string, width int) []string {
	var lines []string
	for i, raw := range strings.Split(safeTerminal(title), "\n") {
		styled := tuiDetailLine(raw)
		if i == 0 {
			styled = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Blue).Render(raw)
		}
		lines = append(lines, strings.Split(ansi.Hardwrap(styled, width, true), "\n")...)
	}
	return lines
}

func pickerSelectedStyle() lipgloss.Style {
	return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Black).Background(lipgloss.Cyan)
}

func runChatPicker(ctx context.Context, in io.Reader, out io.Writer, picker chatPicker) (chatPicker, error) {
	picker.searching = picker.searchEnabled && picker.searchDefault
	if picker.header != nil || picker.statusHeader != nil {
		picker.width, picker.height = prepareLiftPane(out)
	}
	filter := func(model tea.Model, msg tea.Msg) tea.Msg {
		switch msg.(type) {
		case tea.InterruptMsg:
			return createWizardCancelMsg{}
		case tea.QuitMsg:
			m := model.(chatPicker)
			if !m.accepted && !m.cancelled {
				return createWizardCancelMsg{}
			}
		}
		return msg
	}
	result, err := tea.NewProgram(picker, tea.WithInput(in), tea.WithOutput(out), tea.WithContext(ctx), tea.WithFilter(filter)).Run()
	// Bubble Tea's inline shutdown can leave old rows when the final view is
	// shorter. Clear only after its renderer and reader have stopped; the chat
	// owner then redraws its header before opening the next prompt.
	if isInteractiveTerminal(out) {
		fmt.Fprint(out, "\x1b[H\x1b[2J")
	}
	if ctx.Err() != nil {
		return picker, ctx.Err()
	}
	if err != nil {
		return picker, err
	}
	m := result.(chatPicker)
	if m.interrupted {
		return m, context.Canceled
	}
	return m, nil
}
