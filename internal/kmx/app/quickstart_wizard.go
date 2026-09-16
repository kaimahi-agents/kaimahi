package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/color"
	"io"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/guard"
	"golang.org/x/term"
)

// QuickstartWizardOptions configures the experimental path from an empty
// machine to a user-authored Orka agent.
type QuickstartWizardOptions struct {
	Create CreateOptions
}

type quickstartExistingAgent struct {
	Name, Namespace string
}

type quickstartTarget struct {
	Action, Context, Source, Server, Namespaces, Posture string
}

// QuickstartWizard overlaps host-only agent authoring with cold local runtime
// setup. Background setup never owns stdin or writes through the full-screen
// wizard; its complete log is emitted after the terminal has been restored.
func (a *App) QuickstartWizard(opt QuickstartWizardOptions) error {
	started := a.timeNow()
	if a.Stdin == nil || !isInteractiveTerminal(a.Stdin) || !isInteractiveTerminal(a.Err) || os.Getenv("TERM") == "dumb" {
		return fmt.Errorf("kmx quickstart-wizard requires an interactive terminal; use `kmx quickstart` for automation")
	}
	if err := a.validateKindTarget(); err != nil {
		return err
	}
	if err := a.preflight(depKind, depKubectl, a.engineDependency()); err != nil {
		return err
	}
	target, err := a.quickstartWizardTarget()
	if err != nil {
		return err
	}

	create := opt.Create
	create.descriptionDefault = "Hello world agent"
	if create.Namespace == "" {
		create.Namespace = OrkaNamespace
	}
	if create.ProviderType == "" {
		create.ProviderType = "openai"
	}
	if create.Secret == "" {
		create.Secret = "kickstart-provider-key"
	}
	if create.SecretKey == "" {
		create.SecretKey = "api-key"
	}
	existing := a.quickstartExistingAgents()

	var setupLog bytes.Buffer
	setup := *a
	runner := *a.Run
	runner.Stdout, runner.Stderr = &setupLog, &setupLog
	setup.Run = &runner
	setup.Out, setup.Err, setup.Stdin = &setupLog, &setupLog, nil
	setup.guarded = true
	modelPick := make(chan *localModel, 1)
	defaultModel := localModel{Provider: "bundled", Model: a.Cfg.Model, Endpoint: "http://ollama.ollama.svc.cluster.local:11434"}
	completed, imported, cancelled, setupErr, wizardErr := runQuickstartWizard(a.Stdin, a.Err, create, existing, target, defaultModel, modelPick,
		func(report func(quickstartSetupEvent)) error { return setup.quickstartWizardSetup(modelPick, report) })
	if wizardErr != nil || setupErr != nil {
		if setupLog.Len() > 0 {
			fmt.Fprintln(a.Err, "\nInfrastructure setup log:")
			_, _ = setupLog.WriteTo(a.Err)
		}
		return errors.Join(wizardErr, setupErr)
	}
	if cancelled {
		a.notef("Agent creation cancelled. Local infrastructure setup completed; no agent artifact or resources were created.")
		return nil
	}
	if imported == nil {
		if err := a.runQuickstartDeployment(completed); err != nil {
			return err
		}
	} else {
		completed.Name, completed.Namespace = imported.Name, imported.Namespace
	}
	chat, err := runQuickstartReadyScreen(a.Stdin, a.Err, completed.Name)
	if err != nil {
		return err
	}
	if chat {
		return a.quickstartOrkaChat(completed.Name, completed.Namespace)
	}
	a.complete("Agent is ready", started)
	return nil
}

func (a *App) quickstartWizardTarget() (quickstartTarget, error) {
	const action = "create a local cluster, model runtime, Orka, and a user-authored agent"
	cfg, err := a.kubeconfig()
	if err != nil {
		return quickstartTarget{}, err
	}
	posture, err := guard.Classify(cfg, a.Cfg.KubeContext)
	if err != nil {
		return quickstartTarget{}, err
	}
	// Only the already-safe local path moves its banner into the TUI. A remote
	// or unverified target retains GuardCreate's visible confirmation flow.
	if !posture.Local {
		if err := a.GuardCreate(action, "kmx quickstart-wizard"); err != nil {
			return quickstartTarget{}, err
		}
	} else {
		a.guarded = true
	}
	server := posture.Host
	if server == "" {
		server = "<none yet>"
	}
	return quickstartTarget{
		Action: action, Context: posture.Context, Source: a.Cfg.ContextSource,
		Server: server, Namespaces: "orka-system, ollama", Posture: posture.Label,
	}, nil
}

