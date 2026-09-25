package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

func tuiKey(m agentTUIModel, code rune, text string) agentTUIModel {
	next, _ := m.Update(tea.KeyPressMsg{Code: code, Text: text})
	return next.(agentTUIModel)
}

func TestAgentTUINavigationAndInputModes(t *testing.T) {
	m := newAgentTUIModel(AgentTUIOptions{Demo: true})
	m = tuiKey(m, 'j', "j")
	if m.columns[0].Selection != 1 {
		t.Fatal("j did not move selection")
	}
	m = tuiKey(m, tea.KeyUp, "")
	m = tuiKey(m, tea.KeyRight, "")
	if m.focus != 1 || m.columns[0].Selection != 0 {
		t.Fatal("arrow navigation failed")
	}
	m = tuiKey(m, 'h', "h")
	m = tuiKey(m, '/', "/")
	for _, r := range "hjkl" {
		m = tuiKey(m, r, string(r))
	}
	if m.input.Value() != "/hjkl" || m.focus != 0 || m.columns[0].Selection != 0 {
		t.Fatalf("input became navigation: %+v", m)
	}
	m = tuiKey(m, tea.KeyEsc, "")
	if m.command {
		t.Fatal("escape did not close command bar")
	}
	m = tuiKey(m, 'L', "L")
	if m.input.Value() != "/lift assistant " {
		t.Fatalf("lift shortcut=%q", m.input.Value())
	}
	m = tuiKey(m, tea.KeyEsc, "")
	m.columns[0].Selection = 2 // The external runtime has neither chat nor lift.
	m = tuiKey(m, 'L', "L")
	if m.command {
		t.Fatal("unsupported lift shortcut was offered")
	}
}

func TestAgentTUILiftCompletionAndDispatch(t *testing.T) {
	m := newAgentTUIModel(AgentTUIOptions{Demo: true})
	m.opt.Demo = false // Test dispatch without executing the resulting action.
	m = tuiKey(m, '/', "/")
	m = tuiKey(m, 'l', "li")
	m = tuiKey(m, tea.KeyTab, "")
	if m.input.Value() != "/lift " {
		t.Fatal(m.input.Value())
	}
	choices := m.suggestions()
	if len(choices) != 2 || choices[0].value != "assistant" {
		t.Fatalf("agent suggestions=%+v", choices)
	}
	m = tuiKey(m, tea.KeyTab, "")
	choices = m.suggestions()
	if len(choices) != 2 || choices[0].value != "remote-demo" || choices[1].value != "new" {
		t.Fatalf("targets=%+v", choices)
	}
	m = tuiKey(m, tea.KeyTab, "")
	if m.action != nil {
		t.Fatal("completion executed an operation")
	}
	m = tuiKey(m, tea.KeyEnter, "")
	if m.action == nil || m.action.kind != "lift" || m.action.agent.Name != "assistant" || m.action.source.Name != "kind-local-demo" || m.action.target.Name != "remote-demo" {
		t.Fatalf("action=%+v", m.action)
	}
	// Same-named agents in different namespaces require a qualified identity.
	m.action = nil
	a := m.columns[0].Agents[0]
	a.Namespace = "other-agents"
	m.columns[0].Agents = append(m.columns[0].Agents, a)
	updated, _ := m.execute("/chat assistant")
	if updated.(agentTUIModel).action != nil {
		t.Fatal("ambiguous agent name dispatched")
	}
	updated, _ = m.execute("/chat orka/other-agents/assistant")
	if updated.(agentTUIModel).action.agent.Namespace != "other-agents" {
		t.Fatal("qualified agent routed to wrong namespace")
	}
}

func TestAgentTUICommandsValidateAndDemoDoesNotDispatch(t *testing.T) {
	for _, line := range []string{"/chat reporter", "/lift assistant remote-demo", "/lift reporter remote-demo", "/lift assistant kind-local-demo", "/refresh extra", "/unknown", "/quit extra"} {
		m := newAgentTUIModel(AgentTUIOptions{Demo: true})
		updated, _ := m.execute(line)
		m = updated.(agentTUIModel)
		if m.action != nil || m.status == "" {
			t.Fatalf("%q dispatched or lacked feedback: %+v", line, m.action)
		}
	}
	m := newAgentTUIModel(AgentTUIOptions{Demo: true})
	updated, _ := m.execute("/lift")
	m = updated.(agentTUIModel)
	if !m.command || m.input.Value() != "/lift " {
		t.Fatal("bare /lift did not prompt for agent")
	}
	updated, _ = m.execute("/lift assistant new")
	m = updated.(agentTUIModel)
	if m.form == nil {
		t.Fatal("new environment did not open form")
	}
	m = tuiKey(m, tea.KeyEsc, "")
	if m.form != nil || m.action != nil {
		t.Fatal("cancel retained pending cloud action")
	}
	updated, _ = m.execute("/lift assistant new")
	m = updated.(agentTUIModel)
	for _, field := range []string{"demo-rg", "demo-aks", "demoregistry", "westus3"} {
		m.input.SetValue(field)
		m = tuiKey(m, tea.KeyEnter, "")
	}
	if m.form != nil || m.action != nil || !strings.Contains(m.status, "nothing created") {
		t.Fatal("demo form dispatched cloud operation")
	}
}

