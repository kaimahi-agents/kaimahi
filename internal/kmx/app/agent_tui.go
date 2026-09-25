package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/lift"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

type agentTUIEnvsMsg struct {
	envs      []agentTUIEnvironment
	err       error
	preferred [2]string
}
type agentTUIPreferenceMsg struct{ err error }
type agentTUIInventoryMsg struct {
	column, generation int
	agents             []agentTUIAgent
	err                error
}
type agentTUIAction struct {
	kind           string
	agent          agentTUIAgent
	source, target agentTUIEnvironment
	create         *lift.Options
}
type agentTUISuggestion struct{ value, detail string }
type agentTUIMenuAction struct{ key, label string }

type agentTUIModel struct {
	opt                                          AgentTUIOptions
	columns                                      [2]agentTUIColumn
	envs                                         []agentTUIEnvironment
	focus, width, height, suggestion, generation int
	input                                        textinput.Model
	command, details, help, discovering          bool
	status                                       string
	action                                       *agentTUIAction
	form                                         []string
	formStep                                     int
	actionsOpen                                  bool
	actionSelection                              int
	detailScroll                                 int
	loadEnvs                                     func() tea.Msg
	loadInventory                                func(agentTUIEnvironment) ([]agentTUIAgent, error)
	saveEnvironment                              func(local bool, name string) error
	creation                                     *agentTUICreatePane
	inference                                    *consoleInferencePane
	inferenceContext                             context.Context
	loadAzureInference                           func(context.Context, string, string, string, string) ([]consoleAzureChoice, error)
	loadInference                                func(agentTUIEnvironment, agentTUIAgent) (consoleInferenceSnapshot, error)
	startInference                               func(agentTUIEnvironment, agentTUIAgent, consoleInferenceSnapshot, consoleInferenceSource, string) (<-chan consoleInferenceSaved, context.CancelFunc)
	loadCreateTarget                             func(agentTUIEnvironment) (string, error)
	startCreate                                  func(agentTUIEnvironment, string, CreateOptions) (<-chan agentTUICreateResult, context.CancelFunc)
}

func newAgentTUIModel(opt AgentTUIOptions) agentTUIModel {
	input := textinput.New()
	input.Prompt = ""
	input.CharLimit = 512
	input.SetWidth(76)
	m := agentTUIModel{opt: opt, input: input, width: 100, height: 30, discovering: !opt.Demo, status: "Choose an agent · / for commands"}
	if opt.Demo {
		m.columns = agentTUIDemoColumns()
		m.envs = []agentTUIEnvironment{m.columns[0].Env, m.columns[1].Env}
		m.status = "DEMO · sample data; actions never contact clusters or Azure"
	}
	return m
}

// AgentTUI releases terminal ownership before entering existing chat/lift UIs,
// then restores the dashboard and refreshes both inventories on return.
func (a *App) AgentTUI(opt AgentTUIOptions) error {
	if a.Stdin == nil || !isTerminalFile(a.Stdin) || !isInteractiveTerminal(a.Out) || os.Getenv("TERM") == "dumb" {
		return fmt.Errorf("kmx console requires an interactive terminal on stdin and stdout; use kmx agent list for piped output")
	}
	if opt.Namespace == "" {
		opt.Namespace = OrkaNamespace
	}
	if err := scaffold.ValidateNamespace(opt.Namespace); err != nil {
		return err
	}
	m := newAgentTUIModel(opt)
	m.saveEnvironment = func(local bool, name string) error {
		key := "tui/remote"
		if local {
			key = "tui/local"
		}
		return saveLiftPreference(key, name)
	}
	if !opt.Demo && opt.LocalContext == "" && opt.RemoteContext == "" {
		// Prefer the configured context only in its independently classified column.
		m.status = "Discovering environments · configured context: " + a.Cfg.KubeContext
	}
	for {
		ctx, cancel := context.WithCancel(a.operationContext())
		var workers sync.WaitGroup
		m.inferenceContext = ctx
		m.loadAzureInference = a.consoleAzureChoices
		m.loadInference = func(env agentTUIEnvironment, agent agentTUIAgent) (consoleInferenceSnapshot, error) {
			return a.consoleLoadInference(ctx, env, agent)
		}
		m.startInference = func(env agentTUIEnvironment, agent agentTUIAgent, snapshot consoleInferenceSnapshot, source consoleInferenceSource, model string) (<-chan consoleInferenceSaved, context.CancelFunc) {
			workCtx, stop := context.WithCancel(ctx)
			result := make(chan consoleInferenceSaved, 1)
			workers.Add(1)
			go func() {
				defer workers.Done()
				result <- consoleInferenceSaved{a.consoleSaveInference(workCtx, env, agent, snapshot, source, model)}
			}()
			return result, stop
		}
		m.loadCreateTarget = func(env agentTUIEnvironment) (string, error) { return a.consoleCreateTarget(ctx, env) }
		m.startCreate = func(env agentTUIEnvironment, server string, options CreateOptions) (<-chan agentTUICreateResult, context.CancelFunc) {
			workCtx, stop := context.WithCancel(ctx)
			result := make(chan agentTUICreateResult, 1)
			workers.Add(1)
			go func() {
				defer workers.Done()
				result <- agentTUICreateResult{a.consoleCreateAgent(workCtx, env, server, options)}
			}()
			return result, stop
		}
		m.loadEnvs = func() tea.Msg {
			envs, err := a.agentTUIEnvironments(ctx)
			prefs, prefErr := loadLiftPreferences()
			return agentTUIEnvsMsg{envs: envs, err: errors.Join(err, prefErr), preferred: agentTUIPreferredEnvironments(envs, prefs, a.Cfg.KubeContext)}
		}
		m.loadInventory = func(env agentTUIEnvironment) ([]agentTUIAgent, error) {
			return a.agentTUIInventory(ctx, env, opt.Namespace)
		}
		filter := func(model tea.Model, msg tea.Msg) tea.Msg {
			if _, ok := msg.(tea.InterruptMsg); ok && (model.(agentTUIModel).creation != nil || model.(agentTUIModel).inference != nil) {
				return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
			}
			return msg
		}
		result, err := tea.NewProgram(m, tea.WithInput(a.Stdin), tea.WithOutput(a.Out), tea.WithContext(ctx), tea.WithFilter(filter)).Run()
		cancel()
		workers.Wait()
		if err != nil {
			return err
		}
		m = result.(agentTUIModel)
		if m.action == nil {
			return nil
		}
		action := *m.action
		m.action = nil
		m.status = "Returned from " + action.kind
		target, err := a.runAgentTUIAction(action)
		if err != nil && !errors.Is(err, context.Canceled) {
			m.status = action.kind + ": " + err.Error()
		}
		if target.Name != "" {
			m.opt.RemoteContext = target.Name
			m.columns[1].Env = target
			if err := m.saveEnvironment(false, target.Name); err != nil {
				m.status += "; could not remember environment: " + err.Error()
			}
		}
	}
}

