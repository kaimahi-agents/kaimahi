package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/guard"
)

// QuickstartWizardOptions configures the experimental path from an empty
// machine to a user-authored Orka agent.
type QuickstartWizardOptions struct {
	Create          CreateOptions
	Verbose         bool
	Inference       string
	Orka            OrkaSetupOptions
	EnforcementFile string
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
	if opt.Inference == "foundry" || opt.Inference == "copilot" {
		return fmt.Errorf("quickstart host Foundry/Copilot modes are retired; use an existing Ready destination Orka openai Provider with an exact model in the authored suite workspace, then test → push → deploy")
	}
	if err := validateQuickstartSourceOptions(opt.Create); err != nil {
		return err
	}
	if err := a.prepareQuickstartEnforcement(opt.Orka, opt.EnforcementFile); err != nil {
		return err
	}
	if opt.Create.Namespace != "" && a.orkaSetup != nil &&
		opt.Create.Namespace != a.orkaSetupTaskNamespace() {
		return fmt.Errorf("guided Agent namespace must match the selected Orka chart's Task namespace")
	}
	if _, err := quickstartInference(opt.Inference, "detected-later"); err != nil {
		return err
	}
	a.chatVerbose = opt.Verbose
	started := a.timeNow()
	if a.Stdin == nil || !isInteractiveTerminal(a.Stdin) || !isInteractiveTerminal(a.Err) || os.Getenv("TERM") == "dumb" {
		return fmt.Errorf("kmx quickstart --interactive requires an interactive terminal; use `kmx quickstart` for automation")
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
		create.Namespace = a.orkaSetupTaskNamespace()
	}
	existing := a.quickstartExistingAgents()

	var setupLog bytes.Buffer
	setup := *a
	setup.chatInference = opt.Inference
	cfg := *a.Cfg
	setup.Cfg = &cfg
	runner := *a.Run
	setupCtx, cancelSetup := context.WithCancel(a.operationContext())
	defer cancelSetup()
	runner.Context = setupCtx
	runner.Stdout, runner.Stderr = &setupLog, &setupLog
	setup.Run = &runner
	setup.Out, setup.Err, setup.Stdin = &setupLog, &setupLog, nil
	setup.guarded = true
	modelPick := make(chan *localModel, 1)
	defaultModel := localModel{Provider: "bundled", Model: a.Cfg.Model, Endpoint: "http://ollama.ollama.svc.cluster.local:11434"}
	completed, imported, cancelled, setupErr, wizardErr := runQuickstartWizard(a.Stdin, a.Err, create, existing, target, defaultModel, modelPick, cancelSetup,
		func(report func(quickstartSetupEvent)) error { return setup.quickstartWizardSetup(modelPick, report) }, a.checkQuickstartName)
	if cancelled {
		a.notef("Quickstart cancelled. Active setup work was stopped; completed resources were left in place.")
		return nil
	}
	if wizardErr != nil || setupErr != nil {
		if setupLog.Len() > 0 {
			fmt.Fprintln(a.Err, "\nInfrastructure setup log:")
			_, _ = setupLog.WriteTo(a.Err)
		}
		return errors.Join(wizardErr, setupErr)
	}
	if imported != nil {
		completed.Name, completed.Namespace = imported.Name, imported.Namespace
	}
	a.chatInference = "local"
	// Shared review owns execution consent and real Apply/Verify, not live create.
	if imported == nil {
		root, err := a.authorQuickstartSource(a.operationContext(), completed)
		if root != "" {
			a.notef("Authored workspace: %s. Source is retained; scaffold cases are not quality proof.", root)
		}
		if err != nil {
			return err
		}
		result, err := a.runSuiteWorkspaceReview(a.operationContext(), root, completed.Namespace)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				a.notef("Quickstart cancelled. Authored source and completed resources remain; evaluation or placement may already have occurred.")
				return nil
			}
			return err
		}
		if completed.Task != "" {
			answer, err := a.answerSuitePlacement(a.operationContext(), result, completed.Task)
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintln(a.Out, answer); err != nil {
				return err
			}
		}
	}
	if imported != nil {
		if err := a.checkQuickstartExistingAgent(a.operationContext(), completed.Name, completed.Namespace); err != nil {
			return err
		}
	}
	location := a.Cfg.KubeContext
	if strings.HasPrefix(target.Posture, "local") {
		location = "local-" + location
	}
	chat, err := runQuickstartReadyScreen(a.Stdin, a.Err, completed.Name, location)
	if err != nil {
		return err
	}
	if chat {
		return a.quickstartOrkaChat(completed.Name, completed.Namespace)
	}
	a.complete("Agent is ready", started)
	return nil
}

