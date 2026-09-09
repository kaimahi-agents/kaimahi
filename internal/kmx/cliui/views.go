package cliui

import (
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	liptable "charm.land/lipgloss/v2/table"
	"charm.land/lipgloss/v2/tree"
	"github.com/charmbracelet/x/ansi"
)

type Field struct {
	Label string
	Value string
}

type Action struct {
	Label    string
	Command  string
	Detail   string
	Children []Action
}

func (o Output) Fields(fields []Field) string {
	width := 0
	for _, field := range fields {
		width = max(width, lipgloss.Width(field.Label))
	}
	var lines []string
	for _, field := range fields {
		if !o.cap.Color {
			field.Label, field.Value = ansi.Strip(field.Label), ansi.Strip(field.Value)
		}
		if o.cap.Width > 0 && (width+4 >= o.cap.Width ||
			(width+4+lipgloss.Width(field.Value) > o.cap.Width &&
				(lipgloss.Width(field.Value) <= o.cap.Width-4 || width+4 > o.cap.Width/2))) {
			labelIndent := min(2, o.cap.Width-1)
			valueIndent := min(4, o.cap.Width-1)
			label := lipgloss.NewStyle().Width(o.cap.Width - labelIndent).Render(field.Label)
			if o.cap.Color {
				label = lipgloss.NewStyle().Bold(true).Render(label)
			}
			lines = append(lines,
				lipgloss.NewStyle().PaddingLeft(labelIndent).Render(label),
				lipgloss.NewStyle().PaddingLeft(valueIndent).Render(lipgloss.NewStyle().Width(o.cap.Width-valueIndent).Render(field.Value)))
			continue
		}
		labelStyle := lipgloss.NewStyle().Width(width)
		if o.cap.Color {
			labelStyle = labelStyle.Bold(true)
		}
		label := labelStyle.Render(field.Label)
		prefix := "  " + label + "  "
		valueStyle := lipgloss.NewStyle()
		if o.cap.Width > lipgloss.Width(prefix) {
			valueStyle = valueStyle.Width(o.cap.Width - lipgloss.Width(prefix))
		}
		lines = append(lines, lipgloss.JoinHorizontal(lipgloss.Top, prefix, valueStyle.Render(field.Value)))
	}
	return strings.Join(lines, "\n")
}

func (o Output) Table(headers []string, rows [][]string) string {
	return o.TableWithRoles(headers, rows, nil)
}

// ColumnRole assigns meaning at the call site, never by guessing from a name
// or identifier that happens to contain a status word.
type ColumnRole int

const (
	ColumnText  ColumnRole = iota
	ColumnState            // includes numeric HTTP outcomes
	ColumnNumber
)

func (o Output) TableWithRoles(headers []string, rows [][]string, roles []ColumnRole) string {
	if len(rows) == 0 {
		return o.Muted("  none")
	}
	if !o.cap.Color {
		clean := make([]string, len(headers))
		for i, header := range headers {
			clean[i] = ansi.Strip(header)
		}
		headers = clean
	}
	styled := make([][]string, len(rows))
	for i, row := range rows {
		styled[i] = make([]string, len(row))
		for j, value := range row {
			role := ColumnText
			if j < len(roles) {
				role = roles[j]
			}
			styled[i][j] = o.cell(value, role)
		}
	}
	rows = styled
	if o.cap.Width > 0 && tableWidth(headers, rows) > o.cap.Width {
		return o.stackedTable(headers, rows)
	}
	t := liptable.New().Headers(headers...).Rows(rows...).Border(lipgloss.HiddenBorder()).
		BorderTop(false).BorderBottom(false).BorderLeft(false).BorderRight(false).
		BorderHeader(false).BorderRow(false).BorderColumn(false).Width(tableWidth(headers, rows)).
		StyleFunc(func(row, _ int) lipgloss.Style {
			style := lipgloss.NewStyle().Padding(0, 1)
			if row == liptable.HeaderRow && o.cap.Color {
				style = style.Bold(true).Foreground(lipgloss.Cyan)
			}
			return style
		})
	return t.String()
}

