package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

type foundryInputModel struct {
	fields              []textinput.Model
	focus               int
	accepted, cancelled bool
	err                 string
}

func newFoundryInput(c foundryChatConfig) foundryInputModel {
	m := foundryInputModel{}
	for i, value := range []string{c.Endpoint, c.Deployment, c.Tenant, c.Audience} {
		field := textinput.New()
		field.SetValue(value)
		field.SetWidth(64)
		field.CharLimit = 512
		if i == 0 {
			field.Focus()
		}
		m.fields = append(m.fields, field)
	}
	return m
}
func (m foundryInputModel) Init() tea.Cmd { return m.fields[0].Focus() }
func (m foundryInputModel) config() foundryChatConfig {
	return foundryChatConfig{Endpoint: strings.TrimSpace(m.fields[0].Value()), Deployment: strings.TrimSpace(m.fields[1].Value()), Tenant: strings.TrimSpace(m.fields[2].Value()), Audience: strings.TrimSpace(m.fields[3].Value())}
}
func (m foundryInputModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		for i := range m.fields {
			m.fields[i].SetWidth(max(5, min(64, size.Width-6)))
		}
		return m, nil
	}
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "esc", "ctrl+c":
			m.cancelled = true
			return m, tea.Quit
		case "tab", "enter":
			if m.focus == 3 && key.String() == "enter" {
				if err := m.config().validate(); err != nil {
					m.err = err.Error()
					return m, nil
				}
				m.accepted = true
				return m, tea.Quit
			}
			m.fields[m.focus].Blur()
			m.focus = (m.focus + 1) % 4
			return m, m.fields[m.focus].Focus()
		case "shift+tab":
			m.fields[m.focus].Blur()
			m.focus = (m.focus + 3) % 4
			return m, m.fields[m.focus].Focus()
		}
	}
	var cmd tea.Cmd
	m.fields[m.focus], cmd = m.fields[m.focus].Update(msg)
	return m, cmd
}
func (m foundryInputModel) View() tea.View {
	return tea.NewView("FOUNDRY · Azure login, no API key\n\nResource endpoint\n" + m.fields[0].View() + "\nDeployment name\n" + m.fields[1].View() + "\nTenant ID (optional)\n" + m.fields[2].View() + "\nToken scope (optional; defaults to Cognitive Services)\n" + m.fields[3].View() + "\n\n" + m.err + "\ntab fields · enter continue · esc cancel")
}

func (b *orkaChatBackend) foundryPick(ctx context.Context, title string, items []chatPickerItem, search bool) (int, bool, error) {
	m, err := runChatPicker(ctx, b.app.Stdin, b.app.Out, chatPicker{title: "FOUNDRY · " + title, items: items, searchEnabled: search, searchDefault: search, vim: true, action: "select"})
	if err != nil || !m.accepted {
		return 0, false, err
	}
	return m.matches()[m.selection], true, nil
}
func (b *orkaChatBackend) foundryFetch(ctx context.Context, label string, args ...string) ([]byte, error) {
	return runStatusLoading(ctx, b.app.Stdin, b.app.Out, label, nil, nil, func(ctx context.Context) ([]byte, error) { return b.app.liftDiscovery(ctx, "az", args...) })
}