func (m agentTUIModel) Init() tea.Cmd {
	if m.opt.Demo {
		return nil
	}
	return m.loadEnvs
}

func (m *agentTUIModel) refresh() tea.Cmd {
	m.generation++
	var cmds []tea.Cmd
	for i := range m.columns {
		if m.columns[i].Env.Name == "" {
			continue
		}
		m.columns[i].Loading = true
		env, generation, load := m.columns[i].Env, m.generation, m.loadInventory
		cmds = append(cmds, func() tea.Msg {
			agents, err := load(env)
			return agentTUIInventoryMsg{column: i, generation: generation, agents: agents, err: err}
		})
	}
	return tea.Batch(cmds...)
}

func (m agentTUIModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.inference != nil {
		if size, ok := msg.(tea.WindowSizeMsg); ok {
			m.width, m.height = size.Width, size.Height
		}
		switch msg.(type) {
		case agentTUIEnvsMsg, agentTUIInventoryMsg, agentTUIPreferenceMsg:
		default:
			return m.updateInference(msg)
		}
	}
	if m.creation != nil {
		switch event := msg.(type) {
		case tea.WindowSizeMsg:
			m.width, m.height = event.Width, event.Height
		case agentTUICreateTarget:
			if event.pane != m.creation {
				return m, nil
			}
		}
		switch msg.(type) {
		case agentTUIEnvsMsg, agentTUIInventoryMsg, agentTUIPreferenceMsg:
		default:
			return m.updateCreatePane(msg)
		}
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.input.SetWidth(max(1, msg.Width-4))
	case agentTUIPreferenceMsg:
		if msg.err != nil {
			m.status = "Environment selected, but could not save preference: " + msg.err.Error()
		}
	case agentTUIEnvsMsg:
		m.discovering = false
		m.envs = msg.envs
		if strings.HasPrefix(m.status, "Discovering environments") || strings.HasPrefix(m.status, "Environment discovery:") {
			m.status = ""
		}
		if msg.err != nil {
			m.status = "Environment discovery: " + msg.err.Error()
		}
		for i := range m.columns {
			wanted := m.opt.LocalContext
			if i == 1 {
				wanted = m.opt.RemoteContext
			}
			if wanted == "" {
				wanted = m.columns[i].Env.Name
			}
			if wanted == "" {
				wanted = msg.preferred[i]
			}
			old := m.columns[i]
			m.columns[i] = agentTUIColumn{}
			for _, e := range m.envs {
				if e.Local == (i == 0) && (wanted == "" || wanted == e.Name) {
					m.columns[i].Env = e
					if e == old.Env {
						m.columns[i] = old
					}
					break
				}
			}
			if wanted != "" && m.columns[i].Env.Name == "" {
				m.columns[i].Error = "Context unavailable or wrong environment type: " + wanted
			}
		}
		return m, m.refresh()
	case agentTUIInventoryMsg:
		if msg.generation != m.generation {
			return m, nil
		}
		c := &m.columns[msg.column]
		selected := ""
		if c.Selection < len(c.Agents) {
			selected = c.Agents[c.Selection].key()
		}
		c.Agents, c.Loading, c.Error = msg.agents, false, ""
		c.Selection = min(c.Selection, max(0, len(c.Agents)-1))
		for i, a := range c.Agents {
			if a.key() == selected {
				c.Selection = i
			}
		}
		if msg.err != nil {
			c.Error = msg.err.Error()
		}
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			m.action = nil
			return m, tea.Quit
		}
		if m.form != nil {
			return m.updateForm(msg)
		}
		if m.command {
			return m.updateCommand(msg)
		}
		if m.actionsOpen {
			return m.updateActions(msg)
		}
		if m.details {
			return m.updateDetails(msg)
		}
		if m.help {
			if msg.Code == tea.KeyEsc || msg.Code == 'q' || msg.Code == tea.KeyEnter {
				m.help, m.details = false, false
			}
			return m, nil
		}
		switch msg.String() {
		case "q", "esc":
			return m, tea.Quit
		case "h", "left":
			m.focus = 0
		case "l", "right":
			m.focus = 1
		case "tab":
			m.focus = 1 - m.focus
		case "j", "down":
			m.move(1)
		case "k", "up":
			m.move(-1)
		case "g", "home":
			m.columns[m.focus].Selection = 0
		case "G", "end":
			m.columns[m.focus].Selection = max(0, len(m.columns[m.focus].Agents)-1)
		case "?":
			m.help = true
		case "r":
			return m.execute("/refresh")
		case "/":
			return m.openCommand("/")
		case "enter":
			if m.focus == 1 && m.columns[1].Env.Name == "" {
				return m.openCommand("/env remote ")
			}
			m.actionsOpen, m.actionSelection = m.selected() != nil, 0
		case "s":
			if m.focus == 1 && m.columns[1].Env.Name == "" {
				return m.openCommand("/env remote ")
			}
		case "i":
			m.details = m.selected() != nil
			m.detailScroll = 0
		case "n":
			return m.createAgent()
		case "c":
			if a := m.selected(); a != nil {
				return m.execute("/chat " + m.agentToken(*a, m.focus))
			}
		case "L":
			if m.focus == 1 && m.columns[1].Env.Name == "" {
				return m.openCommand("/lift ")
			}
			if a := m.selected(); a != nil && m.focus == 0 && a.canLift() {
				return m.openCommand("/lift " + m.agentToken(*a, 0) + " ")
			}
		}
		return m, nil
	}
	if m.command || m.form != nil {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *agentTUIModel) move(delta int) {
	c := &m.columns[m.focus]
	if len(c.Agents) > 0 {
		c.Selection = (c.Selection + delta + len(c.Agents)) % len(c.Agents)
	}
}