func TestAgentTUIRefreshRetainsIdentityAndRejectsStaleReads(t *testing.T) {
	m := newAgentTUIModel(AgentTUIOptions{Demo: true})
	m.columns[0].Selection = 1
	m.generation = 2
	original := m.columns[0].Agents
	updated, _ := m.Update(agentTUIInventoryMsg{column: 0, generation: 1})
	m = updated.(agentTUIModel)
	if len(m.columns[0].Agents) != len(original) {
		t.Fatal("stale inventory replaced current data")
	}
	updated, _ = m.Update(agentTUIInventoryMsg{column: 0, generation: 2, agents: []agentTUIAgent{original[1], original[0]}, err: errors.New("provider unavailable")})
	m = updated.(agentTUIModel)
	if m.columns[0].Selection != 0 || m.columns[0].Error != "provider unavailable" {
		t.Fatal("refresh lost identity or partial-error evidence")
	}
	updated, _ = m.Update(agentTUIInventoryMsg{column: 0, generation: 2})
	m = updated.(agentTUIModel)
	if m.selected() != nil || m.columns[0].Selection != 0 {
		t.Fatal("empty refresh retained invalid selection")
	}
}

func TestAgentTUIViewFitsTerminalAndSanitizesMetadata(t *testing.T) {
	for _, size := range [][2]int{{100, 30}, {64, 18}, {81, 24}, {40, 12}} {
		for _, mode := range []string{"overview", "command", "details", "actions", "help", "form"} {
			m := newAgentTUIModel(AgentTUIOptions{Demo: true})
			m.width, m.height = size[0], size[1]
			m.input.SetWidth(max(1, m.width-4))
			m.columns[0].Agents[0].Name = "name\x1b]52;c;injected\a\nmetadata"
			switch mode {
			case "command":
				next, _ := m.openCommand("/")
				m = next.(agentTUIModel)
			case "details":
				m.details = true
			case "actions":
				m.actionsOpen = true
			case "help":
				m.help = true
			case "form":
				m.form = []string{"", "", "", "westus3"}
			}
			view := m.View()
			if !view.AltScreen {
				t.Fatal("dashboard did not request alternate screen")
			}
			content := view.Content
			if strings.Contains(content, "\x1b]52") {
				t.Fatal("metadata injected terminal command")
			}
			if lipgloss.Width(content) > m.width || lipgloss.Height(content) > m.height {
				t.Fatalf("%s at %v: rendered %dx%d\n%s", mode, size, lipgloss.Width(content), lipgloss.Height(content), content)
			}
			if mode == "overview" && size[0] >= 64 {
				plain := ansi.Strip(content)
				if !strings.Contains(plain, "REMOTE") || !strings.Contains(plain, "LOCAL") {
					t.Fatalf("missing columns: %s", plain)
				}
			}
		}
	}
}

// Two Orka agents, one of them pointing at a Provider in another namespace
// that cannot be read: the readable agent must survive the unreadable one's
// error rather than the whole column failing.
func TestAgentTUIInventoryPinsContextAndPreservesPartialFailures(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	fakeTool(t, dir, "kubectl", fmt.Sprintf(`printf '%%s\n' "$*" >> %q
case "$*" in
 *'api-resources'*) printf 'agents.core.orka.ai\nagents.kagent.dev\n' ;;
 *'get agents.core.orka.ai'*) printf '%%s' '{"items":[{"metadata":{"name":"same","generation":3,"labels":{"app.kubernetes.io/version":"v2"}},"spec":{"providerRef":{"name":"hosted"}},"status":{"ready":true}},{"metadata":{"name":"shared-ref","generation":1},"spec":{"providerRef":{"name":"elsewhere","namespace":"inference"}}}]}' ;;
 *'-n agents get providers.core.orka.ai -o json'*) printf '%%s' '{"items":[{"metadata":{"name":"hosted"},"spec":{"type":"openai","defaultModel":"test-model","baseURL":"https://user:password@inference.example.com/private?token=hidden"},"status":{"ready":false}}]}' ;;
 *'-n inference get providers.core.orka.ai elsewhere'*) exit 1 ;;
 *) exit 1 ;;
esac`, log))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	a := &App{Cfg: &config.Config{KubeContext: "must-not-use"}, Run: &run.Runner{}}
	agents, err := a.agentTUIInventory(t.Context(), agentTUIEnvironment{Name: "remote-test"}, "agents")
	if err == nil || !strings.Contains(err.Error(), "inference/elsewhere") || len(agents) != 2 {
		t.Fatalf("agents=%+v err=%v", agents, err)
	}
	for _, agent := range agents {
		if agent.Runtime != "orka" {
			t.Fatalf("a non-Orka runtime reached the console: %+v", agent)
		}
		if agent.Name == "same" && (agent.Model != "test-model" || agent.Version != "v2" || agent.InferenceReady != "no" || agent.Endpoint != "https://inference.example.com") {
			t.Fatalf("Orka=%+v", agent)
		}
	}
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if !strings.Contains(line, "--context remote-test --request-timeout=10s") || strings.Contains(line, "get secrets") {
			t.Fatalf("unsafe read: %s", line)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := a.agentTUIInventory(ctx, agentTUIEnvironment{Name: "remote-test"}, "agents"); err == nil {
		t.Fatal("cancelled inventory succeeded")
	}
}

// The console drives Orka Agents and nothing else. A cluster that still serves
// the legacy runtime's kinds is the case that matters: every console operation
// for those objects was removed, so listing them would advertise chat, create,
// inference, tool and lift actions that cannot run. This asserts what the
// console ASKS FOR, not what it happens to render.
func TestConsoleNeverRequestsLegacyKagentKinds(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	fakeTool(t, dir, "kubectl", fmt.Sprintf(`printf '%%s\n' "$*" >> %q
case "$*" in
 *'config view'*) printf '%%s' '{"contexts":[{"name":"remote","context":{"cluster":"r"}}],"clusters":[{"name":"r","cluster":{"server":"https://remote.example.com"}}]}' ;;
 *'api-resources'*) printf 'agents.core.orka.ai\nproviders.core.orka.ai\nagents.kagent.dev\nmodelconfigs.kagent.dev\n' ;;
 *'get agents.core.orka.ai demo'*) printf '%%s' '{"metadata":{"resourceVersion":"42"},"spec":{"providerRef":{"name":"hosted"}}}' ;;
 *'get agents.core.orka.ai'*) printf '%%s' '{"items":[{"metadata":{"name":"demo"},"spec":{"providerRef":{"name":"hosted"}}}]}' ;;
 *'get providers.core.orka.ai'*) printf '%%s' '{"items":[{"metadata":{"name":"hosted"},"spec":{"type":"openai","defaultModel":"test-model"}}]}' ;;
 *) exit 1 ;;
esac`, log))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	a := &App{Cfg: &config.Config{}, Run: &run.Runner{}}
	env := agentTUIEnvironment{Name: "remote"}
	agents, err := a.agentTUIInventory(t.Context(), env, OrkaNamespace)
	if err != nil || len(agents) != 1 || agents[0].Name != "demo" || agents[0].Runtime != "orka" {
		t.Fatalf("the Orka inventory regressed: agents=%+v err=%v", agents, err)
	}
	snapshot, err := a.consoleLoadInference(t.Context(), env, agentTUIAgent{Runtime: "orka", Name: "demo", Namespace: OrkaNamespace})
	if err != nil || len(snapshot.Sources) != 1 || snapshot.Sources[0].Name != "hosted" {
		t.Fatalf("the Orka inference flow regressed: snapshot=%+v err=%v", snapshot, err)
	}
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if strings.Contains(line, "kagent") {
			t.Fatalf("the console asked the cluster for a legacy resource: %s", line)
		}
	}
}