func (b *orkaChatBackend) browseFoundryChat(ctx context.Context) (foundryChatConfig, error) {
	var c foundryChatConfig
	raw, err := b.foundryFetch(ctx, "Loading Azure subscriptions", "account", "list", "-o", "json", "--only-show-errors")
	if err != nil {
		return c, err
	}
	var subs []struct{ Name, ID, TenantID string }
	if err = json.Unmarshal(raw, &subs); err != nil {
		return c, err
	}
	var items []chatPickerItem
	for _, s := range subs {
		items = append(items, chatPickerItem{name: s.Name})
	}
	i, ok, err := b.foundryPick(ctx, "Subscription", items, true)
	if err != nil {
		return c, err
	}
	if !ok {
		return c, context.Canceled
	}
	sub := subs[i]
	c.Tenant = sub.TenantID
	raw, err = b.foundryFetch(ctx, "Loading Foundry resources", "cognitiveservices", "account", "list", "--subscription", sub.ID, "-o", "json", "--only-show-errors")
	if err != nil {
		return c, err
	}
	var all []foundryAccount
	if err = json.Unmarshal(raw, &all); err != nil {
		return c, err
	}
	var accounts []foundryAccount
	items = nil
	for _, a := range all {
		if a.Kind == "AIServices" || a.Kind == "OpenAI" {
			accounts = append(accounts, a)
			items = append(items, chatPickerItem{name: a.Name, detail: a.Location + " · " + a.ResourceGroup})
		}
	}
	if len(items) == 0 {
		return c, fmt.Errorf("no Foundry resources visible; enter an endpoint if you have inference access without discovery access")
	}
	i, ok, err = b.foundryPick(ctx, "Resource", items, true)
	if err != nil {
		return c, err
	}
	if !ok {
		return c, context.Canceled
	}
	account := accounts[i]
	c.Endpoint, err = foundryBaseURL(account)
	if err != nil {
		return c, err
	}
	args := append([]string{"cognitiveservices", "account", "deployment", "list"}, foundryScope(chatLiftTarget{Subscription: sub.ID}, account)...)
	raw, err = b.foundryFetch(ctx, "Loading model deployments", append(args, "-o", "json", "--only-show-errors")...)
	if err != nil {
		return c, err
	}
	var allDeployments []foundryDeployment
	if err = json.Unmarshal(raw, &allDeployments); err != nil {
		return c, err
	}
	var deployments []foundryDeployment
	items = nil
	for _, d := range allDeployments {
		if d.Properties.ProvisioningState == "Succeeded" {
			deployments = append(deployments, d)
			items = append(items, chatPickerItem{name: d.Name, detail: d.Properties.Model.Name + " · " + d.Properties.Model.Version})
		}
	}
	if len(items) == 0 {
		return c, fmt.Errorf("no ready deployments; deploy a chat/tool-capable model first")
	}
	i, ok, err = b.foundryPick(ctx, "Deployment (chat/tool support checked next)", items, true)
	if err != nil {
		return c, err
	}
	if !ok {
		return c, context.Canceled
	}
	c.Deployment = deployments[i].Name
	return c, c.validate()
}

func (b *orkaChatBackend) configureFoundryChat(ctx context.Context) error {
	saved, savedErr := loadFoundryChatConfig()
	items := []chatPickerItem{{name: "Browse Azure deployments", detail: "Use az login · no keys copied"}, {name: "Enter endpoint and deployment", detail: "Works without subscription discovery permission"}}
	if savedErr == nil {
		items = append([]chatPickerItem{{name: "Use saved " + saved.Deployment, detail: saved.Endpoint}}, items...)
	}
	i, ok, err := b.foundryPick(ctx, "Local inference setup", items, false)
	if err != nil {
		return err
	}
	if !ok {
		return context.Canceled
	}
	c := saved
	if !(savedErr == nil && i == 0) {
		if savedErr == nil {
			i--
		}
		if i == 0 {
			c, err = b.browseFoundryChat(ctx)
			if err != nil {
				return err
			}
		} else {
			result, err := tea.NewProgram(newFoundryInput(saved), tea.WithInput(b.app.Stdin), tea.WithOutput(b.app.Out), tea.WithContext(ctx)).Run()
			if err != nil {
				return err
			}
			m := result.(foundryInputModel)
			if !m.accepted {
				return context.Canceled
			}
			c = m.config()
		}
	}
	i, ok, err = b.foundryPick(ctx, fmt.Sprintf("Use %s\nEndpoint: %s\nAuth: Azure CLI login (Entra ID)\nExecution: this host; registered cluster HTTP tools\nOne small billed test request; configuration stores no credentials.", c.Deployment, c.Endpoint), []chatPickerItem{{name: "Cancel"}, {name: "Test and use Foundry"}}, false)
	if err != nil {
		return err
	}
	if !ok || i == 0 {
		return context.Canceled
	}
	client, err := newFoundryChatClient(c)
	if err != nil {
		return err
	}
	_, err = runStatusLoading(ctx, b.app.Stdin, b.app.Out, "Signing in and testing Foundry tool-call support", nil, nil, func(ctx context.Context) ([]byte, error) {
		tool := copilotTool{Name: "kmx_connectivity_check", Description: "Return the ready status", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}}
		message, err := client.complete(ctx, []foundryMessage{{Role: "user", Content: "Call kmx_connectivity_check with no arguments."}}, []copilotTool{tool})
		if err != nil {
			return nil, err
		}
		if len(message.ToolCalls) != 1 || message.ToolCalls[0].Function.Name != tool.Name {
			return nil, fmt.Errorf("deployment did not produce the expected tool call; select a tool-capable model")
		}
		return nil, nil
	})
	if err != nil {
		client.http.CloseIdleConnections()
		return err
	}
	if err = saveFoundryChatConfig(c); err != nil {
		client.http.CloseIdleConnections()
		return err
	}
	if b.app.foundryClient != nil {
		b.app.foundryClient.http.CloseIdleConnections()
	}
	b.app.foundryClient = client
	b.app.chatInference = "foundry"
	return nil
}
