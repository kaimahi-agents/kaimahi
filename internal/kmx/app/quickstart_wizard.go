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
	"github.com/charmbracelet/x/ansi"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/cliui"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/guard"
)

// QuickstartWizardOptions configures the experimental path from an empty
// machine to a user-authored Orka agent.
type QuickstartWizardOptions struct {
	AzureDiscovery string
	Create         CreateOptions
	Verbose        bool
	Inference      string
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
	switch opt.AzureDiscovery {
	case "", "cli", "sdk":
	default:
		return fmt.Errorf("unknown Azure discovery %q; use cli or sdk", opt.AzureDiscovery)
	}
	a.azureDiscoveryMode = opt.AzureDiscovery
	if _, err := quickstartInference(opt.Inference, "detected-later"); err != nil {
		return err
	}
	a.chatVerbose = opt.Verbose
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
	quickstartAgentTools(&create)
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
	a.copilotCLI = setup.copilotCLI
	a.chatInference, a.copilotModel = setup.chatInference, setup.copilotModel
	if a.chatInference == "foundry" {
		backend := &orkaChatBackend{app: a, agent: completed.Name, namespace: completed.Namespace}
		if err := backend.configureFoundryChat(a.operationContext()); err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}
		// The persisted Provider is a valid deferred local fallback. Foundry
		// execution uses the host credential/client, not this placeholder key.
		completed.Model = a.Cfg.Model
		completed.BaseURL = "http://ollama.ollama.svc.cluster.local:11434/v1"
	}
	deploy := func(worker *App) error { return worker.CreateAgent(completed) }
	if imported != nil {
		deploy = func(worker *App) error { return worker.attachQuickstartK8sTool(completed.Name, completed.Namespace) }
	}
	if err := a.runQuickstartDeploymentWork(completed.Name, deploy); err != nil {
		if errors.Is(err, errCreateCancelled) {
			a.notef("Quickstart cancelled. Active deployment work was stopped; completed resources were left in place.")
			return nil
		}
		return err
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
	note   string
}

func (a *App) quickstartWizardModels() []localModel {
	models := []localModel{{Provider: "bundled", Model: a.Cfg.Model}, {Provider: "foundry", Model: "Azure Foundry"}}
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
	a.copilotCLI = detectCopilotCLI()
	models := a.quickstartWizardModels()
	if err := a.operationContext().Err(); err != nil {
		return err
	}
	note := "No Copilot CLI detected; chat will use the local Orka model."
	if a.copilotCLI != "" {
		login := copilotLoginStatus(a.operationContext(), a.copilotCLI)
		note = "Copilot CLI installed · " + login
		if login == "logged in" {
			models = append(models, localModel{Provider: "copilot", Model: "auto"})
			note += " · auto model"
		}
	}
	report(quickstartSetupEvent{step: 0, status: "done", models: models, note: note})
	if err := run(1, a.stepCluster); err != nil {
		return err
	}
	report(quickstartSetupEvent{step: 2, status: "waiting"})
	var choice *localModel
	select {
	case <-a.operationContext().Done():
		return a.operationContext().Err()
	case choice = <-modelPick:
	}
	if choice == nil {
		return fmt.Errorf("inference selection is required")
	}
	a.chatInference = "local"
	if choice.Provider == "foundry" {
		a.chatInference = "foundry"
		for step := 2; step <= 4; step++ {
			report(quickstartSetupEvent{step: step, status: "skipped", note: "Foundry hosted inference; no local model download. Configure Azure login after setup."})
		}
		return a.quickstartWizardOrka(report)
	}
	if choice.Provider == "copilot" {
		a.chatInference, a.copilotModel = "copilot", choice.Model
		// Local resources remain available for an explicit comparison later.
		choice = &localModel{Provider: "bundled", Model: a.Cfg.Model}
	}
	if choice.Provider == "existing" {
		for step := 2; step <= 4; step++ {
			report(quickstartSetupEvent{step: step, status: "skipped", note: "Using the existing Agent Provider."})
		}
		return a.quickstartWizardOrka(report)
	}
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
			// The runtime's own grant, not a second spelling of it: a wizard
			// copy would be free to drift from the account `kmx up --step orka`
			// provisions and the one `agent create` names, and a Role that
			// differs by path is a grant nobody reviews as one.
			err = a.orkaResultReader()
		}
		if err == nil {
			err = a.installQuickstartK8sTool()
		}
		status := "done"
		if err != nil {
			status = "failed"
		}
		report(quickstartSetupEvent{step: 5, status: status, err: err})
		return err
	}()
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
		updated, _ := m.create.Update(tea.WindowSizeMsg{Width: max(24, min(96, msg.Width-2)-10), Height: msg.Height})
		m.create = updated.(createWizardModel)
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
			m.create.opt.BaseURL = strings.TrimSuffix(choice.Endpoint, "/") + "/v1"
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

	updated, cmd := m.create.Update(msg)
	m.create = updated.(createWizardModel)
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
		if choice.Endpoint != "" {
			m.create.opt.BaseURL = strings.TrimSuffix(choice.Endpoint, "/") + "/v1"
		}
	}
	if choice.Provider == "copilot" || choice.Provider == "foundry" {
		m.create.opt.Model = m.defaultModel.Model
		m.create.opt.BaseURL = m.defaultModel.Endpoint + "/v1"
	}
	if choice.Provider == "bundled" {
		m.create.opt.BaseURL = "http://ollama.ollama.svc.cluster.local:11434/v1"
	}
	m.modelPick <- m.chosen
	m.modelStep = false
	if m.imported != nil {
		m.formDone = true
		return
	}
	m.create.startMissingStep()
}