func (o Output) cell(value string, role ColumnRole) string {
	if !o.cap.Color {
		return ansi.Strip(value)
	}
	if role == ColumnNumber {
		return o.Info(value)
	}
	if role != ColumnState {
		return value
	}
	state := strings.ToLower(strings.TrimSpace(value))
	if code, err := strconv.Atoi(state); err == nil {
		switch {
		case code >= 400 && code <= 599:
			return o.Failure(value)
		case code >= 200 && code <= 299:
			return o.Success(value)
		default:
			return o.Warning(value)
		}
	}
	state, _, _ = strings.Cut(state, " (") // cached condition age is still shown
	switch state {
	case "yes", "ready", "ok", "allowed", "approved", "admitted", "running", "succeeded":
		return o.Success(value)
	case "no", "denied", "failed", "expired", "error":
		return o.Failure(value)
	case "unknown", "pending", "requested", "expiring", "no expiry", "attention required":
		return o.Warning(value)
	}
	return value
}

func (o Output) Report(title string, headers []string, rows [][]string, roles ...ColumnRole) string {
	return o.Heading(title+" ("+strconv.Itoa(len(rows))+")") + "\n" + o.TableWithRoles(headers, rows, roles)
}

func tableWidth(headers []string, rows [][]string) int {
	widths := make([]int, len(headers))
	for i, header := range headers {
		widths[i] = lipgloss.Width(header)
	}
	for _, row := range rows {
		for i, value := range row {
			if i < len(widths) {
				widths[i] = max(widths[i], lipgloss.Width(value))
			}
		}
	}
	total := 0
	for _, width := range widths {
		total += width + 2
	}
	return total
}

func (o Output) stackedTable(headers []string, rows [][]string) string {
	var records []string
	for _, row := range rows {
		fields := make([]Field, 0, min(len(headers), len(row)))
		for i, value := range row {
			if i < len(headers) {
				fields = append(fields, Field{Label: headers[i], Value: value})
			}
		}
		records = append(records, o.Fields(fields))
	}
	return strings.Join(records, "\n"+o.Muted(strings.Repeat("─", min(24, max(1, o.cap.Width))))+"\n")
}

func (o Output) Actions(root string, actions []Action) string {
	t := tree.Root(o.Heading(o.wrap(root, 0)))
	for _, action := range actions {
		t.Child(actionNode(o, action, 4))
	}
	return t.String()
}

func actionNode(o Output, action Action, indent int) *tree.Tree {
	label := action.Label
	if action.Detail != "" {
		label += " — " + action.Detail
	}
	node := tree.Root(o.wrap(label, indent))
	if action.Command != "" {
		node.Child(o.Accent(action.Command))
	}
	for _, child := range action.Children {
		node.Child(actionNode(o, child, indent+4))
	}
	return node
}

func (o Output) wrap(text string, indent int) string {
	if !o.cap.Color {
		text = ansi.Strip(text)
	}
	if o.cap.Width > 0 {
		return lipgloss.NewStyle().Width(max(1, o.cap.Width-indent)).Render(text)
	}
	return text
}

type CalloutKind int

const (
	CalloutInfo CalloutKind = iota
	CalloutWarning
	CalloutDanger
)

func (o Output) Callout(kind CalloutKind, title string, fields []Field) string {
	color := lipgloss.Cyan
	if kind == CalloutWarning {
		color = lipgloss.Yellow
	} else if kind == CalloutDanger {
		color = lipgloss.Red
	}
	style := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	if o.cap.Color {
		style = style.BorderForeground(color)
	}
	body := o.Heading(title)
	if len(fields) > 0 {
		inner := o
		if o.cap.Width > 6 {
			inner.cap.Width = o.cap.Width - 4
		}
		body += "\n" + inner.Fields(fields)
	}
	if o.cap.Width > 4 {
		style = style.Width(o.cap.Width - 4)
	}
	return style.Render(body)
}
