package app

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

type createWizardCancelMsg struct{}

type createWizardStep uint8

const (
	createDescription createWizardStep = iota
	createName
	createNamespace
	createProviderType
	createModel
	createSecret
	createResultAccount
	createConfirm
	createDone
)

type createWizardKeys struct {
	Next   key.Binding
	Select key.Binding
	Cancel key.Binding
}

func (k createWizardKeys) ShortHelp() []key.Binding {
	return []key.Binding{k.Next, k.Select, k.Cancel}
}

func (k createWizardKeys) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.Next, k.Select, k.Cancel}}
}

type createWizardModel struct {
	opt       CreateOptions
	input     textinput.Model
	help      help.Model
	keys      createWizardKeys
	step      createWizardStep
	selection int
	err       error
	cancelled bool
}

func newCreateWizardModel(opt CreateOptions) (createWizardModel, error) {
	if err := refuseWizardCredentials(opt.Description, opt.Name); err != nil {
		return createWizardModel{}, err
	}
	if opt.Name != "" {
		if err := scaffold.ValidateName(opt.Name); err != nil {
			return createWizardModel{}, err
		}
	}

	input := textinput.New()
	input.Prompt = "> "
	input.CharLimit = 0
	input.SetWidth(60)
	styles := input.Styles()
	styles.Focused.Prompt = lipgloss.NewStyle().Foreground(lipgloss.Cyan).Bold(true)
	styles.Focused.Placeholder = lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)
	styles.Blurred.Placeholder = styles.Focused.Placeholder
	styles.Cursor.Color = lipgloss.Cyan
	input.SetStyles(styles)

	m := createWizardModel{
		opt:   opt,
		input: input,
		help:  help.New(),
		keys: createWizardKeys{
			Next:   key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "continue")),
			Select: key.NewBinding(key.WithKeys("left", "right", "up", "down", "tab"), key.WithHelp("arrows", "select")),
			Cancel: key.NewBinding(key.WithKeys("esc", "ctrl+c"), key.WithHelp("esc", "cancel")),
		},
	}
	m.keys.Select.SetEnabled(false)
	m.startMissingStep()
	return m, m.err
}

func (m *createWizardModel) startMissingStep() {
	switch {
	case strings.TrimSpace(m.opt.Description) == "":
		m.step = createDescription
		m.input.Placeholder = "What should this agent do?"
		m.input.Validate = requiredDescription
		m.input.CharLimit = 0
		m.input.SetValue("")
		m.input.Focus()
	case strings.TrimSpace(m.opt.Name) == "":
		m.step = createName
		m.input.Placeholder = "agent-name"
		m.input.Validate = validateAgentName
		m.input.CharLimit = 63
		m.input.SetValue(slugAgentName(m.opt.Description))
		m.input.Focus()
	case strings.TrimSpace(m.opt.Namespace) == "":
		m.startReferenceStep(createNamespace, "Namespace the Orka controller watches", scaffold.ValidateNamespace)
	case strings.TrimSpace(m.opt.ProviderType) == "":
		m.startReferenceStep(createProviderType, "openai or anthropic", nil)
	case strings.TrimSpace(m.opt.Model) == "":
		m.startReferenceStep(createModel, "Provider model identifier (not a ModelConfig)", nil)
	case strings.TrimSpace(m.opt.Secret) == "":
		m.startReferenceStep(createSecret, "Secret name, never its value", scaffold.ValidateObjectName)
	case m.opt.Task != "" && !m.opt.NoApply && m.opt.Out != "-" && !m.opt.DryRun && m.opt.ResultServiceAccount == "":
		m.startReferenceStep(createResultAccount, "Existing account in the selected namespace", scaffold.ValidateObjectName)
	default:
		m.finishFields()
	}
}

func (m *createWizardModel) startReferenceStep(step createWizardStep, placeholder string, validate func(string) error) {
	m.step = step
	m.input.Placeholder = placeholder
	m.input.CharLimit = 0
	m.input.Validate = func(value string) error {
		if value == "" {
			return fmt.Errorf("a value is required")
		}
		if validate != nil {
			return validate(value)
		}
		return nil
	}
	m.input.SetValue("")
	m.input.Focus()
}

func requiredDescription(value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("a description is required")
	}
	return nil
}

func validateAgentName(value string) error {
	return scaffold.ValidateName(strings.TrimSpace(value))
}

func (m *createWizardModel) finishFields() {
	m.input.Blur()
	if err := finishCreateWizardOptions(&m.opt); err != nil {
		m.err = err
		m.step = createDone
		return
	}
	if m.opt.Out == "-" || m.opt.NoApply {
		m.opt.NoApply = true
		m.step = createDone
		return
	}
	if m.opt.DryRun {
		m.step = createDone
		return
	}
	m.step = createConfirm
	m.selection = 0 // Enter intentionally applies by default.
	m.keys.Next.SetHelp("enter", "choose")
	m.keys.Select.SetEnabled(true)
}

func (m createWizardModel) Init() tea.Cmd {
	if m.step < createConfirm {
		return textinput.Blink
	}
	return nil
}

