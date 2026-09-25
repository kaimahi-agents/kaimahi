package app

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type consoleInferenceLoaded struct {
	pane     *consoleInferencePane
	snapshot consoleInferenceSnapshot
	err      error
}
type consoleInferenceSaved struct{ err error }
type consoleAzureChoice struct {
	Label, Detail, ID, Tenant, ResourceGroup, Endpoint, Model string
}
type consoleAzureLoaded struct {
	pane    *consoleInferencePane
	stage   string
	choices []consoleAzureChoice
	err     error
}
type consoleInferenceField struct {
	label string
	input textinput.Model
}
type consoleInferencePane struct {
	env                                                              agentTUIEnvironment
	agent                                                            agentTUIAgent
	snapshot                                                         consoleInferenceSnapshot
	stage                                                            string
	selection, field, frame                                          int
	source                                                           consoleInferenceSource
	fields                                                           []consoleInferenceField
	model                                                            string
	err                                                              error
	cancel                                                           context.CancelFunc
	cancelling                                                       bool
	azureAvailable                                                   bool
	azureChoices                                                     []consoleAzureChoice
	azureSubscription, azureTenant, azureResourceGroup, azureAccount string
}

func (m agentTUIModel) openInference() (tea.Model, tea.Cmd) {
	agent := m.selected()
	if agent == nil || agent.External {
		return m, nil
	}
	p := &consoleInferencePane{env: m.columns[m.focus].Env, agent: *agent, stage: "loading"}
	_, azErr := exec.LookPath("az")
	p.azureAvailable = azErr == nil
	m.inference = p
	if m.opt.Demo {
		p.stage = "sources"
		p.snapshot = consoleInferenceSnapshot{Server: "demo", Version: "demo", Sources: []consoleInferenceSource{{Kind: "cluster", Name: "demo-provider", Model: agent.Model, Provider: "openai"}}}
		return m, nil
	}
	load := m.loadInference
	return m, tea.Batch(quickstartTick(), func() tea.Msg { snapshot, err := load(p.env, p.agent); return consoleInferenceLoaded{p, snapshot, err} })
}

func (p *consoleInferencePane) sourceKinds() []string {
	if !p.env.Local {
		kinds := []string{"foundry-cluster", "ollama", "apikey"}
		if p.azureAvailable {
			kinds = append([]string{"azure"}, kinds...)
		}
		return kinds
	}
	if p.agent.Runtime == "kagent" {
		return []string{"ollama", "apikey"}
	}
	kinds := []string{"foundry", "ollama", "copilot", "apikey"}
	if p.azureAvailable {
		kinds = append([]string{"azure"}, kinds...)
	}
	return kinds
}

func (p *consoleInferencePane) setFields(kind string) {
	p.source = consoleInferenceSource{Kind: kind}
	p.fields = nil
	p.field = 0
	p.stage = "fields"
	p.err = nil
	if kind != "cluster" {
		p.model = ""
	}
	var labels, values []string
	switch kind {
	case "foundry-cluster":
		labels = []string{"Foundry HTTPS endpoint", "Deployment / model", "Existing cluster Secret name", "Secret key name"}
		values = []string{"", "", "", "api-key"}
	case "foundry":
		labels = []string{"Foundry HTTPS endpoint", "Deployment / model", "Tenant (optional)"}
		values = []string{"", "", ""}
	case "copilot":
		labels = []string{"Copilot model"}
		values = []string{"gpt-4.1"}
	case "ollama":
		labels = []string{"Ollama endpoint (reachable from cluster)", "Model"}
		values = []string{"http://ollama.ollama.svc.cluster.local:11434", "qwen2.5:3b"}
	case "apikey":
		labels = []string{"Provider type (openai / anthropic)", "API endpoint", "Model", "Existing Kubernetes Secret name", "Secret key name"}
		values = []string{"openai", "https://api.openai.com/v1", "", "", "api-key"}
	case "cluster":
		labels = []string{"Model override (empty uses Provider default)"}
		values = []string{p.model}
	}
	for i, label := range labels {
		input := textinput.New()
		input.CharLimit = 512
		input.SetWidth(64)
		input.SetValue(values[i])
		if i == 0 {
			input.Focus()
		}
		p.fields = append(p.fields, consoleInferenceField{label, input})
	}
}