func (m agentTUIModel) createAgent() (tea.Model, tea.Cmd) {
	c := m.columns[m.focus]
	if c.Env.Name == "" {
		m.status = "Select an environment with /env before creating an agent"
		return m, nil
	}
	if c.Loading || c.Error != "" {
		m.status = "Refresh the environment successfully before creating an agent"
		return m, nil
	}
	wizard, err := newCreateWizardModel(CreateOptions{Namespace: valueOr(m.opt.Namespace, OrkaNamespace)})
	if err != nil {
		m.status = err.Error()
		return m, nil
	}
	m.creation = &agentTUICreatePane{wizard: wizard, env: c.Env, loading: !m.opt.Demo}
	var load tea.Cmd
	if !m.opt.Demo {
		pane, fetch := m.creation, m.loadCreateTarget
		load = func() tea.Msg {
			server, err := fetch(pane.env)
			return agentTUICreateTarget{server: server, err: err, pane: pane}
		}
	} else {
		m.creation.server = "demo"
	}
	return m, tea.Batch(wizard.Init(), quickstartTick(), load)
}

func (m agentTUIModel) updateDetails(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	a := m.selected()
	if a == nil {
		m.details = false
		return m, nil
	}
	lines := m.agentDetailsLines(*a, min(84, max(8, m.width)-8)-4)
	page := max(1, min(30, m.height-4)-4)
	maxScroll := max(0, len(lines)-page)
	switch key.String() {
	case "esc", "q", "enter":
		m.details = false
	case "j", "down":
		m.detailScroll++
	case "k", "up":
		m.detailScroll--
	case "pgdown", "ctrl+d":
		m.detailScroll += page
	case "pgup", "ctrl+u":
		m.detailScroll -= page
	case "home", "g":
		m.detailScroll = 0
	case "end", "G":
		m.detailScroll = maxScroll
	case "p", "t":
		prefix := "System prompt"
		if key.String() == "t" {
			prefix = "Tools:"
		}
		for i, line := range lines {
			if strings.HasPrefix(ansi.Strip(line), prefix) {
				m.detailScroll = i
				break
			}
		}
	}
	m.detailScroll = max(0, min(m.detailScroll, maxScroll))
	return m, nil
}

func (m agentTUIModel) agentActions(a agentTUIAgent) []agentTUIMenuAction {
	actions := []agentTUIMenuAction{{"i", "Inspect agent"}}
	if a.canChat() {
		actions = append(actions, agentTUIMenuAction{"c", "Chat with agent"})
	}
	if !a.External {
		actions = append(actions, agentTUIMenuAction{"f", "Edit inference"})
		if a.Runtime == "orka" {
			actions = append(actions, agentTUIMenuAction{"t", "Add / edit tools"})
		}
	}
	if m.focus == 0 && a.canLift() {
		actions = append(actions, agentTUIMenuAction{"L", "Lift to remote environment"})
	}
	return actions
}

func (m agentTUIModel) agentHints(a agentTUIAgent) string {
	hints := "i inspect"
	if a.canChat() {
		hints = "c chat · " + hints
	}
	if m.focus == 0 && a.canLift() {
		hints += " · L lift"
	}
	return hints + " · <enter> actions"
}