// Reconnecting never repairs tool references, but a name alone is not Ready.
func (a *App) checkQuickstartExistingAgent(ctx context.Context, name, namespace string) error {
	raw, err := a.orkaCapture(ctx, nil, "-n", namespace, "get", orkaPlural("Agent"), name, "--ignore-not-found=true", "-o", "json")
	if err != nil {
		return fmt.Errorf("cannot check existing Agent readiness: %w", err)
	}
	var live orkaObject
	if json.Unmarshal(raw, &live) != nil || live.Kind != "Agent" || live.Metadata.Name != name || live.Metadata.Namespace != namespace || live.Metadata.UID == "" || live.Metadata.Generation < 1 || live.Metadata.DeletionTimestamp != nil {
		return fmt.Errorf("existing Agent identity is unavailable; refresh and select a live Agent")
	}
	if !orkaCurrentGenerationReady(&live, live.Metadata.Generation) {
		return fmt.Errorf("existing Agent is not Ready at its current generation; inspect it before reconnecting")
	}
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
		if err := a.GuardCreateIn(action, a.operationCommand("quickstart", "--interactive"), a.orkaSetupNamespaces()); err != nil {
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
		Server: server, Namespaces: a.orkaSetupNamespaces(), Posture: posture.Label,
	}, nil
}

func (a *App) quickstartExistingAgents() []quickstartExistingAgent {
	raw, err := a.kubectlCapture("-n", a.orkaSetupTaskNamespace(), "get", "agents.core.orka.ai", "-o", "json")
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
	note   string
}

func (a *App) quickstartWizardModels() []localModel {
	models := []localModel{{Provider: "bundled", Model: a.Cfg.Model}}
	env := a.localModels
	if env == nil {
		env = a.defaultLocalModelEnvironment()
	}
	ctx, cancel := context.WithTimeout(a.operationContext(), 2*time.Second)
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
		if err := a.operationContext().Err(); err != nil {
			return err
		}
		report(quickstartSetupEvent{step: step, status: "active"})
		err := fn()
		status := "done"
		if err != nil {
			status = "failed"
		}
		report(quickstartSetupEvent{step: step, status: status, err: err})
		return err
	}
	report(quickstartSetupEvent{step: 0, status: "active"})
	models := a.quickstartWizardModels()
	if err := a.operationContext().Err(); err != nil {
		return err
	}
	note := "Native Orka inference; host Foundry/Copilot quickstart modes are retired. Review the authored workspace and every scaffold case, then select a Ready destination Orka openai Provider with the exact model before test → push → deploy."
	report(quickstartSetupEvent{step: 0, status: "done", models: models, note: note})
	var choice *localModel
	select {
	case <-a.operationContext().Done():
		return a.operationContext().Err()
	case choice = <-modelPick:
	}
	if choice == nil {
		return fmt.Errorf("inference selection is required")
	}
	if choice.Provider == "foundry" || choice.Provider == "copilot" {
		return fmt.Errorf("quickstart host Foundry/Copilot modes are retired; use an existing Ready destination Orka openai Provider with an exact model via authored suite test → push → deploy")
	}
	a.chatInference = "local"
	if choice.Provider == "existing" {
		for step := 1; step <= 5; step++ {
			if step == 5 && a.orkaSetup != nil {
				if err := run(step, a.prepareOrkaRuntime); err != nil {
					return err
				}
				continue
			}
			report(quickstartSetupEvent{step: step, status: "skipped", note: "Using the existing Agent Provider without changing tool references."})
		}
		return nil
	}
	if err := run(1, a.stepCluster); err != nil {
		return err
	}
	report(quickstartSetupEvent{step: 2, status: "waiting"})
	if choice != nil && choice.Provider != "bundled" {
		report(quickstartSetupEvent{step: 2, status: "active", note: "Checking the selected host runtime from kind..."})
		selected := *choice // Verification owns its copy, never the UI's selection.
		a.selectedLocalModel = &selected
		a.verifySelectedLocalModel()
		if err := a.operationContext().Err(); err != nil {
			return err
		}
		if a.selectedLocalModel != nil {
			report(quickstartSetupEvent{step: 2, status: "done", model: a.selectedLocalModel, note: "Host runtime verified; reusing the installed model."})
			report(quickstartSetupEvent{step: 3, status: "skipped"})
			if err := run(4, func() error { return a.prewarmHostQuickstartModel(a.selectedLocalModel) }); err != nil {
				return err
			}
			return a.quickstartWizardOrka(report)
		}
	}
	bundled := &localModel{Provider: "bundled", Model: a.Cfg.Model, Endpoint: "http://ollama.ollama.svc.cluster.local:11434"}
	note = "Using the KMX-managed model."
	if choice != nil && choice.Provider != "bundled" {
		note = "Selected host model is unreachable from kind; falling back to KMX-managed " + bundled.Model + "."
	}
	report(quickstartSetupEvent{step: 2, status: "active", model: bundled, note: note})
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
	// An empty prompt loads weights without replacing the cached agent/tool
	// prefix with an unrelated health-check prompt on every wizard rerun.
	return a.kubectlRun("-n", "ollama", "exec", "deploy/ollama", "--", "ollama", "run", a.Cfg.Model)
}