// The invocation test above proves the paths it walks. This proves the paths it
// does not: no console source may name the legacy kinds at all, so a new pane,
// action or connector cannot reintroduce one behind a branch no test reaches.
// Test files are excluded deliberately — a fixture that serves the legacy kinds
// is how the test above proves the console ignores them.
func TestConsoleSourcesNameNoLegacyKagentResource(t *testing.T) {
	matches, err := filepath.Glob("agent_tui*.go")
	if err != nil {
		t.Fatal(err)
	}
	var sources []string
	for _, path := range matches {
		if !strings.HasSuffix(path, "_test.go") {
			sources = append(sources, path)
		}
	}
	sources = append(sources, filepath.Join("..", "..", "..", "cmd", "kmx", "console_command.go"))
	if len(sources) < 7 {
		t.Fatalf("the console source list is too short to be the console: %q", sources)
	}
	for _, path := range sources {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"kagent.dev", "ModelConfig", `"kagent"`} {
			if strings.Contains(string(body), forbidden) {
				t.Errorf("%s names the retired resource %q", path, forbidden)
			}
		}
	}
	// The negative control: the constants the console must still be built on.
	body, err := os.ReadFile("agent_tui_inference.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`consoleAgentKind    = "agents.core.orka.ai"`, `consoleProviderKind = "providers.core.orka.ai"`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("the console no longer pins the Orka kind %q", want)
		}
	}
}

func TestAgentTUINonterminalFailsBeforeDiscovery(t *testing.T) {
	var out bytes.Buffer
	a := &App{Out: &out}
	if err := a.AgentTUI(AgentTUIOptions{Demo: true}); err == nil || !strings.Contains(err.Error(), "interactive terminal") {
		t.Fatalf("err=%v", err)
	}
}