func (m createWizardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case createWizardCancelMsg:
		m.cancelled = true
		m.step = createDone
		return m, tea.Quit
	case tea.WindowSizeMsg:
		m.help.SetWidth(msg.Width)
		m.input.SetWidth(max(20, min(60, msg.Width-4)))
	case tea.KeyPressMsg:
		if key.Matches(msg, m.keys.Cancel) {
			m.cancelled = true
			m.step = createDone
			return m, tea.Quit
		}
		if m.step == createConfirm {
			switch {
			case key.Matches(msg, m.keys.Select):
				m.selection = 1 - m.selection
				return m, nil
			case key.Matches(msg, m.keys.Next):
				if m.selection == 1 {
					m.cancelled = true
				}
				m.step = createDone
				return m, tea.Quit
			}
		}
		if key.Matches(msg, m.keys.Next) {
			value := strings.TrimSpace(m.input.Value())
			if err := refuseWizardCredentials(value); err != nil {
				m.err = err
				m.input.SetValue("")
				m.step = createDone
				return m, tea.Quit
			}
			if m.input.Validate != nil {
				m.err = m.input.Validate(value)
			}
			if m.err != nil {
				return m, nil
			}
			switch m.step {
			case createDescription:
				m.opt.Description = value
			case createName:
				m.opt.Name = value
			case createNamespace:
				m.opt.Namespace = value
			case createProviderType:
				m.opt.ProviderType = value
			case createModel:
				m.opt.Model = value
			case createSecret:
				m.opt.Secret = value
			case createResultAccount:
				m.opt.ResultServiceAccount = value
			}
			m.err = nil
			m.startMissingStep()
			if m.step == createDone {
				return m, tea.Quit
			}
			return m, textinput.Blink
		}
	}

	if m.step < createConfirm {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		m.err = m.input.Err
		return m, cmd
	}
	return m, nil
}

func (m createWizardModel) View() tea.View {
	if m.step == createDone {
		return tea.NewView("")
	}

	var body strings.Builder
	body.WriteString(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Cyan).Render("Create an agent") + "\n\n")
	switch m.step {
	case createDescription:
		body.WriteString("Description\n")
		body.WriteString(m.input.View())
	case createName:
		body.WriteString("Description: " + displayWizardValue(m.opt.Description) + "\n\nAgent name\n")
		body.WriteString(m.input.View())
	case createNamespace, createProviderType, createModel, createSecret, createResultAccount:
		labels := map[createWizardStep]string{
			createNamespace:     "Namespace the Orka controller watches",
			createProviderType:  "Provider type (openai or anthropic)",
			createModel:         "Provider model identifier (not a ModelConfig)",
			createSecret:        "Existing Provider Secret name (not its value)",
			createResultAccount: "Existing result-reader ServiceAccount",
		}
		body.WriteString(labels[m.step] + "\n")
		body.WriteString(m.input.View())
	case createConfirm:
		fmt.Fprintf(&body, "Name:        %s\nDescription: %s\nNamespace:   %s\nProvider:    %s\nModel:       %s\nSecret:      %s\nOutput:      %s\n", displayWizardValue(m.opt.Name), displayWizardValue(m.opt.Description), displayWizardValue(m.opt.Namespace), displayWizardValue(m.opt.ProviderType), displayWizardValue(m.opt.Model), displayWizardValue(m.opt.Secret), displayWizardValue(m.opt.Out))
		if m.opt.BaseURL != "" {
			fmt.Fprintf(&body, "Base URL:    %s\n", displayWizardValue(m.opt.BaseURL))
		}
		if m.opt.Task != "" {
			body.WriteString("\n" + createTaskAuthorityNotice + "\n")
		}
		body.WriteString("\nCreate Orka resources?\n")
		choices := []string{"Apply", "Cancel"}
		for i, choice := range choices {
			marker := "  "
			if i == m.selection {
				marker = "> "
				choice = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Magenta).Render(choice)
			}
			body.WriteString(marker + choice + "  ")
		}
	}
	if m.opt.BaseURL == "" {
		body.WriteString("\n\n" + createBaseURLHint)
	}
	if m.err != nil {
		body.WriteString("\n  " + lipgloss.NewStyle().Foreground(lipgloss.Red).Render(m.err.Error()))
	}
	body.WriteString("\n\n" + m.help.View(m.keys) + "\n")
	return tea.NewView(body.String())
}

func cancelUnfinishedWizard(model tea.Model, msg tea.Msg) tea.Msg {
	// InterruptMsg makes Bubble Tea skip joining its reader during shutdown.
	// Finish cancellation through Update instead, like Escape/Ctrl-C, so the
	// terminal and reader are restored before leaving the wizard.
	switch msg.(type) {
	case tea.InterruptMsg:
		return createWizardCancelMsg{}
	case tea.QuitMsg:
		if m, ok := model.(createWizardModel); ok && m.step != createDone {
			return createWizardCancelMsg{}
		}
	}
	return msg
}

func runCreateWizard(in io.Reader, out io.Writer, opt CreateOptions) (CreateOptions, error) {
	m, err := newCreateWizardModel(opt)
	if err != nil {
		return opt, err
	}
	if m.step == createDone {
		return m.opt, nil
	}
	result, err := tea.NewProgram(m, tea.WithInput(in), tea.WithOutput(out), tea.WithFilter(cancelUnfinishedWizard)).Run()
	if err != nil {
		if errors.Is(err, tea.ErrInterrupted) || errors.Is(err, tea.ErrProgramKilled) {
			return opt, errCreateCancelled
		}
		return opt, err
	}
	completed, ok := result.(createWizardModel)
	if !ok {
		return opt, fmt.Errorf("agent-create wizard returned unexpected model %T", result)
	}
	if completed.cancelled {
		return opt, errCreateCancelled
	}
	if completed.err != nil {
		return opt, completed.err
	}
	return completed.opt, nil
}

func displayWizardValue(value string) string {
	return strings.Join(strings.Fields(ansi.Strip(value)), " ")
}