func (a *App) prewarmHostQuickstartModel(model *localModel) error {
	payload := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"Reply with exactly: ready"}],"max_tokens":1}`, model.Model)
	path := "/v1/chat/completions"
	if model.Provider == "ollama" {
		payload = fmt.Sprintf(`{"model":%q,"stream":false,"keep_alive":"1h"}`, model.Model)
		path = "/api/generate"
	}
	node := a.Cfg.KindCluster + "-control-plane"
	return a.Run.Run(a.Cfg.ContainerEngine, "exec", node, "curl", "-fsS", "--max-time", "120",
		strings.TrimSuffix(model.Endpoint, "/")+path, "-H", "Content-Type: application/json", "-d", payload)
}

func (a *App) quickstartWizardOrka(report func(quickstartSetupEvent)) error {
	report(quickstartSetupEvent{step: 5, status: "active"})
	err := a.stepQuickstartOrka()
	status := "done"
	if err != nil {
		status = "failed"
	}
	report(quickstartSetupEvent{step: 5, status: status, err: err})
	return err
}

type quickstartWizardModel struct {
	create                   createWizardModel
	events                   <-chan quickstartSetupEvent
	models                   []localModel
	defaultModel             localModel
	modelPick                chan<- *localModel
	cancelSetup              context.CancelFunc
	modelStep                bool
	detectDone               bool
	selection                int
	chosen                   *localModel
	existing                 []quickstartExistingAgent
	imported                 *quickstartExistingAgent
	agentStep                bool
	setup                    [6]string
	setupErr                 error
	setupDone                bool
	formDone                 bool
	frame                    int
	width                    int
	height                   int
	target                   quickstartTarget
	setupNote                string
	inferenceNote            string
	quitting                 bool
	nameCheck                quickstartNameCheck
	nameContext              context.Context
	nameCancel               context.CancelFunc
	nameRevision             int
	nameValue, nameNamespace string
	namePending, nameChecked bool
	nameErr                  error
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
	if key, ok := msg.(tea.KeyPressMsg); ok && (key.String() == "ctrl+c" || key.Code == tea.KeyEsc) {
		msg = createWizardCancelMsg{}
	}
	switch msg := msg.(type) {
	case quickstartNameDebounce:
		if !m.currentNameCheck(msg) {
			return m, nil
		}
		parent := m.nameContext
		if parent == nil {
			parent = context.Background()
		}
		ctx, cancel := context.WithTimeout(parent, 5*time.Second)
		m.nameCancel = cancel
		check := m.nameCheck
		return m, func() tea.Msg { defer cancel(); return quickstartNameResult{msg, check(ctx, msg.namespace, msg.name)} }
	case quickstartNameResult:
		if !m.currentNameCheck(msg.quickstartNameDebounce) {
			return m, nil
		}
		m.namePending, m.nameChecked, m.nameErr = false, true, msg.err
		return m, nil
	case createWizardCancelMsg:
		if m.nameCancel != nil {
			m.nameCancel()
		}
		m.create.cancelled, m.formDone, m.agentStep, m.modelStep = true, true, false, false
		if m.cancelSetup != nil {
			m.cancelSetup()
		}
		m.quitting = true
		return m, tea.Quit
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		// Keep the source input resize path without the retired live-create model.
		width := max(24, min(96, msg.Width-2)-10)
		m.create.input.SetWidth(max(20, min(60, width-4)))
		return m, nil
	case quickstartTickMsg:
		m.frame++
		return m, quickstartTick()
	case quickstartSetupEvent:
		if msg.step < 0 {
			m.setupDone = true
			if m.formDone || m.setupErr != nil {
				m.quitting = true
				return m, tea.Quit
			}
			return m, nil
		}
		if msg.step < len(m.setup) {
			if msg.status == "active" {
				m.frame = 0
			}
			m.setup[msg.step] = msg.status
		}
		if msg.note != "" {
			m.setupNote = msg.note
			if msg.step == 0 {
				m.inferenceNote = msg.note
			}
		}
		if msg.err != nil {
			m.setupErr = msg.err
			m.quitting = true
			return m, tea.Quit
		}
		if msg.model != nil {
			choice := *msg.model
			if m.chosen == nil || m.chosen.Provider != "copilot" {
				m.chosen = &choice
			}
			m.create.opt.Model = choice.Model
		}
		if msg.models != nil {
			m.models = msg.models
			m.detectDone = true
			m.filterInferenceChoices()
		}
		if m.formDone && m.setupErr == nil && m.setupComplete() {
			m.setupDone = true
			m.quitting = true
			return m, tea.Quit
		}
		return m, waitQuickstartEvent(m.events)
	case tea.KeyPressMsg:
		if !m.agentStep && !m.modelStep && m.create.step == createName && m.nameCheck != nil && msg.Code == tea.KeyEnter {
			if !m.nameChecked || m.nameErr != nil {
				if m.nameErr != nil {
					m.nameValue = ""
					return m, m.scheduleNameCheck()
				}
				return m, nil
			}
		}
		if m.agentStep {
			switch msg.Code {
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
					m.create.err = nil
					m.create.step = createDone
					m.filterInferenceChoices()
					return m, nil
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
			case tea.KeyUp, tea.KeyLeft, 'k':
				m.selection = (m.selection - 1 + len(m.models)) % len(m.models)
			case tea.KeyDown, tea.KeyRight, tea.KeyTab, 'j':
				m.selection = (m.selection + 1) % len(m.models)
			case tea.KeyEnter:
				m.chooseModel(m.models[m.selection])
				check := m.scheduleNameCheck()
				return m, tea.Batch(m.create.Init(), check)
			}
			return m, nil
		}
		if m.formDone {
			return m, nil
		}
	}
	// Only the visible form owns text input and cursor animation.
	if m.agentStep || m.modelStep || m.formDone {
		return m, nil
	}

	updated, cmd := updateQuickstartSourceForm(m.create, msg)
	m.create = updated
	if m.create.step == createName {
		m.create.input.Validate = func(value string) error {
			if err := validateAgentName(value); err != nil {
				return err
			}
			if m.nameCheck != nil {
				return nil
			}
			for _, agent := range m.existing {
				if agent.Name == strings.TrimSpace(value) && agent.Namespace == m.create.opt.Namespace {
					return fmt.Errorf("Agent %q already exists in %s; choose a different name, or restart and select Use existing Agent", agent.Name, agent.Namespace)
				}
			}
			return nil
		}
		if check := m.scheduleNameCheck(); check != nil {
			cmd = tea.Batch(cmd, check)
		}
	}
	if m.create.step == createDone {
		m.formDone = true
		if m.create.cancelled || m.create.err != nil {
			if m.cancelSetup != nil {
				m.cancelSetup()
			}
			m.quitting = true
			return m, tea.Quit
		}
		if m.setupDone || m.setupErr != nil {
			m.quitting = true
			return m, tea.Quit
		}
		return m, nil
	}
	return m, cmd
}

func (m *quickstartWizardModel) chooseModel(choice localModel) {
	m.chosen = &choice
	if choice.Provider != "copilot" && choice.Provider != "existing" && choice.Provider != "foundry" {
		m.create.opt.Model = choice.Model
	}
	m.modelPick <- m.chosen
	m.modelStep = false
	if m.imported != nil {
		m.formDone = true
		return
	}
	startQuickstartSourceField(&m.create)
}

func (m *quickstartWizardModel) filterInferenceChoices() {
	if !m.detectDone {
		return
	}
	var choices []localModel
	if m.imported != nil {
		choices = append(choices, localModel{Provider: "existing", Model: "Current Agent Provider"})
	}
	for _, model := range m.models {
		if m.imported == nil && model.Provider != "foundry" && model.Provider != "copilot" {
			choices = append(choices, model)
		}
	}
	m.models = choices
}

func (m quickstartWizardModel) View() tea.View {
	width := m.width
	if width <= 0 {
		width = 88
	}
	width = max(8, width)
	title := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Cyan).
		Render("KMX  /  QUICKSTART WIZARD")
	height := m.height
	if height <= 0 {
		height = 24
	}
	lines := []string{ansi.Truncate(title, width, ""), m.compactInfrastructure(width), m.compactTarget(width)}
	lines = append(lines, m.compactQuestion(width, max(1, height-4)))
	lines = append(lines, ansi.Truncate("enter continue · ↑/↓ select · esc/ctrl+c cancel", width, ""))
	view := strings.Join(lines, "\n")
	rendered := tea.NewView(view)
	rendered.AltScreen = true
	return rendered
}

func (m quickstartWizardModel) compactInfrastructure(width int) string {
	labels := []string{"Detect models", "Kind cluster", "Model runtime", "Download model", "Load model", "Orka"}
	if m.chosen != nil && m.chosen.Provider == "copilot" {
		labels[2], labels[3], labels[4] = "Configuring model", "Configuring model", "Configuring model"
	}
	completed, current := 0, -1
	for i, status := range m.setup {
		if status == "done" || status == "skipped" {
			completed++
			continue
		}
		if current < 0 {
			current = i
		}
	}
	text := "Setup 6/6 ✓ ready"
	if current >= 0 {
		status := m.setup[current]
		if status == "" {
			status = "pending"
		}
		marker := "·"
		if status == "active" {
			marker = []string{"⠋", "⠙", "⠹", "⠸"}[m.frame%4]
		}
		if status == "waiting" {
			labels[current] = "Awaiting your choice"
		}
		text = fmt.Sprintf("Setup %d/6 %s %s · %s", completed, marker, labels[current], status)
	}
	return ansi.Truncate(text, width, "…")
}

func (m quickstartWizardModel) compactTarget(width int) string {
	text := m.target.Context
	if text == "" {
		text = "Local setup"
	}
	if m.target.Posture != "" {
		text += " · " + m.target.Posture
	}
	if strings.Contains(m.inferenceNote, "Copilot CLI installed") {
		status := "status unavailable"
		if strings.Contains(m.inferenceNote, "not logged in") {
			status = "not logged in"
		} else if strings.Contains(m.inferenceNote, "logged in") {
			status = "logged in"
		}
		text += " · Copilot: " + status
	}
	return ansi.Truncate(text, width, "…")
}

// Lists are windowed around the cursor; text fields retain their editable row.
func (m quickstartWizardModel) compactQuestion(width, height int) string {
	inner := max(1, width-6)
	title := "Agent details"
	var rows []string
	var choices []string
	selection := m.selection
	switch {
	case m.agentStep:
		title = "1/4 · Start with"
		choices = append(choices, "Author a new suite workspace")
		for _, a := range m.existing {
			label := "Use existing Agent " + a.Name
			if width < 60 {
				label = a.Name + " (existing)"
			}
			choices = append(choices, label)
		}
	case m.modelStep:
		title = "2/4 · Inference Provider"
		if !m.detectDone {
			rows = []string{"Detecting models before offering choices…"}
		} else {
			for _, model := range m.models {
				choices = append(choices, quickstartProviderLabel(model))
			}
		}
	case m.formDone:
		title = "Agent queued"
		rows = []string{m.create.opt.Name, "Waiting for infrastructure…"}
		if m.setupErr != nil {
			rows = []string{"Setup failed; restoring terminal."}
		}
	case m.create.step == createConfirm:
		title = "4/4 · Review"
		o := m.create.opt
		rows = []string{o.Name + " · " + o.Namespace, "Exact model: " + o.Model, "Instructions: " + valueOr(o.Instructions, "generated from purpose"), "Source and every scaffold case are reviewed next.", "No evaluation or placement consent yet."}
		selection = m.create.selection
		choices = []string{"Author source", "Cancel"}
	default:
		labels := map[createWizardStep]string{createDescription: "Description", createName: "Agent name", createNamespace: "Namespace", createProviderType: "Provider type", createModel: "Model", createSecret: "Secret name", createResultAccount: "Result ServiceAccount", quickstartInstructionsPurpose: "Instructions purpose"}
		title = "3/4 · " + labels[m.create.step]
		input := m.create.input
		input.SetWidth(max(1, inner-2))
		rows = []string{input.View()}
		if m.create.step == createName && m.nameCheck != nil {
			if m.nameErr != nil {
				rows = append(rows, m.nameErr.Error())
			} else if m.namePending {
				rows = append(rows, "Checking name…")
			} else if m.nameChecked {
				rows = append(rows, "Name available")
			}
		}
		if m.create.err != nil && m.create.input.Value() != "" {
			rows = append(rows, m.create.err.Error())
		}
	}
	budget := max(1, height-3)
	if len(choices) > 0 {
		description := ""
		if m.modelStep && m.detectDone && selection < len(m.models) && budget >= 3 {
			description = quickstartProviderDescription(m.models[selection])
		}
		if len(rows) > max(0, budget-2) {
			rows = rows[:max(0, budget-2)]
		}
		count := max(1, budget-len(rows))
		if description != "" {
			count = max(1, count-1)
		}
		start := max(0, min(selection-count/2, len(choices)-count))
		for i := start; i < min(len(choices), start+count); i++ {
			prefix := "  "
			if i == selection {
				prefix = "› "
			}
			row := ansi.Truncate(prefix+choices[i], inner, "…")
			if i == selection {
				row = pickerSelectedStyle().Width(inner).Render(row)
			}
			rows = append(rows, row)
		}
		if description != "" {
			rows = append(rows, lipgloss.NewStyle().Foreground(lipgloss.BrightBlack).Render(ansi.Truncate(description, inner, "…")))
		}
	}
	if len(rows) > budget {
		rows = rows[:budget]
	}
	for i, row := range rows {
		rows[i] = ansi.Truncate(strings.ReplaceAll(row, "\n", " "), inner, "…")
	}
	body := ansi.Truncate(title, inner, "…") + "\n" + strings.Join(rows, "\n")
	if height < 4 {
		return ansi.Truncate(title+": "+strings.Join(rows, " "), width, "…")
	}
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Blue).Padding(0, 1).Width(width - 2).Render(body)
}

func (m quickstartWizardModel) setupComplete() bool {
	for _, status := range m.setup {
		if status != "done" && status != "skipped" {
			return false
		}
	}
	return true
}

func quickstartSize(size int64) string {
	if size >= 1_000_000_000 {
		return fmt.Sprintf("%.1f GB", float64(size)/1_000_000_000)
	}
	return fmt.Sprintf("%.0f MB", float64(size)/1_000_000)
}

func quickstartModelLabel(model localModel) string {
	if model.Provider == "foundry" {
		return "Azure Foundry · Entra login · host execution"
	}
	if model.Provider == "copilot" {
		return "Copilot CLI · " + model.Model
	}
	if model.Provider == "existing" {
		return "Orka · current Agent Provider"
	}
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

func quickstartProviderLabel(model localModel) string {
	switch model.Provider {
	case "foundry":
		return "Azure Foundry (Azure login)"
	case "bundled":
		return "Local Orka Model (available to install)"
	case "copilot":
		return "Copilot CLI - auto (detected)"
	case "existing":
		return "Local Orka Model (detected)"
	default:
		return "(detected) " + quickstartModelLabel(model)
	}
}

func quickstartProviderDescription(model localModel) string {
	switch model.Provider {
	case "foundry":
		return "Hosted model · local HTTP tools · no API key or model download"
	case "bundled":
		if model.Model == "qwen2.5:3b" {
			return model.Model + " · Ollama (requires ~1.9 GB download if not installed)"
		}
		return model.Model + " · Ollama (requires model download if not installed)"
	case "existing":
		return "Uses this Agent's configured model and endpoint; no new model download"
	case "copilot":
		return "Copilot CLI · auto model · no local model download"
	default:
		return quickstartModelLabel(model)
	}
}

func quickstartProgressBar(status string, frame int) string {
	const width = 18
	switch status {
	case "done":
		return "[" + strings.Repeat("=", width) + "]"
	case "failed":
		return "[" + strings.Repeat("!", width) + "]"
	case "skipped":
		return "[" + strings.Repeat("-", width) + "]"
	case "waiting":
		return "[" + strings.Repeat(" ", width) + "]"
	case "active":
		position := frame % (width - 3)
		return "[" + strings.Repeat(" ", position) + "====" + strings.Repeat(" ", width-position-4) + "]"
	default:
		return "[" + strings.Repeat(".", width) + "]"
	}
}

func runQuickstartWizard(in io.Reader, out io.Writer, opt CreateOptions, existing []quickstartExistingAgent, target quickstartTarget, defaultModel localModel, modelPick chan<- *localModel, cancelSetup context.CancelFunc, setup func(func(quickstartSetupEvent)) error, checks ...quickstartNameCheck) (CreateOptions, *quickstartExistingAgent, bool, error, error) {
	create, err := newQuickstartSourceForm(opt)
	if err != nil {
		return opt, nil, false, nil, err
	}
	events := make(chan quickstartSetupEvent, 12)
	setupResult := make(chan error, 1)
	stopped := make(chan struct{})
	go func() {
		setupResult <- setup(func(event quickstartSetupEvent) {
			select {
			case events <- event:
			case <-stopped:
			}
		})
		close(events)
	}()
	model := quickstartWizardModel{create: create, events: events, existing: existing, target: target, defaultModel: defaultModel, modelPick: modelPick, cancelSetup: cancelSetup, modelStep: true, agentStep: true}
	model.selection = quickstartInitialAgentSelection(existing)
	nameCtx, cancelNames := context.WithCancel(context.Background())
	defer cancelNames()
	model.nameContext = nameCtx
	if len(checks) > 0 {
		model.nameCheck = checks[0]
	}
	result, runErr := tea.NewProgram(model, tea.WithInput(in), tea.WithOutput(out), tea.WithFilter(cancelQuickstartWizard)).Run()
	// A terminal startup/read failure must also release every background owner.
	cancelSetup()
	close(stopped)
	setupErr := <-setupResult
	if runErr != nil {
		return opt, nil, false, setupErr, runErr
	}
	completed, ok := result.(quickstartWizardModel)
	if !ok {
		return opt, nil, false, setupErr, fmt.Errorf("quickstart wizard returned unexpected model %T", result)
	}
	if !completed.create.cancelled && completed.create.err != nil {
		return opt, nil, false, setupErr, completed.create.err
	}
	return completed.create.opt, completed.imported, completed.create.cancelled, setupErr, nil
}

// Reruns should reconnect to the quickstart agent rather than creating a second
// bundle with its name. Creating another agent remains an explicit choice.
func quickstartInitialAgentSelection(existing []quickstartExistingAgent) int {
	for i, agent := range existing {
		if agent.Name == "hello-world-agent" {
			return i + 1
		}
	}
	if len(existing) > 0 {
		return 1
	}
	return 0
}

func cancelQuickstartWizard(model tea.Model, msg tea.Msg) tea.Msg {
	switch msg.(type) {
	case tea.InterruptMsg:
		return createWizardCancelMsg{}
	case tea.QuitMsg:
		quitting := false
		switch m := model.(type) {
		case quickstartWizardModel:
			quitting = m.quitting
		case quickstartReadyModel:
			quitting = m.accepted || m.cancelled
		}
		if !quitting {
			return createWizardCancelMsg{}
		}
	}
	return msg
}

type quickstartReadyModel struct {
	name      string
	location  string
	selection int
	width     int
	height    int
	accepted  bool
	cancelled bool
}

func (m quickstartReadyModel) Init() tea.Cmd { return nil }

func (m quickstartReadyModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyPressMsg); ok && (key.String() == "ctrl+c" || key.Code == tea.KeyEsc) {
		msg = createWizardCancelMsg{}
	}
	if _, ok := msg.(createWizardCancelMsg); ok {
		m.cancelled = true
		return m, tea.Quit
	}
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		m.width = size.Width
		m.height = size.Height
		return m, nil
	}
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch key.Code {
		case tea.KeyLeft, tea.KeyRight, tea.KeyUp, tea.KeyDown, tea.KeyTab, 'j', 'k':
			m.selection = 1 - m.selection
		case tea.KeyEnter:
			m.accepted = true
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
	width = max(8, width)
	inner := max(1, min(80, width)-4)
	choices := []string{"Chat with agent", "Finish"}
	location := m.location
	if location == "" {
		location = "current cluster"
	}
	header := agentConnectionHeader(m.name, location, "Ready · setup complete (3/3)", width)
	rows := []string{lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Blue).Render("NEXT STEP")}
	for i, choice := range choices {
		prefix := "  "
		if i == m.selection {
			prefix = "› "
		}
		row := ansi.Truncate(prefix+choice, inner, "…")
		if i == m.selection {
			row = pickerSelectedStyle().Width(inner).Render(row)
		}
		rows = append(rows, row)
	}
	border := lipgloss.NewStyle().Foreground(lipgloss.Blue)
	panel := border.Render("╭" + strings.Repeat("─", inner+2) + "╮")
	for _, row := range rows {
		panel += "\n" + border.Render("│") + " " + row + strings.Repeat(" ", max(0, inner-lipgloss.Width(row))) + " " + border.Render("│")
	}
	panel += "\n" + border.Render("╰"+strings.Repeat("─", inner+2)+"╯")
	rendered := tea.NewView(header + "\n" + panel + "\n" + ansi.Truncate("enter choose · ↑/↓ j/k · esc/ctrl+c finish", width, ""))
	rendered.AltScreen = true
	return rendered
}

func runQuickstartReadyScreen(in io.Reader, out io.Writer, name string, locations ...string) (bool, error) {
	m := quickstartReadyModel{name: name}
	if len(locations) > 0 {
		m.location = locations[0]
	}
	result, err := tea.NewProgram(m, tea.WithInput(in), tea.WithOutput(out), tea.WithFilter(cancelQuickstartWizard)).Run()
	if err != nil {
		return false, err
	}
	m = result.(quickstartReadyModel)
	return m.accepted && !m.cancelled && m.selection == 0, nil
}

func (a *App) quickstartOrkaChat(agent, namespace string) error {
	worker := *a
	return worker.chatWithPreparedEnforcement(
		ChatOptions{Agent: agent, Namespace: namespace, Runtime: "orka", Verbose: a.chatVerbose},
		a.taskEnforcement)
}