func TestAgentTUIEnvironmentClassificationAndKubeconfigIsolation(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	defaultConfig := filepath.Join(dir, "default-config")
	savedConfig := filepath.Join(dir, "saved-config")
	if _, err := writeAgentLocation(agentLocation{Agent: "assistant", Namespace: OrkaNamespace, Context: "saved-remote", Kubeconfig: savedConfig}, nil); err != nil {
		t.Fatal(err)
	}
	fakeTool(t, dir, "kubectl", `case "$KUBECONFIG" in
 *saved-config) printf '%s' '{"clusters":[{"name":"saved","cluster":{"server":"https://saved.example.com"}}],"contexts":[{"name":"saved-remote","context":{"cluster":"saved"}}]}' ;;
 *) printf '%s' '{"clusters":[{"name":"local","cluster":{"server":"https://127.0.0.1:6443"}},{"name":"cloud","cluster":{"server":"https://cloud.example.com"}}],"contexts":[{"name":"kind-local","context":{"cluster":"local"}},{"name":"kind-cloud","context":{"cluster":"cloud"}}]}' ;;
esac`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	a := &App{Cfg: &config.Config{KubeContext: "kind-local"}, Run: &run.Runner{Env: []string{"KUBECONFIG=" + defaultConfig}}}
	envs, err := a.agentTUIEnvironments(t.Context())
	if err != nil || len(envs) != 3 {
		t.Fatalf("envs=%+v err=%v", envs, err)
	}
	var local, remote agentTUIEnvironment
	for _, env := range envs {
		switch env.Name {
		case "kind-local":
			local = env
		case "kind-cloud":
			if env.Local {
				t.Fatal("kind name overrode remote server evidence")
			}
		case "saved-remote":
			remote = env
		}
	}
	if !local.Local || remote.Local || remote.Kubeconfig != savedConfig || local.Kubeconfig != defaultConfig {
		t.Fatalf("local=%+v remote=%+v", local, remote)
	}
	// Returning from a saved remote source to a default-kubeconfig environment
	// must replace, rather than inherit, that source's private KUBECONFIG.
	target := local.app(remote.app(a))
	if target.Cfg.KubeContext != "kind-local" || strings.Join(target.Run.Env, " ") != "KUBECONFIG="+defaultConfig {
		t.Fatalf("target=%+v env=%v", target.Cfg, target.Run.Env)
	}
	if a.Cfg.KubeContext != "kind-local" || a.Run.Env[0] != "KUBECONFIG="+defaultConfig {
		t.Fatal("environment selection mutated source configuration")
	}
}

func TestAgentTUIStartupPrefersRememberedRemoteAndHonorsFlags(t *testing.T) {
	envs := []agentTUIEnvironment{
		{Name: "aks-old"}, {Name: "kind-local", Local: true},
		{Name: "saved-lift", Kubeconfig: "/saved/config"}, {Name: "selected-remote"},
	}
	for _, tc := range []struct {
		name       string
		prefs      map[string]string
		flag, want string
	}{
		{"last lift before alphabetical stale context", map[string]string{"context": "saved-lift"}, "", "saved-lift"},
		{"dashboard selection wins", map[string]string{"context": "saved-lift", "tui/remote": "selected-remote"}, "", "selected-remote"},
		{"deleted preference falls back to lift", map[string]string{"context": "saved-lift", "tui/remote": "deleted"}, "", "saved-lift"},
		{"wrong column preference ignored", map[string]string{"context": "saved-lift", "tui/remote": "kind-local"}, "", "saved-lift"},
		{"explicit context wins", map[string]string{"context": "saved-lift"}, "aks-old", "aks-old"},
		{"missing explicit context stays missing", map[string]string{"context": "saved-lift"}, "missing", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newAgentTUIModel(AgentTUIOptions{RemoteContext: tc.flag})
			m.loadInventory = func(agentTUIEnvironment) ([]agentTUIAgent, error) { return nil, nil }
			updated, _ := m.Update(agentTUIEnvsMsg{envs: envs, preferred: agentTUIPreferredEnvironments(envs, tc.prefs, "kind-local")})
			m = updated.(agentTUIModel)
			if m.columns[1].Env.Name != tc.want || m.columns[0].Env.Name != "kind-local" {
				t.Fatalf("columns=%+v", m.columns)
			}
			if tc.want == "saved-lift" && m.columns[1].Env.Kubeconfig != "/saved/config" {
				t.Fatal("lost saved credential routing")
			}
		})
	}
}

func TestAgentTUIEnvironmentSelectionPersistsOutsideDemo(t *testing.T) {
	for _, demo := range []bool{false, true} {
		m := newAgentTUIModel(AgentTUIOptions{Demo: true})
		m.opt.Demo = demo
		saved := ""
		m.saveEnvironment = func(local bool, name string) error {
			if local {
				t.Fatal("saved remote as local")
			}
			saved = name
			return nil
		}
		m.loadInventory = func(agentTUIEnvironment) ([]agentTUIAgent, error) { return nil, nil }
		_, cmd := m.execute("/env remote remote-demo")
		var run func(tea.Cmd)
		run = func(cmd tea.Cmd) {
			if cmd == nil {
				return
			}
			if batch, ok := cmd().(tea.BatchMsg); ok {
				for _, child := range batch {
					run(child)
				}
			}
		}
		run(cmd)
		if demo && saved != "" || !demo && saved != "remote-demo" {
			t.Fatalf("demo=%t saved=%q", demo, saved)
		}
	}
}

func TestAgentTUIEntriesAndDetailsOverlay(t *testing.T) {
	m := newAgentTUIModel(AgentTUIOptions{Demo: true})
	m.width, m.height = 120, 32
	column := ansi.Strip(m.columnView(0, 60, 25))
	for _, part := range []string{"› assistant", "    orka · ready: yes", "    qwen3:8b", "  ──────────"} {
		if !strings.Contains(column, part) {
			t.Fatalf("missing indented/separated entry %q:\n%s", part, column)
		}
	}
	before := ansi.Strip(m.View().Content)
	m = tuiKey(m, 'i', "i")
	overlay := ansi.Strip(m.View().Content)
	for _, part := range []string{"LOCAL", "REMOTE", "AGENT · assistant", "Inference provider:", "<esc> / <enter> close"} {
		if !strings.Contains(overlay, part) {
			t.Fatalf("overlay missing %q:\n%s", part, overlay)
		}
	}
	m = tuiKey(m, tea.KeyDown, "")
	if m.columns[0].Selection != 0 {
		t.Fatal("modal navigation changed background selection")
	}
	m = tuiKey(m, tea.KeyEsc, "")
	if got := ansi.Strip(m.View().Content); got != before {
		t.Fatal("closing overlay did not restore overview")
	}
}

