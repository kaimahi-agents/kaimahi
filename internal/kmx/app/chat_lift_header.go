package app

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/cliui"
	"golang.org/x/term"
)

type liftHeader struct {
	agent, source, target string
	step                  int
}

var liftHeaderStages = []string{"Target", "Orka", "Inference", "Tools", "Deploy", "Connect"}

func (h liftHeader) view(width int) string {
	if width <= 0 {
		width = 88
	}
	fit := func(s string) string { return ansi.Truncate(s, width, "…") }
	target := h.target
	if target == "" {
		target = "select cluster"
	}
	rows := []string{fit("KMX / " + tuiField("agent", h.agent) + " · " + tuiField("source", h.source)), fit(tuiField("target", target))}
	var stages []string
	for i, name := range liftHeaderStages {
		label := fmt.Sprintf("%d %s", i+1, name)
		if i == h.step {
			label = pickerSelectedStyle().Render(" " + label + " ")
		}
		stages = append(stages, label)
	}
	bar := strings.Join(stages, " › ")
	if ansi.StringWidth(bar) > width {
		bar = fmt.Sprintf("Step %d/%d · ", h.step+1, len(liftHeaderStages)) + pickerSelectedStyle().Render(liftHeaderStages[h.step]) + " " + strings.Repeat("━", h.step+1) + strings.Repeat("─", len(liftHeaderStages)-h.step-1)
	}
	return strings.Join(append(rows, fit(bar)), "\n")
}

func (b *orkaChatBackend) paintLiftHeader() {
	if b.liftHeader == nil || !isInteractiveTerminal(b.app.Out) {
		return
	}
	width := cliui.New(b.app.Out).Width()
	fmt.Fprint(b.app.Out, "\x1b[H\x1b[2J", b.liftHeader.view(width), "\r\n")
}

// Transfer the screen from the static fetch/status renderer to a pane that
// includes its own header. Start at the top, not below the previous header.
// Seed dimensions before the first frame to avoid scrolling a short terminal
// while waiting for Bubble Tea's initial WindowSizeMsg.
func prepareLiftPane(out io.Writer) (width, height int) {
	if file, ok := out.(*os.File); ok && isInteractiveTerminal(file) {
		width, height, _ = term.GetSize(int(file.Fd()))
		fmt.Fprint(out, "\x1b[r\x1b[H\x1b[2J")
	}
	return width, height
}