func (a *App) quickstartExistingAgents() []quickstartExistingAgent {
	raw, err := a.kubectlCapture("-n", OrkaNamespace, "get", "agents.core.orka.ai", "-o", "json")
	if err != nil {
		return nil
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if json.Unmarshal([]byte(raw), &list) != nil {
		return nil
	}
	agents := make([]quickstartExistingAgent, 0, len(list.Items))
	for _, item := range list.Items {
		if item.Metadata.Name != "" && item.Metadata.Namespace != "" {
			agents = append(agents, quickstartExistingAgent{Name: item.Metadata.Name, Namespace: item.Metadata.Namespace})
		}
	}
	return agents
}

type quickstartSetupEvent struct {
	step   int
	status string
	err    error
	model  *localModel
	models []localModel
}

func (a *App) quickstartWizardModels() []localModel {
	models := []localModel{{Provider: "bundled", Model: a.Cfg.Model}}
	env := a.localModels
	if env == nil {
		env = a.defaultLocalModelEnvironment()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for _, detector := range env.Detectors {
		found, err := detector.Detect(ctx)
		if err == nil {
			models = append(models, found...)
		}
	}
	return models
}

func (a *App) quickstartWizardSetup(modelPick <-chan *localModel, report func(quickstartSetupEvent)) error {
	run := func(step int, fn func() error) error {
		report(quickstartSetupEvent{step: step, status: "active"})
		err := fn()
		status := "done"
		if err != nil {
			status = "failed"
		}
		report(quickstartSetupEvent{step: step, status: status, err: err})
		return err
	}
	detected := make(chan []localModel, 1)
	go func() {
		report(quickstartSetupEvent{step: 1, status: "active"})
		models := a.quickstartWizardModels()
		report(quickstartSetupEvent{step: 1, status: "done", models: models})
		detected <- models
	}()
	if err := run(0, a.stepCluster); err != nil {
		return err
	}
	<-detected
	choice := <-modelPick
	if choice != nil && choice.Provider != "bundled" {
		a.selectedLocalModel = choice
		a.verifySelectedLocalModel()
		if a.selectedLocalModel != nil {
			report(quickstartSetupEvent{step: 2, status: "done", model: a.selectedLocalModel})
			report(quickstartSetupEvent{step: 3, status: "done", model: a.selectedLocalModel})
			if err := run(4, func() error { return a.prewarmHostQuickstartModel(a.selectedLocalModel) }); err != nil {
				return err
			}
			return a.quickstartWizardOrka(report)
		}
	}
	bundled := &localModel{Provider: "bundled", Model: a.Cfg.Model, Endpoint: "http://ollama.ollama.svc.cluster.local:11434"}
	report(quickstartSetupEvent{step: 3, status: "pending", model: bundled})
	if err := run(2, a.stepOllama); err != nil {
		return err
	}
	if err := run(3, a.stepModel); err != nil {
		return err
	}
	if err := run(4, a.prewarmQuickstartModel); err != nil {
		return err
	}
	return a.quickstartWizardOrka(report)
}

func (a *App) prewarmQuickstartModel() error {
	return a.kubectlRun("-n", "ollama", "exec", "deploy/ollama", "--", "ollama", "run", a.Cfg.Model, "Reply with exactly: ready")
}

func (a *App) prewarmHostQuickstartModel(model *localModel) error {
	payload := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"Reply with exactly: ready"}],"max_tokens":1}`, model.Model)
	node := a.Cfg.KindCluster + "-control-plane"
	return a.Run.Run(a.Cfg.ContainerEngine, "exec", node, "curl", "-fsS", "--max-time", "120",
		strings.TrimSuffix(model.Endpoint, "/")+"/v1/chat/completions", "-H", "Content-Type: application/json", "-d", payload)
}

func (a *App) quickstartWizardOrka(report func(quickstartSetupEvent)) error {
	return func() error {
		report(quickstartSetupEvent{step: 5, status: "active"})
		if err := a.OrkaInstall(OrkaOptions{Provider: "-"}); err != nil {
			report(quickstartSetupEvent{step: 5, status: "failed", err: err})
			return err
		}
		body := secretManifest("kickstart-provider-key", OrkaNamespace,
			map[string]string{"api-key": "not-used-by-this-endpoint"},
			map[string]string{"app.kubernetes.io/managed-by": "kmx"})
		err := a.applySecretIn(OrkaNamespace, body, "kickstart-provider-key")
		if err == nil {
			err = a.quickstartResultReader()
		}
		status := "done"
		if err != nil {
			status = "failed"
		}
		report(quickstartSetupEvent{step: 5, status: status, err: err})
		return err
	}()
}

func (a *App) quickstartResultReader() error {
	body := []byte(`apiVersion: v1