func (p *consoleInferencePane) readFields() error {
	v := func(i int) string { return strings.TrimSpace(p.fields[i].input.Value()) }
	s := p.source
	switch s.Kind {
	case "foundry-cluster":
		s.Endpoint, s.Model, s.Secret, s.SecretKey = v(0), v(1), v(2), v(3)
	case "cluster":
		p.model = v(0)
		return refuseWizardCredentials(p.model)
	case "foundry":
		s.Endpoint, s.Model, s.Tenant = v(0), v(1), v(2)
	case "copilot":
		s.Model = v(0)
	case "ollama":
		s.Endpoint, s.Model = v(0), v(1)
	case "apikey":
		s.Provider, s.Endpoint, s.Model, s.Secret, s.SecretKey = v(0), v(1), v(2), v(3), v(4)
	}
	s.Name = consoleInferenceDefaultName(s)
	if err := s.validate(p.agent.Runtime); err != nil {
		return err
	}
	p.source = s
	return nil
}

func consoleInferenceDefaultName(s consoleInferenceSource) string {
	prefix := s.Kind
	if s.Kind == "foundry-cluster" {
		prefix = "foundry"
	}
	if s.Kind == "apikey" {
		prefix = valueOr(s.Provider, "api")
	}
	return slugAgentName(prefix + "-" + s.Model)
}

func (p *consoleInferencePane) nameSource() tea.Cmd {
	p.stage = "name"
	p.field = 0
	p.err = nil
	input := textinput.New()
	input.CharLimit = 63
	input.SetWidth(64)
	input.SetValue(consoleInferenceDefaultName(p.source))
	input.CursorEnd()
	p.fields = []consoleInferenceField{{label: "Inference source name", input: input}}
	return p.fields[0].input.Focus()
}

func (m agentTUIModel) loadAzure(stage string) (tea.Model, tea.Cmd) {
	p := m.inference
	p.stage = "azure-loading"
	p.err = nil
	p.selection = 0
	if m.opt.Demo {
		choices := []consoleAzureChoice{{Label: "Demo subscription", ID: "demo", Tenant: ""}}
		if stage == "azure-accounts" {
			choices = []consoleAzureChoice{{Label: "Demo Foundry", ID: "demo-foundry", ResourceGroup: "demo", Endpoint: "https://example.openai.azure.com"}}
		}
		if stage == "azure-deployments" {
			choices = []consoleAzureChoice{{Label: "chat-model", Model: "chat-model", Detail: "gpt-4.1"}}
		}
		return m, func() tea.Msg { return consoleAzureLoaded{p, stage, choices, nil} }
	}
	ctx, cancel := context.WithCancel(m.inferenceContext)
	p.cancel = cancel
	fetch := m.loadAzureInference
	sub, group, account := p.azureSubscription, p.azureResourceGroup, p.azureAccount
	return m, tea.Batch(quickstartTick(), func() tea.Msg {
		defer cancel()
		choices, err := fetch(ctx, stage, sub, group, account)
		return consoleAzureLoaded{p, stage, choices, err}
	})
}