func TestAgentTUIActionsPopupCapabilitiesAndShortcuts(t *testing.T) {
	m := newAgentTUIModel(AgentTUIOptions{Demo: true})
	m.width, m.height = 120, 32
	view := ansi.Strip(m.View().Content)
	if strings.Count(view, "c chat · i inspect") != 1 {
		t.Fatal("selected-agent hints should appear exactly once, inline")
	}
	m = tuiKey(m, tea.KeyEnter, "")
	if !m.actionsOpen || m.details {
		t.Fatal("enter should open actions, not inspect")
	}
	for _, label := range []string{"ACTIONS · assistant", "Inspect agent", "Chat with agent", "Lift to remote environment", "LOCAL", "REMOTE"} {
		if !strings.Contains(ansi.Strip(m.View().Content), label) {
			t.Fatalf("missing action/background %q", label)
		}
	}
	m = tuiKey(m, tea.KeyDown, "")
	if m.actionSelection != 1 || m.columns[0].Selection != 0 {
		t.Fatal("actions moved background selection")
	}
	m = tuiKey(m, tea.KeyEnter, "")
	if m.actionsOpen || m.action != nil || !strings.Contains(m.status, "DEMO · chat assistant") {
		t.Fatal("chat menu dispatch failed")
	}
	m = tuiKey(m, tea.KeyEnter, "")
	m = tuiKey(m, 'i', "i")
	if m.actionsOpen || !m.details {
		t.Fatal("inspect shortcut in popup failed")
	}
	m = tuiKey(m, tea.KeyEsc, "")
	m = tuiKey(m, tea.KeyEnter, "")
	m = tuiKey(m, 'L', "L")
	if m.actionsOpen || !m.command || m.input.Value() != "/lift assistant " {
		t.Fatal("lift menu dispatch failed")
	}
	m = tuiKey(m, tea.KeyEsc, "")
	m.focus = 1
	if got := m.agentActions(*m.selected()); len(got) != 4 {
		t.Fatalf("remote actions=%v", got)
	}
	m.columns[1].Agents[0].External = true
	m = tuiKey(m, tea.KeyEnter, "")
	if got := m.agentActions(*m.selected()); len(got) != 1 || got[0].key != "i" {
		t.Fatalf("external runtime actions=%v", got)
	}
	m = tuiKey(m, 'c', "c")
	if !m.actionsOpen {
		t.Fatal("unavailable chat action dispatched")
	}
	m = tuiKey(m, tea.KeyEsc, "")
	if m.actionsOpen {
		t.Fatal("escape did not close actions")
	}
	m.columns[1].Agents = nil
	m = tuiKey(m, tea.KeyEnter, "")
	if m.actionsOpen {
		t.Fatal("empty column opened actions")
	}
}

func TestAgentTUIEditActionsAndInferencePatch(t *testing.T) {
	for _, key := range []rune{'f', 't'} {
		m := newAgentTUIModel(AgentTUIOptions{Demo: true})
		m.opt.Demo = false
		m = tuiKey(m, tea.KeyEnter, "")
		m = tuiKey(m, key, string(key))
		want := "inference"
		if key == 't' {
			want = "tools"
		}
		if key == 'f' {
			if m.action != nil || m.inference == nil || m.inference.agent.Name != "assistant" || m.inference.env.Name != "kind-local-demo" {
				t.Fatal("inference should open a targeted overlay")
			}
			continue
		}
		if m.action == nil || m.action.kind != want || m.action.source.Name != "kind-local-demo" || m.action.agent.Name != "assistant" {
			t.Fatalf("action=%+v", m.action)
		}
	}
	original := map[string]any{"name": "old", "maxTokens": 100}
	raw, err := consoleInferencePatch("42", "new-config", "agents", "", original)
	if err != nil {
		t.Fatal(err)
	}
	var patch []map[string]any
	if err := json.Unmarshal(raw, &patch); err != nil {
		t.Fatal(err)
	}
	if patch[0]["op"] != "test" || patch[0]["value"] != "42" {
		t.Fatal("update lacks optimistic concurrency")
	}
	if patch[1]["path"] != "/spec/providerRef" {
		t.Fatalf("the update does not repoint an Orka Provider: %v", patch[1])
	}
	model := patch[2]["value"].(map[string]any)
	if model["name"] != nil || model["maxTokens"] != float64(100) || original["name"] != "old" {
		t.Fatalf("model patch=%v", model)
	}
	if strings.Contains(string(raw), "modelConfig") {
		t.Fatalf("the update still patches a retired ModelConfig reference: %s", raw)
	}
}

func TestAgentTUIToolActionAvailableWithoutConfiguredTools(t *testing.T) {
	for _, column := range []int{0, 1} {
		m := newAgentTUIModel(AgentTUIOptions{Demo: true})
		m.focus = column
		m.columns[column].Agents[0].Tools = nil
		m = tuiKey(m, tea.KeyEnter, "")
		if !strings.Contains(ansi.Strip(m.View().Content), "Add / edit tools") {
			t.Fatal("tool-free agent must still offer tool configuration")
		}
		m.opt.Demo = false
		m = tuiKey(m, 't', "t")
		if m.action == nil || m.action.kind != "tools" || m.action.source != m.columns[column].Env {
			t.Fatalf("tool-free agent failed to open targeted editor: %+v", m.action)
		}
	}
}

func TestAgentTUIEmptyEnvironmentCreation(t *testing.T) {
	for _, demo := range []bool{true, false} {
		m := newAgentTUIModel(AgentTUIOptions{Demo: true, Namespace: "agents"})
		m.opt.Demo = demo
		m.focus = 1
		m.columns[1].Agents = nil
		m.columns[1].Env.Kubeconfig = "/remote/config"
		m.width = 160
		if !strings.Contains(ansi.Strip(m.View().Content), "<no agents here, n to create a new one>") {
			t.Fatal("missing empty entry")
		}
		m = tuiKey(m, 'n', "n")
		if m.action != nil || m.creation == nil || m.creation.env.Kubeconfig != "/remote/config" || m.creation.wizard.opt.Namespace != "agents" || m.creation.loading == demo {
			t.Fatalf("creation pane=%+v", m.creation)
		}
	}
	for _, column := range []agentTUIColumn{{}, {Env: agentTUIEnvironment{Name: "remote"}, Loading: true}, {Env: agentTUIEnvironment{Name: "remote"}, Error: "unreachable"}} {
		m := newAgentTUIModel(AgentTUIOptions{})
		m.columns[0] = column
		m = tuiKey(m, 'n', "n")
		if m.action != nil || m.creation != nil || strings.Contains(ansi.Strip(m.View().Content), "no agents here") {
			t.Fatal("unknown inventory treated as empty")
		}
	}
}