func (m agentTUIModel) updateActions(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	a := m.selected()
	if a == nil {
		m.actionsOpen = false
		return m, nil
	}
	actions := m.agentActions(*a)
	m.actionSelection = min(m.actionSelection, len(actions)-1)
	chosen := ""
	switch key.String() {
	case "esc", "q":
		m.actionsOpen = false
		return m, nil
	case "j", "down", "tab":
		m.actionSelection = (m.actionSelection + 1) % len(actions)
	case "k", "up":
		m.actionSelection = (m.actionSelection + len(actions) - 1) % len(actions)
	case "enter":
		chosen = actions[m.actionSelection].key
	default:
		for _, action := range actions {
			if key.String() == action.key {
				chosen = action.key
				break
			}
		}
	}
	if chosen == "" {
		return m, nil
	}
	m.actionsOpen = false
	switch chosen {
	case "i":
		m.details = true
		m.detailScroll = 0
	case "c":
		return m.execute("/chat " + m.agentToken(*a, m.focus))
	case "L":
		return m.openCommand("/lift " + m.agentToken(*a, 0) + " ")
	case "f":
		return m.openInference()
	case "t":
		kind := "tools"
		if m.opt.Demo {
			m.status = "DEMO · edit " + kind + " for " + a.Name + " (no action performed)"
			return m, nil
		}
		m.action = &agentTUIAction{kind: kind, agent: *a, source: m.columns[m.focus].Env}
		return m, tea.Quit
	}
	return m, nil
}

func (m agentTUIModel) selected() *agentTUIAgent {
	c := m.columns[m.focus]
	if c.Selection < 0 || c.Selection >= len(c.Agents) {
		return nil
	}
	a := c.Agents[c.Selection]
	return &a
}

func (m agentTUIModel) agentToken(a agentTUIAgent, column int) string {
	count := 0
	for _, other := range m.columns[column].Agents {
		if other.Name == a.Name {
			count++
		}
	}
	if count > 1 {
		return a.key()
	}
	return a.Name
}

func (m agentTUIModel) openCommand(value string) (tea.Model, tea.Cmd) {
	m.command, m.suggestion = true, 0
	m.input.SetValue(value)
	m.input.CursorEnd()
	return m, m.input.Focus()
}

func agentTUIWords(value string) []string {
	words := strings.Fields(value)
	if strings.HasSuffix(value, " ") {
		words = append(words, "")
	}
	return words
}

func (m agentTUIModel) suggestions() []agentTUISuggestion {
	words := agentTUIWords(m.input.Value())
	if len(words) == 0 {
		words = []string{"/"}
	}
	var choices []agentTUISuggestion
	switch len(words) {
	case 1:
		choices = []agentTUISuggestion{{"/inspect", "Selected agent details"}, {"/chat", "Chat with an agent in the focused column"}, {"/lift", "Lift a local Orka agent to a remote environment"}, {"/env", "Switch local or remote environment"}, {"/refresh", "Reload environments and agents"}, {"/help", "Keys and command syntax"}, {"/quit", "Close dashboard"}}
	case 2:
		switch words[0] {
		case "/lift", "/chat", "/inspect":
			column := m.focus
			if words[0] == "/lift" {
				column = 0
			}
			for _, a := range m.columns[column].Agents {
				if words[0] == "/lift" && !a.canLift() || words[0] == "/chat" && !a.canChat() {
					continue
				}
				choices = append(choices, agentTUISuggestion{m.agentToken(a, column), a.Runtime + " · " + a.Namespace + " · " + valueOr(a.Model, "model unknown")})
			}
		case "/env":
			choices = []agentTUISuggestion{{"local", "Local kind environment"}, {"remote", "Remote Kubernetes environment"}}
		}
	case 3:
		if words[0] == "/lift" || words[0] == "/env" && (words[1] == "local" || words[1] == "remote") {
			for _, env := range m.envs {
				local := words[0] == "/env" && words[1] == "local"
				if env.Local == local {
					choices = append(choices, agentTUISuggestion{env.Name, "Kubernetes environment"})
				}
			}
			if words[0] == "/lift" {
				choices = append(choices, agentTUISuggestion{"new", "Create a new remote AKS agent environment…"})
			}
		}
	}
	query := strings.ToLower(words[len(words)-1])
	var matches []agentTUISuggestion
	for _, choice := range choices {
		if strings.HasPrefix(strings.ToLower(choice.value), query) {
			matches = append(matches, choice)
		}
	}
	return matches
}

func (m *agentTUIModel) complete() bool {
	choices := m.suggestions()
	if len(choices) == 0 {
		return false
	}
	words := agentTUIWords(m.input.Value())
	if len(words) == 0 {
		words = []string{""}
	}
	words[len(words)-1] = choices[min(m.suggestion, len(choices)-1)].value
	m.input.SetValue(strings.Join(words, " ") + " ")
	m.input.CursorEnd()
	m.suggestion = 0
	return true
}

func (m agentTUIModel) updateCommand(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	choices := m.suggestions()
	switch key.Code {
	case tea.KeyEsc:
		m.command = false
		m.input.Blur()
		return m, nil
	case tea.KeyUp:
		if len(choices) > 0 {
			m.suggestion = (m.suggestion + len(choices) - 1) % len(choices)
		}
		return m, nil
	case tea.KeyDown:
		if len(choices) > 0 {
			m.suggestion = (m.suggestion + 1) % len(choices)
		}
		return m, nil
	case tea.KeyTab:
		m.complete()
		return m, nil
	case tea.KeyEnter:
		words := agentTUIWords(m.input.Value())
		if len(choices) > 0 && (len(words) == 0 || words[len(words)-1] != choices[min(m.suggestion, len(choices)-1)].value) {
			m.complete()
			return m, nil
		}
		return m.execute(strings.TrimSpace(m.input.Value()))
	}
	m.suggestion = 0
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(key)
	return m, cmd
}

