package app

import (
	"context"
	"fmt"
	"io"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/guard"
)

type agentTUICreateResult struct{ err error }
type agentTUICreateTarget struct {
	server string
	err    error
	pane   *agentTUICreatePane
}
type agentTUICreatePane struct {
	wizard                                 createWizardModel
	env                                    agentTUIEnvironment
	server                                 string
	loading, running, finished, cancelling bool
	frame                                  int
	err                                    error
	cancel                                 context.CancelFunc
}

func (a *App) consoleCreateTarget(ctx context.Context, env agentTUIEnvironment) (string, error) {
	raw, err := env.app(a).orkaCapture(ctx, nil, "config", "view", "-o", "json")
	if err != nil {
		return "", err
	}
	kube, err := guard.ParseKubeconfig(raw)
	if err != nil {
		return "", err
	}
	p, err := guard.Classify(kube, env.Name)
	if err != nil {
		return "", err
	}
	if p.Host == "" {
		return "", fmt.Errorf("selected environment has no API server")
	}
	return p.Host, nil
}

func (a *App) consoleCreateAgent(ctx context.Context, env agentTUIEnvironment, server string, opt CreateOptions) error {
	// Recheck the reviewed target before applying. The embedded review authorizes
	// this exact context; the normal create guard still validates its metadata.
	current, err := a.consoleCreateTarget(ctx, env)
	if err != nil {
		return err
	}
	if current != server {
		return fmt.Errorf("environment server changed; reopen creation to review the new target")
	}
	worker := env.app(a)
	worker.InvocationCommand = ""
	worker.Cfg.Confirm = env.Name
	worker.Run.Context = ctx
	worker.Out, worker.Err, worker.Stdin = io.Discard, io.Discard, nil
	worker.Run.Stdout, worker.Run.Stderr = io.Discard, io.Discard
	return worker.CreateAgent(opt)
}

func (m agentTUIModel) updateCreatePane(msg tea.Msg) (tea.Model, tea.Cmd) {
	p := m.creation
	switch msg := msg.(type) {
	case agentTUICreateTarget:
		p.loading = false
		p.server, p.err = msg.server, msg.err
		if msg.err != nil {
			p.finished = true
		}
		return m, nil
	case agentTUICreateResult:
		p.running, p.finished, p.err = false, true, msg.err
		if p.cancel != nil {
			p.cancel()
		}
		if p.cancelling {
			m.status = "Creation cancelled; any completed writes remain"
		}
		if !m.opt.Demo {
			return m, m.refresh()
		}
		return m, nil
	case quickstartTickMsg:
		if !p.loading && !p.running {
			return m, nil
		}
		p.frame++
		return m, quickstartTick()
	case tea.WindowSizeMsg:
		updated, _ := p.wizard.Update(tea.WindowSizeMsg{Width: max(20, min(84, m.width-8)-10), Height: m.height})
		p.wizard = updated.(createWizardModel)
		return m, nil
	case tea.KeyPressMsg:
		cancel := msg.Code == tea.KeyEsc || msg.String() == "ctrl+c"
		if p.running {
			if cancel && !p.cancelling {
				p.cancelling = true
				p.cancel()
			}
			return m, nil
		}
		if cancel || p.finished && msg.Code == tea.KeyEnter {
			if p.cancel != nil {
				p.cancel()
			}
			m.creation = nil
			return m, nil
		}
		if p.loading || p.finished {
			return m, nil
		}
	}
	if p.loading || p.running || p.finished {
		return m, nil
	}
	updated, cmd := p.wizard.Update(msg)
	p.wizard = updated.(createWizardModel)
	if p.wizard.step != createDone {
		return m, cmd
	}
	// The shared form emits tea.Quit when finished. It closes only this pane,
	// never the parent console, and submission stays inside the same program.
	if p.wizard.cancelled {
		m.creation = nil
		return m, nil
	}
	if p.wizard.err != nil {
		p.err = p.wizard.err
		p.finished = true
		return m, nil
	}
	if m.opt.Demo {
		p.finished = true
		m.status = "DEMO · creation reviewed (nothing created)"
		return m, nil
	}
	p.running = true
	result, cancel := m.startCreate(p.env, p.server, p.wizard.opt)
	p.cancel = cancel
	return m, tea.Batch(quickstartTick(), func() tea.Msg { return <-result })
}

func (m agentTUIModel) creationView() string {
	p := m.creation
	width := min(88, m.width-8)
	height := min(26, m.height-2)
	inner := width - 4
	fit := func(s string) string { return ansi.Truncate(tuiOneLine(s), inner, "…") }
	rows := []string{fit("CREATE AGENT · " + p.env.Name), fit("Namespace: " + p.wizard.opt.Namespace + " · Server: " + valueOr(p.server, "checking…"))}
	switch {
	case p.finished:
		message := "Agent created: " + p.wizard.opt.Name
		if m.opt.Demo {
			message = "DEMO · nothing created"
		}
		if p.err != nil {
			message = "Creation stopped: " + p.err.Error()
		}
		rows = append(rows, agentTUIIndentedText(message, inner, 0)...)
		rows = rows[:min(len(rows), height-4)]
		rows = append(rows, "", "<enter> / <esc> close")
	case p.running || p.loading:
		message := "Checking environment…"
		if p.running {
			message = "Creating Provider and Agent; waiting for readiness…"
		}
		if p.cancelling {
			message = "Cancelling; waiting for work to stop…"
		}
		rows = append(rows, "", fit([]string{"⠋", "⠙", "⠹", "⠸"}[p.frame%4]+" "+message), "", "<esc> cancel")
	default:
		// Quickstart embeds the same createWizardModel; reuse its bounded field,
		// validation and review rendering instead of another creation form.
		form := quickstartWizardModel{create: p.wizard}
		rows = append(rows, form.compactQuestion(inner, height-6), "<enter> continue · arrows choose · <esc> cancel")
	}
	return lipgloss.NewStyle().Width(width).Padding(0, 1).Background(lipgloss.Color("#18232D")).Foreground(lipgloss.Color("#E6EDF3")).
		Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Cyan).BorderBackground(lipgloss.Color("#18232D")).Render(strings.Join(rows, "\n"))
}