func (m *quickstartWizardModel) filterInferenceChoices() {
	if !m.detectDone {
		return
	}
	var choices []localModel
	if m.imported != nil {
		choices = append(choices, localModel{Provider: "existing", Model: "Current Agent Provider"})
	}
	copilotAdded := false
	for _, model := range m.models {
		if model.Provider == "copilot" {
			if !copilotAdded {
				choices = append([]localModel{{Provider: "copilot", Model: "auto"}}, choices...)
				copilotAdded = true
			}
		} else if m.imported == nil || model.Provider == "foundry" {
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
	labels := []string{"Detect models", "Kind cluster", "Model runtime", "Download model", "Load model", "Orka + tools"}
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
		choices = append(choices, "Create a new agent")
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
		rows = []string{o.Name + " · " + o.Namespace, o.ProviderType + " · " + o.Model, "Endpoint: " + o.BaseURL, "Tools: " + o.Tools, "Secret: " + o.Secret, "Output: " + o.Out}
		selection = m.create.selection
		choices = []string{"Apply", "Cancel"}
	default:
		labels := map[createWizardStep]string{createDescription: "Description", createName: "Agent name", createNamespace: "Namespace", createProviderType: "Provider type", createModel: "Model", createSecret: "Secret name", createResultAccount: "Result ServiceAccount"}
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
	modelLabel := "Model download"
	if m.chosen != nil {
		modelLabel = "Model " + m.chosen.Model
	}
	labels := []string{"Detecting models", "Kind cluster", "Model runtime", modelLabel, "Loading model", "Orka runtime"}
	if m.chosen != nil && m.chosen.Provider == "copilot" {
		labels[2], labels[3], labels[4] = "Configuring model", "Configuring model", "Configuring model"
	}
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
	body.WriteString("\n\nInfrastructure runs top to bottom while you prepare the agent.")
	if m.setup[2] == "waiting" {
		body.WriteString("\nWaiting for your agent/model choice before continuing setup.")
	}
	if m.setupNote != "" {
		body.WriteString("\n" + m.setupNote)
	}
	if m.inferenceNote != "" && m.inferenceNote != m.setupNote {
		body.WriteString("\n" + m.inferenceNote)
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
	stages := []string{"Agent source", "Model discovery & choice", "Agent details", "Review & continue"}
	stepLabel := lipgloss.NewStyle().Foreground(lipgloss.Blue).Bold(true).Render(fmt.Sprintf("STAGE %d OF 4 · %s", step, stages[step-1]))
	body.WriteString(stepLabel + "\n\n")
	if m.agentStep {
		body.WriteString(lipgloss.NewStyle().Bold(true).Render("Start with") + "\n\n")
		choices := []string{"Create a new agent"}
		for _, agent := range m.existing {
			choices = append(choices, fmt.Sprintf("Use existing Agent %q (%s)", agent.Name, agent.Namespace))
		}
		body.WriteString(quickstartChoices(choices, m.selection))
		body.WriteString("\n\n" + lipgloss.NewStyle().Foreground(lipgloss.BrightBlack).Render("enter choose  •  arrows/j/k select"))
	} else if m.modelStep {
		if !m.detectDone {
			body.WriteString(lipgloss.NewStyle().Bold(true).Render("Detecting models") + "\n\n")
			body.WriteString(lipgloss.NewStyle().Foreground(lipgloss.BrightBlack).Render("Checking local model runtimes before offering choices..."))
		} else {
			body.WriteString(lipgloss.NewStyle().Bold(true).Render("Inference Provider") + "\n\n")
			choices := make([]string, 0, len(m.models))
			for _, model := range m.models {
				choices = append(choices, quickstartProviderLabel(model))
			}
			body.WriteString(quickstartChoices(choices, m.selection))
			if m.selection < len(m.models) {
				body.WriteString("\n" + quickstartProviderDescription(m.models[m.selection]))
			}
			body.WriteString("\n\n" + lipgloss.NewStyle().Foreground(lipgloss.BrightBlack).Render("enter choose  •  arrows/j/k select"))
		}
	} else if m.formDone {
		if m.setupErr != nil {
			body.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Red).Render("Infrastructure setup failed. Restoring the terminal for diagnostics."))
		} else if m.imported != nil {
			body.WriteString(fmt.Sprintf("Using existing Agent %q. Waiting for infrastructure before continuing...", m.imported.Name))
		} else {
			body.WriteString(fmt.Sprintf("Agent %q is queued. Waiting for the model and runtime before deployment...", m.create.opt.Name))
		}
	} else {
		inner := m.create.View().Content
		if _, content, ok := strings.Cut(inner, "\n\n"); ok {
			inner = content
		}
		body.WriteString(inner)
	}
	if m.chosen != nil && !m.modelStep {
		fmt.Fprintf(&body, "\n\n%s %s", lipgloss.NewStyle().Foreground(lipgloss.BrightBlack).Render("MODEL"), m.quickstartModelChoiceLabel(*m.chosen))
	}
	if m.formDone {
		heading := color.Color(lipgloss.Blue)
		if m.setupErr != nil {
			heading = lipgloss.Red
		}
		return quickstartSection("AGENT SETUP", body.String(), width, heading)
	}
	return quickstartPanel("AGENT SETUP", body.String(), width, lipgloss.Blue, lipgloss.Blue)
}

func (m quickstartWizardModel) setupComplete() bool {
	for _, status := range m.setup {
		if status != "done" && status != "skipped" {
			return false
		}
	}
	return true
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
			body.WriteString(pickerSelectedStyle().Render("› " + choice))
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
		return "host runtimes"
	case 1:
		return "~1.3 GB node image"
	case 2:
		if m.chosen != nil && m.chosen.Provider != "bundled" {
			return "reuse host runtime"
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

func quickstartInteractiveStep(step createWizardStep) int {
	switch step {
	case createConfirm, createDone:
		return 4
	default:
		return 3
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
	create, err := newCreateWizardModel(opt)
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
		case quickstartDeployModel:
			quitting = m.done || m.cancelled
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
	return a.ChatWithOptions(ChatOptions{Agent: agent, Namespace: namespace, Runtime: "orka", Interactive: true, Verbose: a.chatVerbose, AzureDiscovery: a.azureDiscoveryMode})
}

type orkaChatBackend struct {
	liftHeader       *liftHeader
	azureDiscovery   *azureSDKDiscovery
	app              *App
	agent, namespace string
	chatContext      context.Context
	toolConnections  map[string]*copilotToolConnection
	resultSession    *orkaResultSession
}

func (b *orkaChatBackend) Agent() string { return b.agent }

func (b *orkaChatBackend) Connect(ctx context.Context, renderer *chatRenderer) ([]cliui.Field, error) {
	b.chatContext = ctx
	location, err := b.app.chatLocation(ctx)
	if err != nil {
		return nil, err
	}
	tools, err := b.enabledToolsSummary(ctx)
	if err != nil {
		return nil, err
	}
	renderer.statusStart(b.agent, b.app.Cfg.KubeContext)
	runtime, tasks := "Orka Task worker Jobs", "Each message creates one fresh local Task"
	if b.app.chatInference == "copilot" || b.app.chatInference == "foundry" {
		runtime, tasks = "Host Copilot CLI · "+b.app.copilotModel, "Copilot prompts with KMX tool execution; no Orka Task created"
		if b.app.chatInference == "foundry" {
			runtime, tasks = "Host Foundry client · Entra login", "Native model tool calls with KMX HTTP tool execution; no Orka Task created"
		}
		supported, _, err := b.copilotTools(ctx)
		if err != nil {
			return nil, err
		}
		names := make([]string, 0, len(supported))
		for _, tool := range supported {
			names = append(names, tool.Name)
		}
		tools = fmt.Sprintf("(%d tools enabled via host adapter) %s", len(names), strings.Join(names, ", "))
	}
	return []cliui.Field{
		{Label: "Location", Value: location},
		{Label: "Deployment", Value: "Orka Agent/" + b.agent + " | namespace " + b.namespace},
		{Label: "Runtime", Value: runtime},
		{Label: "Tasks", Value: tasks},
		{Label: "Tools", Value: tools},
		{Label: "Inference", Value: b.inferenceLabel()},
	}, nil
}

func (b *orkaChatBackend) Send(ctx context.Context, message string, renderer *chatRenderer) error {
	renderer.beginAssistant(b.agent)
	inference, host, err := b.app.hostInferenceStrategy()
	if err != nil {
		return err
	}
	if host {
		return b.sendHostTurn(ctx, message, renderer, inference)
	}
	var profile *orkaTaskProfile
	if renderer.verboseEnabled() {
		profile = &orkaTaskProfile{}
	}
	if b.resultSession != nil {
		deadline, _ := b.resultSession.ctx.Deadline()
		if b.resultSession.ctx.Err() != nil || time.Until(deadline) < 5*time.Minute {
			b.resultSession.close()
			b.resultSession = nil
		}
	}
	sessionStarted := time.Now()
	openedSession := false
	if b.resultSession == nil {
		parent := b.chatContext
		if parent == nil {
			parent = ctx
		}
		sessionCtx, cancel := context.WithTimeout(parent, 8*time.Minute)
		quiet := *b.app
		quiet.Err = io.Discard
		session, err := quiet.openOrkaResultSession(sessionCtx, CreateOptions{Namespace: b.namespace, ResultServiceAccount: "orka-result-reader", OrkaAPIService: "orka-api", ResultPort: "19180"})
		if err != nil {
			cancel()
			return err
		}
		context.AfterFunc(session.ctx, cancel)
		b.resultSession = session
		openedSession = true
	}
	sessionTime := time.Duration(0)
	if openedSession {
		sessionTime = time.Since(sessionStarted)
	}
	answer, err := b.app.runQuickstartOrkaTaskProfile(ctx, b.agent, b.namespace, message, profile, func(phase string) {
		renderer.assistantOperation(b.agent, "WORKING", "", colorBlue, phase)
	}, b.resultSession)
	if profile != nil {
		profile.session += sessionTime
	}
	if err != nil {
		return err
	}
	renderer.assistant(b.agent, answer, true)
	if profile != nil {
		renderer.assistantOperation(b.agent, "TIMING", "", colorBlue, profile.summary())
	}
	return nil
}

func (a *App) runQuickstartOrkaTaskProfile(parent context.Context, agent, namespace, prompt string, profile *orkaTaskProfile, report func(string), reuse ...*orkaResultSession) (string, error) {
	phase := func(label string) {
		if report != nil {
			report(label)
		}
	}
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
	phase("Opening result connection")
	started := time.Now()
	var session *orkaResultSession
	if len(reuse) > 0 {
		session = reuse[0]
	} else {
		session, err = quiet.openOrkaResultSession(ctx, opt)
		if err != nil {
			return "", err
		}
		defer session.close()
	}
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	unhook := context.AfterFunc(session.ctx, stop)
	defer unhook()
	if profile != nil {
		profile.session = time.Since(started)
	}
	phase("Creating Task")
	started = time.Now()
	id, err := a.createOrkaObject(ctx, namespace, doc)
	if err != nil {
		return "", err
	}
	if profile != nil {
		profile.create = time.Since(started)
	}
	phase("Task " + id.Name + ": waiting for worker execution and completion")
	started = time.Now()
	answer, err := a.waitOrkaTaskResultProgress(ctx, namespace, id, session, func() {
		if profile != nil {
			profile.execution = time.Since(started)
		}
		started = time.Now()
		phase("Retrieving result")
	})
	if err == nil && profile != nil {
		profile.result = time.Since(started)
		started = time.Now()
		profile.loadEvents(session.ctx, session, namespace, id.Name)
		profile.telemetry = time.Since(started)
	}
	return answer, err
}

type quickstartDeployDone struct{ err error }
type quickstartDeployModel struct {
	name      string
	done      bool
	err       error
	frame     int
	cancel    context.CancelFunc
	cancelled bool
}

func (m quickstartDeployModel) Init() tea.Cmd { return quickstartTick() }
func (m quickstartDeployModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyPressMsg); ok && (key.String() == "ctrl+c" || key.Code == tea.KeyEsc) {
		msg = createWizardCancelMsg{}
	}
	switch msg := msg.(type) {
	case createWizardCancelMsg:
		m.cancelled = true
		if m.cancel != nil {
			m.cancel()
		}
		return m, tea.Quit
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
	heading := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Cyan).Render("KMX  /  QUICKSTART WIZARD")
	rendered := tea.NewView(heading + fmt.Sprintf("\n\nPHASE 2 OF 3 · Deploy agent\n\nDeploy Agent %q (Orka agent)\n\n  Provider → Agent %s %s\n\nEach resource must become Ready before the next is created.\n\nesc / ctrl+c cancel", m.name, quickstartBarStyle(status).Render(quickstartProgressBar(status, m.frame)), quickstartStatusStyle(status).Render(status)))
	rendered.AltScreen = true
	return rendered
}

func (a *App) runQuickstartDeployment(opt CreateOptions) error {
	return a.runQuickstartDeploymentWork(opt.Name, func(worker *App) error { return worker.CreateAgent(opt) })
}

func (a *App) runQuickstartDeploymentWork(name string, work func(*App) error) error {
	var log bytes.Buffer
	worker := *a
	runner := *a.Run
	ctx, cancel := context.WithCancel(a.operationContext())
	defer cancel()
	runner.Context = ctx
	runner.Stdout, runner.Stderr = &log, &log
	worker.Run, worker.Out, worker.Err, worker.Stdin = &runner, &log, &log, nil
	done := make(chan error, 1)
	program := tea.NewProgram(quickstartDeployModel{name: name, cancel: cancel}, tea.WithInput(a.Stdin), tea.WithOutput(a.Err), tea.WithFilter(cancelQuickstartWizard))
	go func() {
		err := work(&worker)
		done <- err
		program.Send(quickstartDeployDone{err: err})
	}()
	result, runErr := program.Run()
	cancel()
	deployErr := <-done
	if completed, ok := result.(quickstartDeployModel); ok && completed.cancelled {
		return errCreateCancelled
	}
	if runErr != nil || deployErr != nil {
		if log.Len() > 0 {
			fmt.Fprintln(a.Err, "\nAgent deployment log:")
			_, _ = log.WriteTo(a.Err)
		}
	}
	return errors.Join(runErr, deployErr)
}