func (m agentTUIModel) execute(line string) (tea.Model, tea.Cmd) {
	words := strings.Fields(line)
	if len(words) == 0 {
		return m, nil
	}
	m.status = ""
	fail := func(message string) (tea.Model, tea.Cmd) { m.status = message; return m, nil }
	closeBar := func() { m.command = false; m.input.Blur() }
	switch words[0] {
	case "/quit", "/help", "/refresh":
		if len(words) != 1 {
			return fail(words[0] + " takes no arguments")
		}
		closeBar()
		switch words[0] {
		case "/quit":
			return m, tea.Quit
		case "/help":
			m.help = true
		case "/refresh":
			if m.opt.Demo {
				m.status = "DEMO · sample inventory refreshed"
				return m, nil
			}
			m.discovering = true
			m.generation++ // invalidate reads from before environment rediscovery
			return m, m.loadEnvs
		}
	case "/env":
		if len(words) != 3 || words[1] != "local" && words[1] != "remote" {
			return fail("Usage: /env local|remote <context>")
		}
		for _, env := range m.envs {
			if env.Name != words[2] || env.Local != (words[1] == "local") {
				continue
			}
			i := 0
			if !env.Local {
				i = 1
			}
			m.columns[i] = agentTUIColumn{Env: env}
			if i == 0 {
				m.opt.LocalContext = env.Name
			} else {
				m.opt.RemoteContext = env.Name
			}
			closeBar()
			if m.opt.Demo {
				m.columns = agentTUIDemoColumns()
				return m, nil
			}
			var save tea.Cmd
			if m.saveEnvironment != nil {
				persist := m.saveEnvironment
				save = func() tea.Msg { return agentTUIPreferenceMsg{persist(env.Local, env.Name)} }
			}
			return m, tea.Batch(m.refresh(), save)
		}
		return fail("Environment not found; /refresh to reload contexts")
	case "/inspect", "/chat", "/lift":
		if words[0] == "/lift" && len(words) == 1 {
			return m.openCommand("/lift ")
		}
		column := m.focus
		if words[0] == "/lift" {
			column = 0
		}
		if len(words) > 3 || words[0] != "/lift" && len(words) > 2 {
			return fail("Too many arguments")
		}
		var agent *agentTUIAgent
		if len(words) >= 2 {
			for _, a := range m.columns[column].Agents {
				if words[1] == m.agentToken(a, column) || words[1] == a.key() {
					copy := a
					agent = &copy
					break
				}
			}
		} else if words[0] != "/lift" {
			agent = m.selected()
		}
		if agent == nil {
			return fail("Choose an agent using " + words[0] + " <agent>")
		}
		if words[0] == "/inspect" {
			for i, a := range m.columns[column].Agents {
				if a.key() == agent.key() {
					m.columns[column].Selection = i
				}
			}
			m.details = true
			m.detailScroll = 0
			closeBar()
			return m, nil
		}
		if words[0] == "/chat" && !agent.canChat() {
			return fail("Chat is unavailable for this external runtime")
		}
		action := agentTUIAction{kind: strings.TrimPrefix(words[0], "/"), agent: *agent, source: m.columns[column].Env}
		if words[0] == "/lift" {
			if !agent.canLift() {
				return fail("Lift supports native Orka agents")
			}
			if len(words) != 3 {
				return m.openCommand("/lift " + words[1] + " ")
			}
			if words[2] == "new" {
				closeBar()
				m.action = &action
				m.form = []string{"", "", "", DefaultLocation}
				m.formStep = 0
				m.input.SetValue("")
				return m, m.input.Focus()
			}
			for _, env := range m.envs {
				if !env.Local && env.Name == words[2] {
					action.target = env
					break
				}
			}
			if action.target.Name == "" {
				return fail("Choose a remote context or new")
			}
		}
		closeBar()
		if m.opt.Demo {
			m.status = "DEMO · " + action.kind + " " + agent.Name + " (no action performed)"
			return m, nil
		}
		m.action = &action
		return m, tea.Quit
	default:
		return fail("Unknown command; /help lists commands")
	}
	return m, nil
}

var agentTUIFormLabels = []string{"Resource group", "AKS cluster name", "Registry name (globally unique)", "Azure region"}

func (m agentTUIModel) updateForm(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if key.Code == tea.KeyEsc {
		m.form, m.action = nil, nil
		m.input.Blur()
		m.status = "New environment cancelled"
		return m, nil
	}
	if key.Code != tea.KeyEnter {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(key)
		return m, cmd
	}
	value := strings.TrimSpace(m.input.Value())
	if value == "" {
		m.status = "A value is required"
		return m, nil
	}
	m.form[m.formStep] = value
	if m.formStep < len(m.form)-1 {
		m.formStep++
		m.status = ""
		m.input.SetValue(m.form[m.formStep])
		m.input.CursorEnd()
		return m, nil
	}
	opt := withLiftDefaults(lift.Options{ResourceGroup: m.form[0], Cluster: m.form[1], Registry: m.form[2], Location: m.form[3], Payload: lift.PayloadOrka, Step: "cluster", Observability: true})
	if err := opt.Validate(); err != nil {
		m.status = err.Error()
		m.formStep = 0
		m.input.SetValue(m.form[0])
		m.input.CursorEnd()
		return m, nil
	}
	m.form = nil
	m.input.Blur()
	if m.opt.Demo {
		m.action = nil
		m.status = "DEMO · environment form complete (nothing created)"
		return m, nil
	}
	m.action.create = &opt
	return m, tea.Quit
}

