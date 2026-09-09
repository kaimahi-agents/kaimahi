// Package cliui renders human-facing terminal output without changing the
// plain formats consumed by scripts, tests, and redirected logs.
package cliui

import (
	"io"
	"os"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/term"
)

type fdWriter interface {
	io.Writer
	Fd() uintptr
}

type Capabilities struct {
	Rich  bool
	Color bool
	Width int
}

// Output owns presentation policy for one destination stream. Rich layout and
// ANSI color are separate: NO_COLOR keeps hierarchy but removes escapes.
type Output struct {
	cap Capabilities
}

var (
	phaseStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Cyan)
	successStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Green)
	failureStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Red)
	headingStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Cyan)
	warningStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Yellow)
	accentStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Magenta)
	mutedStyle   = lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)
	infoStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Blue)
)

func New(w io.Writer) Output {
	return newOutput(w, os.Getenv, term.IsTerminal, term.GetSize)
}

func WithCapabilities(cap Capabilities) Output { return Output{cap: cap} }

func newOutput(w io.Writer, getenv func(string) string, isTerminal func(int) bool, getSize func(int) (int, int, error)) Output {
	file, ok := w.(fdWriter)
	if !ok || !isTerminal(int(file.Fd())) || getenv("TERM") == "dumb" {
		return Output{}
	}
	width, _, err := getSize(int(file.Fd()))
	if err != nil {
		width = 0
	}
	return Output{cap: Capabilities{Rich: true, Color: getenv("NO_COLOR") == "", Width: width}}
}

func (o Output) Rich() bool { return o.cap.Rich }
func (o Output) Width() int { return o.cap.Width }

func (o Output) Phase(text string) string   { return o.render(phaseStyle, text) }
func (o Output) Success(text string) string { return o.render(successStyle, text) }
func (o Output) Failure(text string) string { return o.render(failureStyle, text) }
func (o Output) Heading(text string) string { return o.render(headingStyle, text) }
func (o Output) Warning(text string) string { return o.render(warningStyle, text) }
func (o Output) Accent(text string) string  { return o.render(accentStyle, text) }
func (o Output) Muted(text string) string   { return o.render(mutedStyle, text) }
func (o Output) Info(text string) string    { return o.render(infoStyle, text) }

func (o Output) render(style lipgloss.Style, text string) string {
	if !o.cap.Color {
		return ansi.Strip(text)
	}
	return style.Render(text)
}