func (m agentTUIModel) updateInference(msg tea.Msg) (tea.Model, tea.Cmd) {
	p := m.inference
	switch msg := msg.(type) {
	case consoleInferenceLoaded:
		if msg.pane != p {
			return m, nil
		}
		p.snapshot, p.err = msg.snapshot, msg.err
		p.stage = "sources"
		if msg.err != nil {
			p.stage = "done"
		}
		return m, nil
	case consoleAzureLoaded:
		if msg.pane != p {
			return m, nil
		}
		p.stage, p.azureChoices, p.err = msg.stage, msg.choices, msg.err
		p.cancel = nil
		if p.err == nil && len(msg.choices) == 0 {
			p.err = fmt.Errorf("no available Azure choices; check login/access or use manual Foundry setup")
		}
		if p.err != nil {
			p.stage = "kinds"
		}
		return m, nil
	case consoleInferenceSaved:
		p.err = msg.err
		p.stage = "done"
		if p.cancel != nil {
			p.cancel()
		}
		if !m.opt.Demo {
			return m, m.refresh()
		}
		return m, nil
	case quickstartTickMsg:
		p.frame++
		if p.stage == "loading" || p.stage == "saving" || p.stage == "azure-loading" {
			return m, quickstartTick()
		}
		return m, nil
	case tea.WindowSizeMsg:
		for i := range p.fields {
			p.fields[i].input.SetWidth(max(1, min(76, m.width-14)))
		}
		return m, nil
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" || msg.Code == tea.KeyEsc {
			if p.stage == "saving" {
				p.cancelling = true
				p.cancel()
				return m, nil
			}
			if p.cancel != nil {
				p.cancel()
			}
			m.inference = nil
			return m, nil
		}
		switch p.stage {
		case "loading", "saving", "azure-loading":
			return m, nil
		case "done":
			if msg.Code == tea.KeyEnter {
				m.inference = nil
			}
			return m, nil
		case "name":
			if msg.Code == tea.KeyEnter {
				p.source.Name = strings.TrimSpace(p.fields[0].input.Value())
				if p.source.Name == "" {
					p.source.Name = consoleInferenceDefaultName(p.source)
				}
				p.err = p.source.validate(p.agent.Runtime)
				if p.err == nil {
					p.stage = "review"
					p.selection = 0
				}
				return m, nil
			}
			var cmd tea.Cmd
			p.fields[0].input, cmd = p.fields[0].input.Update(msg)
			return m, cmd
		case "fields":
			if msg.Code == tea.KeyEnter && p.field == len(p.fields)-1 {
				p.err = p.readFields()
				if p.err != nil {
					return m, nil
				}
				if p.source.Kind != "cluster" {
					return m, p.nameSource()
				}
				p.stage = "review"
				p.selection = 0
				return m, nil
			}
			if msg.Code == tea.KeyTab || msg.Code == tea.KeyEnter || msg.String() == "shift+tab" {
				p.fields[p.field].input.Blur()
				step := 1
				if msg.String() == "shift+tab" {
					step = -1
				}
				p.field = (p.field + step + len(p.fields)) % len(p.fields)
				return m, p.fields[p.field].input.Focus()
			}
			var cmd tea.Cmd
			p.fields[p.field].input, cmd = p.fields[p.field].input.Update(msg)
			return m, cmd
		default:
			count := len(p.snapshot.Sources) + 1
			if p.stage == "kinds" {
				count = len(p.sourceKinds())
			}
			if p.stage == "review" {
				count = 2
			}
			if strings.HasPrefix(p.stage, "azure-") {
				count = len(p.azureChoices)
			}
			if count == 0 {
				return m, nil
			}
			switch msg.String() {
			case "j", "down", "tab":
				p.selection = (p.selection + 1) % count
			case "k", "up":
				p.selection = (p.selection + count - 1) % count
			case "enter":
				switch p.stage {
				case "sources":
					if p.selection == len(p.snapshot.Sources) {
						p.stage = "kinds"
						p.selection = 0
						return m, nil
					}
					selected := p.snapshot.Sources[p.selection]
					p.source = selected
					if selected.Kind == "cluster" && p.agent.Runtime == "orka" {
						p.model, _ = p.snapshot.Model["name"].(string)
						p.setFields("cluster")
						p.source = selected
						return m, p.fields[0].input.Focus()
					}
					p.stage = "review"
					p.selection = 0
				case "kinds":
					if p.sourceKinds()[p.selection] == "azure" {
						return m.loadAzure("azure-subscriptions")
					}
					p.setFields(p.sourceKinds()[p.selection])
					return m, p.fields[0].input.Focus()
				case "azure-subscriptions":
					choice := p.azureChoices[p.selection]
					p.azureSubscription, p.azureTenant = choice.ID, choice.Tenant
					return m.loadAzure("azure-accounts")
				case "azure-accounts":
					choice := p.azureChoices[p.selection]
					p.azureAccount, p.azureResourceGroup = choice.ID, choice.ResourceGroup
					p.source = consoleInferenceSource{Kind: "foundry", Endpoint: choice.Endpoint, Tenant: p.azureTenant}
					if !p.env.Local {
						p.source.Kind = "foundry-cluster"
						p.source.Subscription, p.source.ResourceGroup, p.source.Account = p.azureSubscription, p.azureResourceGroup, p.azureAccount
					}
					return m.loadAzure("azure-deployments")
				case "azure-deployments":
					p.source.Model = p.azureChoices[p.selection].Model
					return m, p.nameSource()
				case "review":
					if p.selection == 0 {
						m.inference = nil
						return m, nil
					}
					if m.opt.Demo {
						p.stage = "done"
						return m, nil
					}
					p.stage = "saving"
					p.err = nil
					result, cancel := m.startInference(p.env, p.agent, p.snapshot, p.source, p.model)
					p.cancel = cancel
					return m, tea.Batch(quickstartTick(), func() tea.Msg { return <-result })
				}
			}
			return m, nil
		}
	}
	if p.stage == "fields" || p.stage == "name" {
		var cmd tea.Cmd
		p.fields[p.field].input, cmd = p.fields[p.field].input.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m agentTUIModel) inferenceView() string {
	p := m.inference
	width := min(88, m.width-8)
	inner := width - 4
	height := min(28, m.height-2)
	fit := func(s string) string { return ansi.Truncate(tuiOneLine(s), inner, "…") }
	heading := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Cyan)
	muted := lipgloss.NewStyle().Foreground(lipgloss.Color("#93A4B5"))
	rows := []string{heading.Render(fit("INFERENCE · " + p.agent.Name)), muted.Render(fit(p.env.Name + " · " + p.agent.Namespace)), "", muted.Render(strings.Repeat("─", inner)), ""}
	footer := "j/k ↑/↓ choose · <enter> select · <esc> close"
	var choices []string
	switch p.stage {
	case "sources":
		for _, s := range p.snapshot.Sources {
			name := s.Name
			if s.Namespace != "" {
				name = s.Namespace + "/" + name
			}
			choices = append(choices, name+" · "+s.Kind+" · "+s.Model)
		}
		choices = append(choices, "Add inference source…")
	case "kinds":
		for _, kind := range p.sourceKinds() {
			switch kind {
			case "foundry":
				choices = append(choices, "Foundry · enter endpoint and deployment")
			case "foundry-cluster":
				choices = append(choices, "Foundry · cluster authentication with Secret")
			case "azure":
				choices = append(choices, "Azure · discover Foundry deployments with az login")
			case "copilot":
				choices = append(choices, "Copilot · host GitHub login connector")
			case "ollama":
				choices = append(choices, "Ollama · cluster model endpoint")
			case "apikey":
				choices = append(choices, "API key · cluster Secret-backed connector")
			}
		}
	case "azure-subscriptions", "azure-accounts", "azure-deployments":
		label := map[string]string{"azure-subscriptions": "Azure subscription", "azure-accounts": "Foundry resource", "azure-deployments": "Model deployment"}[p.stage]
		rows = append(rows, heading.Render(label))
		for _, choice := range p.azureChoices {
			choices = append(choices, choice.Label+" · "+choice.Detail)
		}
	case "name":
		rows = append(rows, heading.Render("Inference source name"), fit(p.source.Kind+" · "+p.source.Model))
		input := p.fields[0].input
		input.SetWidth(max(1, inner-2))
		rows = append(rows, input.View(), muted.Render(fit("Accept the suggested name or type your own.")))
		footer = "<enter> review · <esc> cancel"
	case "fields":
		rows = append(rows, fit("Add / edit "+p.source.Kind))
		if p.source.Kind == "foundry" {
			rows = append(rows, fit("Uses az login on this host; model calls run from console chat."))
		}
		if p.source.Kind == "copilot" {
			rows = append(rows, fit("Uses installed Copilot CLI and copilot login on this host."))
		}
		if p.source.Kind == "foundry-cluster" {
			rows = append(rows, fit("Cluster calls Foundry directly; no host inference/login required."))
		}
		rows = append(rows, fit(fmt.Sprintf("%d/%d · %s", p.field+1, len(p.fields), p.fields[p.field].label)))
		input := p.fields[p.field].input
		input.SetWidth(max(1, inner-2))
		rows = append(rows, input.View())
		footer = "<tab> next · shift+tab back · <enter> continue · <esc> cancel"
	case "review":
		model := p.source.Model
		if p.source.Kind == "cluster" {
			model = valueOr(p.model, model)
		}
		rows = append(rows, fit("Source: "+p.source.Name+" · "+p.source.Kind), fit("Model: "+valueOr(model, "Provider default")))
		if p.source.Namespace != "" {
			rows = append(rows, fit("Provider namespace: "+p.source.Namespace))
		}
		if p.source.Endpoint != "" {
			rows = append(rows, fit("Endpoint: "+p.source.Endpoint))
		}
		if p.source.Kind == "foundry" || p.source.Kind == "copilot" {
			rows = append(rows, fit("Host chat override; cluster agent stays unchanged."), fit("Verify login and save routing; no inference call."))
		} else {
			rows = append(rows, fit("Server: "+p.snapshot.Server), fit("Save agent configuration; new connectors are create-only."))
		}
		if p.source.Secret != "" {
			rows = append(rows, fit("Secret: "+p.source.Secret+" / "+p.source.SecretKey))
		}
		if p.source.Kind == "foundry-cluster" {
			rows = append(rows, fit("Cluster Secret + Provider; one small billed cluster probe."))
			if p.source.Account != "" {
				rows = append(rows, fit("Azure CLI retrieves key once during setup; no runtime dependency."))
			}
		}
		choices = []string{"Cancel", "Save inference"}
	case "loading", "saving", "azure-loading":
		text := "Loading inference sources…"
		if p.stage == "azure-loading" {
			text = "Discovering Azure Foundry resources…"
		}
		if p.stage == "saving" {
			text = "Setting up inference; waiting for readiness…"
		}
		if p.cancelling {
			text = "Cancelling; waiting for work to stop…"
		}
		rows = append(rows, fit([]string{"⠋", "⠙", "⠹", "⠸"}[p.frame%4]+" "+text))
		footer = "<esc> cancel"
	case "done":
		text := "Inference saved"
		if m.opt.Demo {
			text = "DEMO · nothing saved"
		}
		if p.err != nil {
			text = "Inference setup stopped; completed resources are retained."
		}
		rows = append(rows, fit(text))
		footer = "<enter> / <esc> close"
	}
	if p.err != nil {
		rows = append(rows, agentTUIIndentedText(p.err.Error(), inner, 0)...)
	}
	if len(choices) > 0 {
		budget := max(1, height-3-len(rows))
		start := max(0, p.selection-budget+1)
		for i := start; i < min(len(choices), start+budget); i++ {
			text := fit("  " + choices[i])
			if i == p.selection {
				text = pickerSelectedStyle().Width(inner).Render(fit("› " + choices[i]))
			}
			rows = append(rows, text)
		}
	}
	rows = rows[:min(len(rows), height-3)]
	rows = append(rows, fit(footer))
	return lipgloss.NewStyle().Width(width).Padding(0, 1).Background(lipgloss.Color("#18232D")).Foreground(lipgloss.Color("#E6EDF3")).Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Cyan).BorderBackground(lipgloss.Color("#18232D")).Render(strings.Join(rows, "\n"))
}