func tuiOneLine(s string) string { return strings.Join(strings.Fields(safeTerminal(s)), " ") }

func agentTUIKeyHints(s string) string {
	key := lipgloss.NewStyle().Bold(true).Underline(true)
	for _, token := range []string{"<enter>", "<esc>", "<tab>"} {
		s = strings.ReplaceAll(s, token, key.Render(token))
	}
	return s
}

func (m agentTUIModel) View() tea.View {
	w, h := max(1, m.width), max(1, m.height)
	if w < 64 || h < 18 {
		v := tea.NewView(ansi.Truncate("Resize to at least 64×18 · ctrl+c quits", w, ""))
		v.AltScreen = true
		return v
	}
	fit := func(s string) string { return agentTUIKeyHints(ansi.Truncate(tuiOneLine(s), w, "…")) }
	accent := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Cyan)
	header := "KMX · AGENTS"
	if m.opt.Demo {
		header += " · DEMO"
	}
	if m.discovering {
		header += " · discovering environments…"
	}
	header = accent.Render(header)
	footer := []string{fit("h/l ←/→ columns · j/k ↑/↓ agents · / commands · n create · r refresh · ? help · q quit")}
	if m.status != "" {
		footer = append([]string{fit(m.status)}, footer...)
	}
	if m.command {
		footer = []string{}
		choices := m.suggestions()
		start := max(0, m.suggestion-3)
		for i := start; i < min(len(choices), start+4); i++ {
			prefix := "  "
			if i == m.suggestion {
				prefix = "› "
			}
			line := fit(prefix + choices[i].value + "  " + choices[i].detail)
			if i == m.suggestion {
				line = accent.Render(line)
			}
			footer = append(footer, line)
		}
		if len(choices) == 0 {
			footer = append(footer, agentTUIKeyHints("  No completions · <enter> to submit"))
		}
		footer = append(footer, fit(m.status), m.input.View(), fit("<tab> complete · ↑/↓ suggestions · <enter> accept/run · <esc> close"))
	}
	bodyHeight := h - len(footer) - 2
	var body string
	switch {
	case m.form != nil:
		lines := []string{"Create remote AKS environment", "", "Uses the current Azure account; the lift review shows the subscription.", "Creates a resource group, registry and AKS cluster, then continues agent lift.", ""}
		for i, label := range agentTUIFormLabels {
			value := m.form[i]
			if i == m.formStep {
				value = m.input.View()
			}
			lines = append(lines, label+": "+value)
		}
		lines = append(lines, "", "<enter> next · <esc> cancel")
		body = strings.Join(lines, "\n")
	case m.help:
		body = "KEYS & COMMANDS\n\n" +
			"h/l or ←/→  switch columns; j/k or ↑/↓  select agent\n" +
			"<enter> actions · i inspect · c chat · L lift local agent · r refresh\n\n" +
			"n create agent in the focused environment\n" +
			"/inspect [agent]     details in the focused column\n" +
			"/chat [agent]        open chat, /exit returns here\n" +
			"/lift <agent> <env>  lift a local native Orka agent\n" +
			"/lift <agent> new    create AKS environment, then lift\n" +
			"/env local|remote <context>  switch an environment\n" +
			"/refresh · /help · /quit\n\n" +
			"<tab> completes; <enter> accepts a suggestion, then runs a complete command.\n" +
			"Version uses app.kubernetes.io/version; generation is a fallback, not a release.\n" +
			"<esc> / <enter> returns"
	default:
		leftWidth := m.localColumnWidth(w)
		body = lipgloss.JoinHorizontal(lipgloss.Top, m.columnView(0, leftWidth, bodyHeight), m.columnView(1, w-leftWidth, bodyHeight))
	}
	// Bound every rendered line and the total footprint, including tiny/resized terminals.
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if m.form == nil && !m.help {
			lines[i] = ansi.Truncate(line, w, "")
		} else {
			lines[i] = agentTUIKeyHints(ansi.Truncate(safeTerminal(line), w, "…"))
		}
	}
	lines = lines[:min(len(lines), bodyHeight)]
	for len(lines) < bodyHeight {
		lines = append(lines, "")
	}
	content := header + "\n" + strings.Join(lines, "\n") + "\n" + strings.Join(footer, "\n")
	if m.inference != nil {
		panel := m.inferenceView()
		content = lipgloss.NewCompositor(lipgloss.NewLayer(content), lipgloss.NewLayer(panel).X((w-lipgloss.Width(panel))/2).Y(max(0, (h-lipgloss.Height(panel))/2)).Z(1)).Render()
	}
	if m.creation != nil {
		panel := m.creationView()
		content = lipgloss.NewCompositor(lipgloss.NewLayer(content), lipgloss.NewLayer(panel).X((w-lipgloss.Width(panel))/2).Y(max(0, (h-lipgloss.Height(panel))/2)).Z(1)).Render()
	}
	if m.details || m.actionsOpen {
		if a := m.selected(); a != nil {
			panel := m.agentDetailsView(*a, w)
			if m.actionsOpen {
				panel = m.agentActionsView(*a, w)
			}
			content = lipgloss.NewCompositor(
				lipgloss.NewLayer(content),
				lipgloss.NewLayer(panel).X((w-lipgloss.Width(panel))/2).Y((h-lipgloss.Height(panel))/2).Z(1),
			).Render()
		}
	}
	v := tea.NewView(content)
	v.AltScreen = true
	return v
}