kind: ServiceAccount
metadata:
  name: orka-result-reader
  namespace: orka-system
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: orka-result-reader
  namespace: orka-system
rules:
- apiGroups: ["core.orka.ai"]
  resources: ["tasks"]
  verbs: ["get"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: orka-result-reader
  namespace: orka-system
subjects:
- kind: ServiceAccount
  name: orka-result-reader
  namespace: orka-system
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: orka-result-reader
`)
	return a.applyBytes("quickstart-wizard result reader", body)
}

type quickstartWizardModel struct {
	create       createWizardModel
	events       <-chan quickstartSetupEvent
	models       []localModel
	defaultModel localModel
	modelPick    chan<- *localModel
	modelStep    bool
	detectDone   bool
	selection    int
	chosen       *localModel
	existing     []quickstartExistingAgent
	imported     *quickstartExistingAgent
	agentStep    bool
	setup        [6]string
	setupErr     error
	setupDone    bool
	formDone     bool
	frame        int
	width        int
	target       quickstartTarget
}

type quickstartTickMsg struct{}

func waitQuickstartEvent(events <-chan quickstartSetupEvent) tea.Cmd {
	return func() tea.Msg {
		event, ok := <-events
		if !ok {
			return quickstartSetupEvent{step: -1, status: "complete"}
		}
		return event
	}
}

func quickstartTick() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return quickstartTickMsg{} })
}

func (m quickstartWizardModel) Init() tea.Cmd {
	return tea.Batch(m.create.Init(), waitQuickstartEvent(m.events), quickstartTick())
}

func (m quickstartWizardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
	case quickstartTickMsg:
		m.frame++
		return m, quickstartTick()
	case quickstartSetupEvent:
		if msg.step < 0 {
			m.setupDone = true
			if m.formDone || m.setupErr != nil {
				return m, tea.Quit
			}
			return m, nil
		}
		if msg.step < len(m.setup) {
			m.setup[msg.step] = msg.status
		}
		if msg.err != nil {
			m.setupErr = msg.err
		}
		if msg.model != nil {
			choice := *msg.model
			m.chosen = &choice
			m.create.opt.Model = choice.Model
			m.create.opt.BaseURL = strings.TrimSuffix(choice.Endpoint, "/") + "/v1"
		}
		if msg.models != nil {
			m.models = msg.models
			m.detectDone = true
			if !m.agentStep && m.modelStep && len(m.models) == 1 {
				m.chooseModel(m.models[0])
			}
		}
		return m, waitQuickstartEvent(m.events)
	case tea.KeyPressMsg:
		if m.agentStep {
			switch msg.Code {
			case tea.KeyEsc:
				m.create.cancelled, m.formDone, m.agentStep = true, true, false
				m.modelPick <- &m.defaultModel
				if m.setupDone {
					return m, tea.Quit
				}
			case tea.KeyUp, tea.KeyLeft, 'k':
				m.selection = (m.selection - 1 + len(m.existing) + 1) % (len(m.existing) + 1)
			case tea.KeyDown, tea.KeyRight, tea.KeyTab, 'j':
				m.selection = (m.selection + 1) % (len(m.existing) + 1)
			case tea.KeyEnter:
				picked := m.selection
				m.agentStep = false
				m.selection = 0
				if picked > 0 {
					choice := m.existing[picked-1]
					m.imported = &choice
					m.create.opt.Name, m.create.opt.Namespace = choice.Name, choice.Namespace
					m.create.cancelled = false
					m.create.step = createDone
					m.formDone = true
					m.modelPick <- &m.defaultModel
					if m.setupDone {
						return m, tea.Quit
					}
					return m, nil
				}
				if m.detectDone && len(m.models) == 1 {
					m.chooseModel(m.models[0])
				}
				return m, nil
			}
			return m, nil
		}
		if m.modelStep {
			if !m.detectDone || len(m.models) == 0 {
				return m, nil
			}
			switch msg.Code {
			case tea.KeyEsc:
				m.create.cancelled, m.formDone, m.modelStep = true, true, false
				m.modelPick <- &m.defaultModel
				if m.setupDone {
					return m, tea.Quit
				}
			case tea.KeyUp, tea.KeyLeft, 'k':
				m.selection = (m.selection - 1 + len(m.models)) % len(m.models)
			case tea.KeyDown, tea.KeyRight, tea.KeyTab, 'j':
				m.selection = (m.selection + 1) % len(m.models)
			case tea.KeyEnter:
				m.chooseModel(m.models[m.selection])
			}
			return m, nil
		}
	}

	updated, cmd := m.create.Update(msg)
	m.create = updated.(createWizardModel)
	if m.create.step == createDone {
		m.formDone = true
		if m.setupDone || m.setupErr != nil {
			return m, tea.Quit
		}
		return m, nil
	}
	return m, cmd
}

func (m *quickstartWizardModel) chooseModel(choice localModel) {
	m.chosen = &choice
	m.create.opt.Model = choice.Model
	if choice.Provider == "bundled" {
		m.create.opt.BaseURL = "http://ollama.ollama.svc.cluster.local:11434/v1"
	}
	m.modelPick <- m.chosen
	m.modelStep = false
	m.create.startMissingStep()
}

func (m quickstartWizardModel) View() tea.View {
	width := m.width
	if width <= 0 {
		width = 88
	}
	panelWidth := max(30, min(96, width-2))
	title := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Cyan).
		Render("KMX  /  QUICKSTART WIZARD")
	view := title + "\n\n" + m.infrastructurePanel(panelWidth) + "\n\n" + m.agentPanel(panelWidth)
	rendered := tea.NewView(view)
	rendered.AltScreen = true
	return rendered
}

func (m quickstartWizardModel) infrastructurePanel(width int) string {
	var body strings.Builder
	target := []struct{ label, value string }{
		{"ABOUT TO", m.target.Action}, {"CONTEXT", m.target.Context}, {"CHOSEN BY", m.target.Source},
		{"SERVER", m.target.Server}, {"NAMESPACES", m.target.Namespaces}, {"POSTURE", m.target.Posture},
	}
	for _, field := range target {
		if field.value == "" {
			continue
		}
		label := lipgloss.NewStyle().Foreground(lipgloss.BrightBlack).Width(12).Render(field.label)
		fmt.Fprintf(&body, "%s %s\n", label, field.value)
	}
	if body.Len() > 0 {
		body.WriteByte('\n')
	}
	labels := []string{"Kind cluster", "Detecting models", "Ollama image", "Model " + m.create.opt.Model, "Loading model", "Orka runtime"}
	for i, label := range labels {
		status := m.setup[i]
		if status == "" {
			status = "pending"
		}
		state := quickstartStatusStyle(status).Render(status)
		bar := quickstartBarStyle(status).Render(quickstartProgressBar(status, m.frame))
		detail := lipgloss.NewStyle().Foreground(lipgloss.BrightBlack).Render(m.infrastructureSize(i))
		if width >= 76 {
			fmt.Fprintf(&body, "%-16s %s %-7s %s", label, bar, state, detail)
		} else {
			fmt.Fprintf(&body, "%s\n  %s %-7s %s", label, bar, state, detail)
		}
		if i != len(labels)-1 {
			body.WriteByte('\n')
		}
	}
	return quickstartSection("TARGET & INFRASTRUCTURE", body.String(), width, lipgloss.Cyan)
}

func (m quickstartWizardModel) agentPanel(width int) string {
	var body strings.Builder
	step := quickstartInteractiveStep(m.create.step)
	if m.agentStep {
		step = 1
	} else if m.modelStep {
		step = 2
	}
	stepLabel := lipgloss.NewStyle().Foreground(lipgloss.Magenta).Bold(true).Render(fmt.Sprintf("STEP %d OF 8", step))
	body.WriteString(stepLabel + "\n\n")
	if m.agentStep {
		body.WriteString(lipgloss.NewStyle().Bold(true).Render("Start with") + "\n\n")
		choices := []string{"Create a new agent"}
		for _, agent := range m.existing {
			choices = append(choices, fmt.Sprintf("Use existing Agent %q (%s)", agent.Name, agent.Namespace))
		}
		body.WriteString(quickstartChoices(choices, m.selection))
	} else if m.modelStep {
		if !m.detectDone {
			body.WriteString(lipgloss.NewStyle().Bold(true).Render("Detecting models") + "\n\n")
			body.WriteString(lipgloss.NewStyle().Foreground(lipgloss.BrightBlack).Render("Checking local model runtimes before offering choices..."))
		} else {
			body.WriteString(lipgloss.NewStyle().Bold(true).Render("Choose a model") + "\n\n")
			choices := make([]string, 0, len(m.models))
			for _, model := range m.models {
				choices = append(choices, m.quickstartModelChoiceLabel(model))
			}
			body.WriteString(quickstartChoices(choices, m.selection))
			body.WriteString("\n\n" + lipgloss.NewStyle().Foreground(lipgloss.BrightBlack).Render("enter choose  •  arrows/j/k select  •  esc cancel"))
		}
	} else if m.formDone {
		if m.setupErr != nil {
			body.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Red).Render("Infrastructure setup failed. Restoring the terminal for diagnostics."))
		} else {
			body.WriteString(fmt.Sprintf("Agent %q is queued. Waiting for the model and runtime before deployment...", m.create.opt.Name))
		}
	} else {
		inner := m.create.View().Content
		inner = strings.TrimPrefix(inner, "Create an agent\n\n")
		body.WriteString(inner)
	}
	if m.chosen != nil && !m.modelStep {
		fmt.Fprintf(&body, "\n\n%s %s", lipgloss.NewStyle().Foreground(lipgloss.BrightBlack).Render("MODEL"), quickstartModelLabel(*m.chosen))
	}
	if m.formDone {
		heading := color.Color(lipgloss.Magenta)
		if m.setupErr != nil {
			heading = lipgloss.Red
		}
		return quickstartSection("AGENT SETUP", body.String(), width, heading)
	}
	return quickstartPanel("AGENT SETUP", body.String(), width, lipgloss.Magenta, lipgloss.Magenta)
}

func (m quickstartWizardModel) quickstartModelChoiceLabel(model localModel) string {
	if model.Provider == "bundled" && m.setup[3] == "done" {
		return fmt.Sprintf("%s (KMX managed, already downloaded)", model.Model)
	}
	return quickstartModelLabel(model)
}

func quickstartPanel(title, body string, width int, headingColor, borderColor color.Color) string {
	innerWidth := max(24, width-4)
	heading := lipgloss.NewStyle().Bold(true).Foreground(headingColor).Render(" " + title + " ")
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Padding(1, 2).
		Width(innerWidth).
		Render(heading + "\n\n" + body)
}

func quickstartSection(title, body string, width int, headingColor color.Color) string {
	heading := lipgloss.NewStyle().Bold(true).Foreground(headingColor).Render(title)
	return lipgloss.NewStyle().Width(max(24, width-2)).PaddingLeft(1).Render(heading + "\n\n" + body)
}

func quickstartChoices(choices []string, selected int) string {
	var body strings.Builder
	for i, choice := range choices {
		if i > 0 {
			body.WriteByte('\n')
		}
		if i == selected {
			body.WriteString(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Magenta).Render("› " + choice))
		} else {
			body.WriteString(lipgloss.NewStyle().Foreground(lipgloss.BrightBlack).Render("  " + choice))
		}
	}
	return body.String()
}

func quickstartStatusStyle(status string) lipgloss.Style {
	color := lipgloss.BrightBlack
	if status == "done" {
		color = lipgloss.Green
	} else if status == "active" {
		color = lipgloss.Cyan
	} else if status == "failed" {
		color = lipgloss.Red
	}
	return lipgloss.NewStyle().Foreground(color).Bold(status != "pending")
}

func quickstartBarStyle(status string) lipgloss.Style {
	return quickstartStatusStyle(status)
}

func (m quickstartWizardModel) infrastructureSize(step int) string {
	switch step {
	case 0:
		return "~1.3 GB node image"
	case 1:
		return "host runtimes"
	case 2:
		if m.chosen != nil && m.chosen.Provider != "bundled" {
			return "skipped; host runtime"
		}
		return "~1.1 GB image"
	case 3:
		model := m.chosen
		if model == nil && len(m.models) > 0 {
			model = &m.models[0]
		}
		if model != nil && model.Provider != "bundled" {
			if model.Size > 0 {
				return fmt.Sprintf("%s already installed", quickstartSize(model.Size))
			}
			return "already installed; size unknown"
		}
		return "~1.9 GB model"
	case 4:
		return "weights into memory"
	case 5:
		return "~860 MB images"
	default:
		return ""
	}
}

func quickstartSize(size int64) string {
	if size >= 1_000_000_000 {
		return fmt.Sprintf("%.1f GB", float64(size)/1_000_000_000)
	}
	return fmt.Sprintf("%.0f MB", float64(size)/1_000_000)
}

func quickstartModelLabel(model localModel) string {
	if model.Provider == "bundled" {
		return fmt.Sprintf("%s (KMX managed, download during setup)", model.Model)
	}
	size := "size unknown"
	if model.Size > 0 {
		size = quickstartSize(model.Size)
	}
	source := model.Provider + " managed"
	if model.Provider == "ollama" {
		source = "Ollama managed"
	}
	return fmt.Sprintf("%s (%s, %s)", model.Model, source, size)
}

func quickstartInteractiveStep(step createWizardStep) int {
	switch step {
	case createDescription:
		return 2
	case createName:
		return 3
	case createNamespace:
		return 4
	case createProviderType:
		return 5
	case createModel:
		return 6
	case createSecret:
		return 7
	case createResultAccount:
		return 7
	default:
		return 8
	}
}

func quickstartProgressBar(status string, frame int) string {
	const width = 18
	switch status {
	case "done":
		return "[" + strings.Repeat("=", width) + "]"
	case "failed":
		return "[" + strings.Repeat("!", width) + "]"
	case "active":
		position := frame % (width - 3)
		return "[" + strings.Repeat(" ", position) + "====" + strings.Repeat(" ", width-position-4) + "]"
	default:
		return "[" + strings.Repeat(".", width) + "]"
	}
}

func runQuickstartWizard(in io.Reader, out io.Writer, opt CreateOptions, existing []quickstartExistingAgent, target quickstartTarget, defaultModel localModel, modelPick chan<- *localModel, setup func(func(quickstartSetupEvent)) error) (CreateOptions, *quickstartExistingAgent, bool, error, error) {
	create, err := newCreateWizardModel(opt)
	if err != nil {
		return opt, nil, false, nil, err
	}
	events := make(chan quickstartSetupEvent, 12)
	setupResult := make(chan error, 1)
	go func() {
		setupResult <- setup(func(event quickstartSetupEvent) { events <- event })
		close(events)
	}()
	model := quickstartWizardModel{create: create, events: events, existing: existing, target: target, defaultModel: defaultModel, modelPick: modelPick, modelStep: true, agentStep: true}
	result, runErr := tea.NewProgram(model, tea.WithInput(in), tea.WithOutput(out)).Run()
	setupErr := <-setupResult
	if runErr != nil {
		return opt, nil, false, setupErr, runErr
	}
	completed, ok := result.(quickstartWizardModel)
	if !ok {
		return opt, nil, false, setupErr, fmt.Errorf("quickstart wizard returned unexpected model %T", result)
	}
	if completed.create.err != nil {
		return opt, nil, false, setupErr, completed.create.err
	}
	return completed.create.opt, completed.imported, completed.create.cancelled, setupErr, nil
}

type quickstartReadyModel struct {
	name      string
	selection int
	width     int
}

func (m quickstartReadyModel) Init() tea.Cmd { return nil }
func (m quickstartReadyModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		m.width = size.Width
		return m, nil
	}
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch key.Code {
		case tea.KeyLeft, tea.KeyRight, tea.KeyUp, tea.KeyDown, tea.KeyTab, 'j', 'k':
			m.selection = 1 - m.selection
		case tea.KeyEnter:
			return m, tea.Quit
		}
	}
	return m, nil
}
func (m quickstartReadyModel) View() tea.View {
	width := m.width
	if width <= 0 {
		width = 72
	}
	panelWidth := max(30, min(80, width-2))
	choices := []string{"Chat with agent", "Finish"}
	ready := quickstartSection("READY",
		lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Green).Render("Agent "+fmt.Sprintf("%q", m.name)+" is ready")+"\n\n"+
			lipgloss.NewStyle().Foreground(lipgloss.BrightBlack).Render("The model and Orka runtime are available."),
		panelWidth, lipgloss.Green)
	actions := quickstartPanel("NEXT STEP",
		lipgloss.NewStyle().Bold(true).Render("What would you like to do?")+"\n\n"+
			quickstartChoices(choices, m.selection)+"\n\n"+
			lipgloss.NewStyle().Foreground(lipgloss.BrightBlack).Render("enter choose  •  arrows/j/k select"),
		panelWidth, lipgloss.Magenta, lipgloss.Magenta)
	rendered := tea.NewView(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Cyan).Render("KMX  /  QUICKSTART COMPLETE") + "\n\n" + ready + "\n\n" + actions)
	rendered.AltScreen = true
	return rendered
}

func runQuickstartReadyScreen(in io.Reader, out io.Writer, name string) (bool, error) {
	result, err := tea.NewProgram(quickstartReadyModel{name: name}, tea.WithInput(in), tea.WithOutput(out)).Run()
	if err != nil {
		return false, err
	}
	return result.(quickstartReadyModel).selection == 0, nil
}

func (a *App) quickstartOrkaChat(agent, namespace string) error {
	if err := waitForStableTerminalSize(a.Stdin, a.Out); err != nil {
		return err
	}
	return a.runInteractiveChatBackend(&orkaChatBackend{app: a, agent: agent, namespace: namespace})
}

func waitForStableTerminalSize(in *os.File, out io.Writer) error {
	outFile, ok := out.(*os.File)
	if in == nil || !ok || !term.IsTerminal(int(in.Fd())) || !term.IsTerminal(int(outFile.Fd())) {
		return nil
	}
	deadline := time.Now().Add(time.Second)
	lastWidth, lastHeight := 0, 0
	stableSince := time.Time{}
	for time.Now().Before(deadline) {
		width, height, err := term.GetSize(int(outFile.Fd()))
		if err == nil && width > 0 && height > 0 {
			if width == lastWidth && height == lastHeight {
				if !stableSince.IsZero() && time.Since(stableSince) >= 250*time.Millisecond {
					return nil
				}
			} else {
				lastWidth, lastHeight, stableSince = width, height, time.Now()
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	return fmt.Errorf("terminal dimensions did not stabilize after quickstart; chat was not started")
}

type orkaChatBackend struct {
	app              *App
	agent, namespace string
}

func (b *orkaChatBackend) Agent() string { return b.agent }

func (b *orkaChatBackend) Connect(_ context.Context, renderer *chatRenderer) error {
	renderer.statusStart(b.agent, b.app.Cfg.KubeContext)
	renderer.statusSection("Runtime", "Orka | namespace "+b.namespace)
	renderer.statusSection("Tasks", "Each message creates one fresh local Task")
	renderer.statusEnd()
	return nil
}

func (b *orkaChatBackend) Send(ctx context.Context, message string, renderer *chatRenderer) error {
	renderer.beginAssistant(b.agent)
	renderer.assistantOperation(b.agent, "WORKING", "", colorBlue, "Running a fresh Orka Task and worker; local CPU inference can take about 30-60s")
	answer, err := b.app.runQuickstartOrkaTaskContext(ctx, b.agent, b.namespace, message)
	if err != nil {
		return err
	}
	renderer.assistant(b.agent, answer, true)
	return nil
}

func (a *App) runQuickstartOrkaTask(agent, namespace, prompt string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	return a.runQuickstartOrkaTaskContext(ctx, agent, namespace, prompt)
}

func (a *App) runQuickstartOrkaTaskContext(parent context.Context, agent, namespace, prompt string) (string, error) {
	suffix, err := randomHex(8)
	if err != nil {
		return "", err
	}
	prefix := strings.TrimRight(agent[:min(len(agent), 40)], "-")
	doc := map[string]any{
		"apiVersion": "core.orka.ai/v1alpha1", "kind": "Task",
		"metadata": map[string]any{"name": prefix + "-" + suffix, "namespace": namespace},
		"spec":     map[string]any{"type": "ai", "prompt": prompt, "agentRef": map[string]any{"name": agent, "namespace": namespace}, "resources": map[string]any{}},
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Minute)
	defer cancel()
	opt := CreateOptions{Namespace: namespace, ResultServiceAccount: "orka-result-reader", OrkaAPIService: "orka-api", ResultPort: "19180", Task: prompt}
	quiet := *a
	var diagnostics bytes.Buffer
	quiet.Err = &diagnostics
	session, err := quiet.openOrkaResultSession(ctx, opt)
	if err != nil {
		return "", err
	}
	defer session.close()
	id, err := a.createOrkaObject(session.ctx, namespace, doc)
	if err != nil {
		return "", err
	}
	return a.waitOrkaTaskResult(session.ctx, namespace, id, session)
}

type quickstartDeployDone struct{ err error }
type quickstartDeployModel struct {
	name  string
	done  bool
	err   error
	frame int
}

func (m quickstartDeployModel) Init() tea.Cmd { return quickstartTick() }
func (m quickstartDeployModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case quickstartTickMsg:
		m.frame++
		return m, quickstartTick()
	case quickstartDeployDone:
		m.done, m.err = true, msg.err
		return m, tea.Quit
	}
	return m, nil
}
func (m quickstartDeployModel) View() tea.View {
	status := "active"
	if m.err != nil {
		status = "failed"
	}
	if m.done && m.err == nil {
		status = "done"
	}
	rendered := tea.NewView(fmt.Sprintf("Deploy Agent %q (Orka agent)\n\n  Provider and Agent %s %s\n", m.name, quickstartProgressBar(status, m.frame), status))
	rendered.AltScreen = true
	return rendered
}

func (a *App) runQuickstartDeployment(opt CreateOptions) error {
	var log bytes.Buffer
	worker := *a
	runner := *a.Run
	runner.Stdout, runner.Stderr = &log, &log
	worker.Run, worker.Out, worker.Err, worker.Stdin = &runner, &log, &log, nil
	done := make(chan error, 1)
	program := tea.NewProgram(quickstartDeployModel{name: opt.Name}, tea.WithInput(a.Stdin), tea.WithOutput(a.Err))
	go func() {
		err := worker.CreateAgent(opt)
		done <- err
		program.Send(quickstartDeployDone{err: err})
	}()
	_, runErr := program.Run()
	deployErr := <-done
	if runErr != nil || deployErr != nil {
		if log.Len() > 0 {
			fmt.Fprintln(a.Err, "\nAgent deployment log:")
			_, _ = log.WriteTo(a.Err)
		}
	}
	return errors.Join(runErr, deployErr)
}