func TestAgentTUIPromptInspectionScrollAndTools(t *testing.T) {
	m := newAgentTUIModel(AgentTUIOptions{Demo: true})
	m.width, m.height = 80, 24
	m.columns[0].Agents[0].SystemPrompt = strings.Repeat("  Preserve prompt indentation and long lines.\n", 80) + "PROMPT-END"
	m = tuiKey(m, 'i', "i")
	m = tuiKey(m, 't', "t")
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "k8s-get-resources") || !strings.Contains(view, "disabled") {
		t.Fatalf("missing tool details: %s", view)
	}
	m = tuiKey(m, 'p', "p")
	if !strings.Contains(ansi.Strip(m.View().Content), "System prompt · inline") {
		t.Fatal("prompt jump failed")
	}
	m = tuiKey(m, 'G', "G")
	if view := m.View().Content; !strings.Contains(ansi.Strip(view), "PROMPT-END") || lipgloss.Height(view) > 24 {
		t.Fatal("long prompt is inaccessible or overflows")
	}
	m = tuiKey(m, 'g', "g")
	if m.detailScroll != 0 {
		t.Fatal("home did not restore details")
	}
	if m.columns[0].Selection != 0 {
		t.Fatal("inspection scrolling changed agent")
	}
}

func TestAgentTUIInventoryPromptAndToolMetadata(t *testing.T) {
	dir := t.TempDir()
	fakeTool(t, dir, "kubectl", `case "$*" in
 *api-resources*) printf 'agents.core.orka.ai\nagents.kagent.dev\n' ;;
 *'get agents.core.orka.ai'*) printf '%s' '{"items":[{"metadata":{"name":"native"},"spec":{"systemPrompt":{"configMapRef":{"name":"instructions","key":"prompt"}},"tools":[{"name":"read"},{"name":"write","enabled":false}]}},{"metadata":{"name":"missing"},"spec":{"systemPrompt":{"configMapRef":{"name":"instructions","key":"missing"}}}}]}' ;;
 *'get providers.core.orka.ai'*) printf '%s' '{"items":[]}' ;;
 *'get tools.core.orka.ai'*) printf '%s' '{"items":[{"metadata":{"name":"read"},"spec":{"description":"Read resources","http":{"method":"GET","url":"https://tools.example.com/read"}}}]}' ;;
 *'get configmap instructions'*) printf '%s' '{"data":{"prompt":"First line\n  Indented line"}}' ;;
 *) exit 1 ;;
esac`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	a := &App{Cfg: &config.Config{}, Run: &run.Runner{}}
	agents, err := a.agentTUIInventory(t.Context(), agentTUIEnvironment{Name: "remote"}, "agents")
	if err != nil || len(agents) != 2 {
		t.Fatalf("agents=%+v err=%v", agents, err)
	}
	for _, agent := range agents {
		switch agent.Name {
		case "native":
			if agent.SystemPrompt != "First line\n  Indented line" || !strings.Contains(agent.PromptSource, "instructions") || len(agent.Tools) != 2 || !agent.Tools[1].Disabled || agent.Tools[0].Kind != "HTTP GET" || !strings.Contains(agent.Tools[0].Detail, "Read resources") || !strings.Contains(agent.toolSummary(), "1/2 enabled") {
				t.Fatalf("native=%+v", agent)
			}
		case "missing":
			if agent.PromptError == "" {
				t.Fatal("missing ConfigMap key presented as empty prompt")
			}
		}
	}
}

func TestAgentTUIUnconfiguredRemotePanelAndLift(t *testing.T) {
	m := newAgentTUIModel(AgentTUIOptions{Demo: true})
	m.columns[1] = agentTUIColumn{}
	for _, width := range []int{64, 80, 120, 180} {
		m.width, m.height = width, 24
		if m.localColumnWidth(width) <= width/2 {
			t.Fatal("unconfigured remote did not collapse")
		}
		view := m.View().Content
		if lipgloss.Width(view) > width || lipgloss.Height(view) > 24 {
			t.Fatal("setup panel overflows")
		}
		if !strings.Contains(ansi.Strip(view), "Not configured") {
			t.Fatal("setup hint missing")
		}
	}
	if !strings.Contains(m.agentHints(*m.selected()), "L lift") {
		t.Fatal("local lift hint missing")
	}
	m.focus = 1
	m = tuiKey(m, tea.KeyEnter, "")
	if m.input.Value() != "/env remote " {
		t.Fatal("setup does not open remote completion")
	}
	m = tuiKey(m, tea.KeyEsc, "")
	m = tuiKey(m, 'L', "L")
	if m.input.Value() != "/lift " {
		t.Fatal("setup lift shortcut missing")
	}
	m.columns[1].Env = agentTUIEnvironment{Name: "configured"}
	if m.localColumnWidth(120) != 60 {
		t.Fatal("configured remote stayed collapsed")
	}
}

func TestAgentTUIInspectorWordWrapAndIndentation(t *testing.T) {
	text := "List live Kubernetes resources, read-only. Returns names, namespaces and status.\n  Keep authored indentation across wrapped lines too."
	lines := agentTUIIndentedText(text, 38, 4)
	for _, line := range lines {
		if !strings.HasPrefix(line, "    ") || lipgloss.Width(line) > 38 {
			t.Fatalf("bad indentation/width: %q", line)
		}
	}
	if strings.Join(strings.Fields(strings.Join(lines, " ")), " ") != strings.Join(strings.Fields(text), " ") {
		t.Fatal("wrapping split a word or lost text")
	}
	if !strings.Contains(strings.Join(lines, "\n"), "      Keep authored indentation") {
		t.Fatal("authored indentation lost")
	}
	m := newAgentTUIModel(AgentTUIOptions{Demo: true})
	a := *m.selected()
	a.Endpoint = "https://" + strings.Repeat("long", 50) + ".example.com"
	a.PromptSource = strings.Repeat("source", 40)
	for _, line := range m.agentDetailsLines(a, 52) {
		if lipgloss.Width(line) > 52 {
			t.Fatalf("inspector overflow: %q", line)
		}
	}
}

// Inference information reaches the inspector from two places: the cluster
// Provider an Agent names, and a host override saved for console chat. Both
// are Orka-only now, so both are checked against an Orka Agent.
func TestAgentTUIProviderAndHostOverrideInferenceInfo(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	fakeTool(t, dir, "kubectl", `case "$*" in
 *api-resources*) printf 'agents.core.orka.ai\n' ;;
 *'get agents.core.orka.ai'*) printf '%s' '{"items":[{"metadata":{"name":"hello"},"spec":{"providerRef":{"name":"local"}}}]}' ;;
 *'get providers.core.orka.ai'*) printf '%s' '{"items":[{"metadata":{"name":"local"},"spec":{"type":"openai","defaultModel":"qwen2.5:3b","baseURL":"http://ollama.ollama.svc.cluster.local:11434/v1"},"status":{"ready":true}}]}' ;;
 *) exit 1 ;;
esac`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	a := &App{Cfg: &config.Config{}, Run: &run.Runner{}}
	env := agentTUIEnvironment{Name: "kind-test", Local: true}
	agents, err := a.agentTUIInventory(t.Context(), env, OrkaNamespace)
	if err != nil || len(agents) != 1 {
		t.Fatalf("agents=%+v err=%v", agents, err)
	}
	agent := agents[0]
	if agent.Endpoint != "http://ollama.ollama.svc.cluster.local:11434" || agent.Provider != "local (openai)" || agent.Model != "qwen2.5:3b" || agent.InferenceReady != "yes" {
		t.Fatalf("inference=%+v", agent)
	}
	// A saved host override replaces the cluster reading and says so, rather
	// than reporting a readiness the console never checked.
	source := consoleInferenceSource{Kind: "copilot", Name: "host", Model: "gpt-4.1"}
	if err := saveConsoleInference(env, agent, &source); err != nil {
		t.Fatal(err)
	}
	agents, err = a.agentTUIInventory(t.Context(), env, OrkaNamespace)
	if err != nil || len(agents) != 1 {
		t.Fatalf("agents=%+v err=%v", agents, err)
	}
	overridden := agents[0]
	if overridden.Provider != "host copilot" || overridden.Model != "gpt-4.1" || overridden.InferenceReady != "not checked (host chat override)" || overridden.InferenceConfig == "" {
		t.Fatalf("host override=%+v", overridden)
	}
	m := newAgentTUIModel(AgentTUIOptions{Demo: true})
	view := ansi.Strip(strings.Join(m.agentDetailsLines(overridden, 100), "\n"))
	if !strings.Contains(view, "Inference config: "+overridden.InferenceConfig) || !strings.Contains(view, "Copilot CLI on this host") {
		t.Fatalf("missing inference information:\n%s", view)
	}
}

func TestAgentTUICreationOverlayReusesWizardAndStaysInConsole(t *testing.T) {
	m := newAgentTUIModel(AgentTUIOptions{Demo: true})
	m = tuiKey(m, 'n', "n")
	for _, size := range [][2]int{{64, 18}, {80, 24}, {120, 40}} {
		m.width, m.height = size[0], size[1]
		view := m.View().Content
		if lipgloss.Width(view) > m.width || lipgloss.Height(view) > m.height {
			t.Fatalf("creation overflow %v: %dx%d", size, lipgloss.Width(view), lipgloss.Height(view))
		}
		if !strings.Contains(ansi.Strip(view), "CREATE AGENT") || !strings.Contains(ansi.Strip(view), "REMOTE") {
			t.Fatal("creation replaced inventory rather than overlaying it")
		}
	}
	for _, value := range []string{"Help me inspect workloads", "inspector", "openai", "test-model", "existing-secret"} {
		m.creation.wizard.input.SetValue(value)
		m = tuiKey(m, tea.KeyEnter, "")
	}
	if m.creation.wizard.step != createConfirm {
		t.Fatalf("wizard did not reach review: %+v", m.creation.wizard)
	}
	m = tuiKey(m, tea.KeyEnter, "")
	if !m.creation.finished || m.creation.running || m.action != nil {
		t.Fatal("demo review dispatched or closed console")
	}
	m = tuiKey(m, tea.KeyEnter, "")
	if m.creation != nil {
		t.Fatal("completion did not close pane")
	}
	m = tuiKey(m, 'n', "n")
	m = tuiKey(m, 'j', "j")
	if m.columns[0].Selection != 0 || m.creation.wizard.input.Value() != "j" {
		t.Fatal("form input reached inventory")
	}
	m = tuiKey(m, tea.KeyEsc, "")
	if m.creation != nil {
		t.Fatal("escape did not close form")
	}
}

func TestAgentTUICreationSubmissionAndCancellation(t *testing.T) {
	m := newAgentTUIModel(AgentTUIOptions{Demo: true})
	m.focus = 1
	m = tuiKey(m, 'n', "n")
	m.opt.Demo = false
	m.creation.server = "reviewed.example.com"
	m.creation.wizard.opt = CreateOptions{Name: "inspector", Namespace: "agents"}
	m.creation.wizard.step = createConfirm
	started, cancelled := false, false
	result := make(chan agentTUICreateResult, 1)
	m.startCreate = func(env agentTUIEnvironment, server string, opt CreateOptions) (<-chan agentTUICreateResult, context.CancelFunc) {
		started = true
		if env.Name != "remote-demo" || server != "reviewed.example.com" || opt.Name != "inspector" || opt.Namespace != "agents" {
			t.Fatal("submission lost target or options")
		}
		return result, func() { cancelled = true }
	}
	m.loadInventory = func(agentTUIEnvironment) ([]agentTUIAgent, error) { return nil, nil }
	m = tuiKey(m, tea.KeyEnter, "")
	if !started || !m.creation.running {
		t.Fatal("submission did not start background creation")
	}
	m = tuiKey(m, tea.KeyEsc, "")
	if !cancelled || !m.creation.cancelling || !m.creation.running {
		t.Fatal("cancellation returned before operation stopped")
	}
	next, _ := m.Update(agentTUICreateResult{err: context.Canceled})
	m = next.(agentTUIModel)
	if !m.creation.finished || m.creation.running {
		t.Fatal("result did not finish pane")
	}
	m = tuiKey(m, tea.KeyEsc, "")
	if m.creation != nil || m.action != nil {
		t.Fatal("cancelled creation left console")
	}
}

func TestAgentTUICreationRefusesChangedServer(t *testing.T) {
	dir := t.TempDir()
	fakeTool(t, dir, "kubectl", `case "$*" in
 *'config view'*) printf '%s' '{"contexts":[{"name":"remote","context":{"cluster":"r"}}],"clusters":[{"name":"r","cluster":{"server":"https://changed.example.com"}}]}' ;;
 *) exit 1 ;;
esac`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	a := &App{Cfg: &config.Config{}, Run: &run.Runner{}}
	err := a.consoleCreateAgent(t.Context(), agentTUIEnvironment{Name: "remote"}, "reviewed.example.com", CreateOptions{Name: "inspector"})
	if err == nil || !strings.Contains(err.Error(), "server changed") {
		t.Fatalf("changed target not refused: %v", err)
	}
}

func TestConsoleCreationTimerOnlyRunsDuringWork(t *testing.T) {
	m := newAgentTUIModel(AgentTUIOptions{Demo: true})
	m = tuiKey(m, 'n', "n")
	for _, state := range []string{"form", "finished", "loading", "running"} {
		m.creation.loading = state == "loading"
		m.creation.running = state == "running"
		m.creation.finished = state == "finished"
		_, cmd := m.Update(quickstartTickMsg{})
		if (cmd != nil) != (state == "loading" || state == "running") {
			t.Fatalf("timer running in %s", state)
		}
	}
}

func TestConsolePromptReferenceAndSavedContextPrecedence(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	saved := filepath.Join(dir, "saved-config")
	if _, err := writeAgentLocation(agentLocation{Agent: "demo", Namespace: OrkaNamespace, Context: "remote", Kubeconfig: saved}, nil); err != nil {
		t.Fatal(err)
	}
	fakeTool(t, dir, "kubectl", `case "$*" in
 *'config view'*) printf '%s' '{"contexts":[{"name":"remote","context":{"cluster":"r"}}],"clusters":[{"name":"r","cluster":{"server":"https://remote.example.com"}}]}' ;;
 *api-resources*) printf 'agents.core.orka.ai\n' ;;
 *'get agents.core.orka.ai'*) printf '%s' '{"items":[{"metadata":{"name":"demo"},"spec":{"systemPrompt":{"configMapRef":{"name":"instructions","key":"prompt"}}}}]}' ;;
 *'get providers.core.orka.ai'*) printf '%s' '{"items":[]}' ;;
 *'get configmap instructions'*) printf '%s' '{"data":{"prompt":"Referenced system prompt"}}' ;;
 *) exit 1 ;;
esac`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	a := &App{Cfg: &config.Config{KubeContext: "different-configured-context"}, Run: &run.Runner{}}
	envs, err := a.agentTUIEnvironments(t.Context())
	if err != nil || len(envs) != 1 || envs[0].Kubeconfig != saved {
		t.Fatalf("envs=%+v err=%v", envs, err)
	}
	worker := envs[0].app(a)
	if worker.Cfg.KubeContext != "remote" || !strings.Contains(strings.Join(worker.Run.Env, " "), "KUBECONFIG="+saved) {
		t.Fatal("selected source routing lost")
	}
	agents, err := a.agentTUIInventory(t.Context(), envs[0], OrkaNamespace)
	if err != nil || len(agents) != 1 || agents[0].SystemPrompt != "Referenced system prompt" {
		t.Fatalf("agents=%+v err=%v", agents, err)
	}
}