func (m agentTUIModel) columnView(index, width, height int) string {
	if index == 1 && m.columns[1].Env.Name == "" {
		return m.remoteSetupView(width, height)
	}
	c := m.columns[index]
	inner := width - 4
	// Sanitize external text before adding layout whitespace. tuiOneLine on a
	// complete row would collapse the very indentation separating agent fields.
	fit := func(s string) string { return ansi.Truncate(s, inner, "…") }
	color := lipgloss.BrightBlack
	if m.focus == index {
		color = lipgloss.Cyan
	}
	border := lipgloss.NewStyle().Foreground(color)
	muted := lipgloss.NewStyle().Foreground(lipgloss.Color("#707B86"))
	heading := muted.Bold(true)
	selected := lipgloss.NewStyle().Background(lipgloss.Cyan).Foreground(lipgloss.Black).Width(inner)
	title := "LOCAL"
	if index == 1 {
		title = "REMOTE"
	}
	if m.focus == index {
		title = "● " + title
	}
	lines := []string{heading.Render(fit(title + " · " + tuiOneLine(valueOr(c.Env.Name, "not selected"))))}
	switch {
	case c.Loading:
		lines = append(lines, "Loading agents…")
	case c.Error != "":
		lines = append(lines, fit("Error: "+tuiOneLine(c.Error)))
	case c.Env.Name == "":
		lines = append(lines, "/env to choose an environment")
	default:
		noun := "agents"
		if len(c.Agents) == 1 {
			noun = "agent"
		}
		lines = append(lines, fmt.Sprintf("Connected · %d %s", len(c.Agents), noun))
	}
	lines[1] = muted.Render(fit(lines[1]))
	lines = append(lines, "")
	// Reserve an extra row for shortcut hints on the focused entry.
	count := max(1, (height-7)/5)
	start := max(0, c.Selection-count+1)
	for i := start; i < min(len(c.Agents), start+count); i++ {
		a := c.Agents[i]
		prefix := "  "
		if i == c.Selection && m.focus == index {
			prefix = "› "
		}
		runtime := a.Runtime
		if a.External {
			runtime += " (external)"
		}
		name := fit(prefix + tuiOneLine(a.Name) + " · " + tuiOneLine(a.Version))
		runtimeLine := fit("    " + tuiOneLine(runtime) + " · ready: " + tuiOneLine(a.Ready))
		inferenceLine := fit("    " + tuiOneLine(valueOr(a.Model, "model unknown")) + " · " + tuiOneLine(valueOr(a.Provider, "provider unknown")))
		toolLine := fit("    " + tuiOneLine(a.toolSummary()))
		if i == c.Selection && m.focus == index {
			name = selected.Bold(true).Render(name)
			runtimeLine = selected.Render(runtimeLine)
			inferenceLine = selected.Render(inferenceLine)
			toolLine = selected.Render(toolLine)
		}
		lines = append(lines, name, runtimeLine, inferenceLine, toolLine)
		if i == c.Selection && m.focus == index {
			lines = append(lines, selected.Render(fit("    "+m.agentHints(a))))
		}
		lines = append(lines, border.Render("  "+strings.Repeat("─", max(0, inner-2))))
	}
	if len(c.Agents) == 0 && !c.Loading && c.Error == "" && c.Env.Name != "" {
		empty := "<no agents here, n to create a new one>"
		for _, line := range strings.Split(ansi.Hardwrap(empty, inner, true), "\n") {
			if m.focus == index {
				line = selected.Render(line)
			}
			lines = append(lines, line)
		}
	}
	if len(c.Agents) > count {
		lines = append(lines, fmt.Sprintf("%d/%d", c.Selection+1, len(c.Agents)))
	}
	for i := range lines {
		lines[i] = fit(lines[i])
	}
	return lipgloss.NewStyle().Width(width).Height(height).Border(lipgloss.RoundedBorder()).BorderForeground(color).Padding(0, 1).Render(strings.Join(lines, "\n"))
}

func (m agentTUIModel) localColumnWidth(width int) int {
	if m.columns[1].Env.Name == "" {
		return width - min(32, width/3)
	}
	return width / 2
}

func (m agentTUIModel) remoteSetupView(width, height int) string {
	inner := max(1, width-4)
	color := lipgloss.BrightBlack
	if m.focus == 1 {
		color = lipgloss.Cyan
	}
	muted := lipgloss.NewStyle().Foreground(lipgloss.Color("#707B86"))
	rows := []string{muted.Bold(true).Render("REMOTE"), muted.Render("Not configured"), ""}
	for _, text := range []string{"s / <enter>: select remote", "L: lift an agent", "Choose new in /lift to create a remote environment."} {
		rows = append(rows, strings.Split(ansi.Wrap(agentTUIKeyHints(text), inner, ""), "\n")...)
		rows = append(rows, "")
	}
	if err := m.columns[1].Error; err != "" {
		rows = append(rows, strings.Split(ansi.Wrap(tuiOneLine(err), inner, ""), "\n")...)
	}
	rows = rows[:min(len(rows), max(1, height-2))]
	return lipgloss.NewStyle().Width(width).Height(height).Padding(0, 1).Border(lipgloss.RoundedBorder()).BorderForeground(color).Render(strings.Join(rows, "\n"))
}

func (m agentTUIModel) agentDetailsView(a agentTUIAgent, screenWidth int) string {
	width := min(84, screenWidth-8)
	inner := width - 4
	background := lipgloss.Color("#18232D")
	text := lipgloss.Color("#E6EDF3")
	fit := func(s string) string { return ansi.Truncate(tuiOneLine(s), inner, "…") }
	title := lipgloss.NewStyle().Background(lipgloss.Cyan).Foreground(lipgloss.Black).Bold(true).Width(inner).Render(fit("AGENT · " + a.Name))
	lines := m.agentDetailsLines(a, inner)
	page := max(1, min(30, m.height-4)-4)
	start := max(0, min(m.detailScroll, max(0, len(lines)-page)))
	end := min(len(lines), start+page)
	rows := append([]string{title}, lines[start:end]...)
	rows = append(rows, agentTUIKeyHints(fit(fmt.Sprintf("↑/↓ scroll · t tools · p prompt · <esc> / <enter> close · %d/%d", end, len(lines)))))
	return lipgloss.NewStyle().Width(width).Padding(0, 1).Background(background).Foreground(text).
		Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Cyan).BorderBackground(background).Render(strings.Join(rows, "\n"))
}

func (m agentTUIModel) agentDetailsLines(a agentTUIAgent, inner int) []string {
	inner = max(1, inner)
	runtime := a.Runtime
	if a.External {
		runtime += " · external CLI runtime"
	}
	var rows []string
	label := lipgloss.NewStyle().Foreground(lipgloss.Cyan).Bold(true)
	value := lipgloss.NewStyle().Foreground(lipgloss.Color("#E6EDF3"))
	muted := lipgloss.NewStyle().Foreground(lipgloss.Color("#93A4B5"))
	field := func(key, text string) {
		prefix := key + ": "
		indent := min(lipgloss.Width(prefix), inner/2)
		wrapped := strings.Split(ansi.Wrap(tuiOneLine(text), max(1, inner-indent), ""), "\n")
		for i, line := range wrapped {
			if i == 0 {
				rows = append(rows, label.Render(prefix)+value.Render(line))
			} else {
				rows = append(rows, strings.Repeat(" ", indent)+value.Render(line))
			}
		}
	}
	block := func(text string, indent int) {
		rows = append(rows, agentTUIIndentedText(text, inner, indent)...)
	}
	for _, entry := range [][2]string{
		{"Environment", m.columns[m.focus].Env.Name}, {"Namespace", a.Namespace},
		{"Runtime", runtime}, {"Version", a.Version}, {"Agent Ready", a.Ready},
		{"Inference provider", valueOr(a.Provider, "unknown")}, {"Model", valueOr(a.Model, "unknown")},
		{"Endpoint", valueOr(a.Endpoint, "unknown")}, {"Inference Ready", a.InferenceReady},
	} {
		field(entry[0], entry[1])
	}
	if a.InferenceConfig != "" {
		field("Inference config", a.InferenceConfig)
	}
	rows = append(rows, "")
	field("Tools", strings.TrimPrefix(a.toolSummary(), "Tools: "))
	for _, tool := range a.Tools {
		state := "enabled"
		if tool.Disabled {
			state = "disabled"
		}
		toolRows := agentTUIIndentedText(tuiOneLine(tool.Name)+" · "+tuiOneLine(tool.Kind)+" · "+state, inner, 2)
		for _, line := range toolRows {
			rows = append(rows, label.Render(line))
		}
		if tool.Detail != "" {
			block(tool.Detail, 4)
		}
	}
	rows = append(rows, "", label.Render("System prompt")+muted.Render(ansi.Truncate(" · "+tuiOneLine(valueOr(a.PromptSource, "inline")), max(1, inner-13), "…")))
	if a.PromptError != "" {
		block(a.PromptError, 2)
	} else {
		block(valueOr(a.SystemPrompt, "(not configured)"), 2)
	}
	return rows
}

// Wrap at words and carry both the field indentation and authored indentation
// onto continuation lines. Only unbroken tokens wider than the panel are split.
func agentTUIIndentedText(text string, width, indent int) []string {
	var rows []string
	for _, paragraph := range strings.Split(strings.ReplaceAll(safeTerminal(text), "\t", "    "), "\n") {
		trimmed := strings.TrimLeft(paragraph, " ")
		padding := min(indent+len(paragraph)-len(trimmed), max(0, width-1))
		for _, line := range strings.Split(ansi.Wrap(trimmed, max(1, width-padding), ""), "\n") {
			rows = append(rows, strings.Repeat(" ", padding)+line)
		}
	}
	return rows
}

func (m agentTUIModel) agentActionsView(a agentTUIAgent, screenWidth int) string {
	width := min(60, screenWidth-8)
	inner := width - 4
	background := lipgloss.Color("#18232D")
	fit := func(s string) string { return ansi.Truncate(tuiOneLine(s), inner, "…") }
	rows := []string{fit("ACTIONS · " + a.Name), ""}
	actions := m.agentActions(a)
	for i, action := range actions {
		row := "  " + action.key + "  " + action.label
		if i == min(m.actionSelection, len(actions)-1) {
			row = lipgloss.NewStyle().Width(inner).Background(lipgloss.Cyan).Foreground(lipgloss.Black).
				Render(ansi.Truncate("› "+action.key+"  "+action.label, inner, "…"))
		}
		rows = append(rows, row)
	}
	rows = append(rows, "", agentTUIKeyHints(fit("j/k ↑/↓ choose · <enter> run · <esc> close")))
	return lipgloss.NewStyle().Width(width).Padding(0, 1).Background(background).Foreground(lipgloss.Color("#E6EDF3")).
		Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Cyan).BorderBackground(background).Render(strings.Join(rows, "\n"))
}
