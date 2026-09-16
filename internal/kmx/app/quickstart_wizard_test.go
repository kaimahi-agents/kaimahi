package app

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestQuickstartWizardViewKeepsInfrastructureAboveNumberedAgentStep(t *testing.T) {
	create, err := newCreateWizardModel(CreateOptions{Model: "qwen2.5:3b"})
	if err != nil {
		t.Fatal(err)
	}
	m := quickstartWizardModel{create: create, setup: [6]string{"done", "done", "active", "pending", "pending", "pending"}, frame: 3,
		target: quickstartTarget{Context: "kind-demo", Source: "kmx ctx", Server: "127.0.0.1", Namespaces: "orka-system, ollama", Posture: "local kind"}}
	view := ansi.Strip(m.View().Content)
	for _, want := range []string{"TARGET & INFRASTRUCTURE", "kind-demo", "kmx ctx", "127.0.0.1", "local kind", "Kind cluster", "Detecting models", "Ollama image", "Model qwen2.5:3b", "Loading model", "Orka runtime", "AGENT SETUP", "STEP 2 OF 8", "Description"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q:\n%s", want, view)
		}
	}
	if strings.Index(view, "INFRASTRUCTURE") > strings.Index(view, "AGENT SETUP") {
		t.Fatalf("infrastructure progress is not at the top:\n%s", view)
	}
}

func TestQuickstartWizardWaitsForSetupAfterReview(t *testing.T) {
	create, err := newCreateWizardModel(CreateOptions{
		Name: "demo", Description: "Demo", Namespace: OrkaNamespace,
		ProviderType: "openai", Model: "qwen2.5:3b", Secret: "kickstart-provider-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	events := make(chan quickstartSetupEvent)
	m := quickstartWizardModel{create: create, events: events}
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := updated.(quickstartWizardModel)
	if cmd != nil || !got.formDone || got.setupDone {
		t.Fatalf("review did not wait for setup: formDone=%v setupDone=%v cmd=%v", got.formDone, got.setupDone, cmd)
	}
	if !strings.Contains(got.View().Content, `Agent "demo" is queued. Waiting for the model and runtime`) {
		t.Fatalf("waiting state is not visible:\n%s", got.View().Content)
	}
	_, cmd = got.Update(quickstartSetupEvent{step: -1, status: "complete"})
	if cmd == nil {
		t.Fatal("setup completion did not release the wizard")
	}
}

func TestQuickstartProgressBarsDistinguishEveryState(t *testing.T) {
	states := []string{"pending", "active", "done", "failed"}
	seen := map[string]bool{}
	for _, state := range states {
		bar := quickstartProgressBar(state, 4)
		if len(bar) != 20 || seen[bar] {
			t.Fatalf("state %q has invalid or duplicate bar %q", state, bar)
		}
		seen[bar] = true
	}
}

func TestQuickstartWizardRefusesNoninteractiveInputBeforeSetup(t *testing.T) {
	a := &App{}
	err := a.QuickstartWizard(QuickstartWizardOptions{})
	if err == nil || !strings.Contains(err.Error(), "requires an interactive terminal") {
		t.Fatalf("error=%v", err)
	}
}

func TestQuickstartModelChoicesShowSourceAndReportedSize(t *testing.T) {
	if got := quickstartModelLabel(localModel{Provider: "bundled", Model: "qwen2.5:3b"}); !strings.Contains(got, "KMX managed") || !strings.Contains(got, "download") {
		t.Fatalf("bundled label=%q", got)
	}
	got := quickstartModelLabel(localModel{Provider: "ollama", Model: "qwen3:8b", Size: 8_000_000_000})
	for _, want := range []string{"qwen3:8b", "Ollama managed", "8.0 GB"} {
		if !strings.Contains(got, want) {
			t.Fatalf("host label %q missing %q", got, want)
		}
	}
}

func TestBundledModelChoiceSaysWhenDownloadAlreadyFinished(t *testing.T) {
	model := localModel{Provider: "bundled", Model: "qwen2.5:3b"}
	m := quickstartWizardModel{}
	if got := m.quickstartModelChoiceLabel(model); !strings.Contains(got, "download during setup") {
		t.Fatalf("pending label=%q", got)
	}
	m.setup[3] = "done"
	if got := m.quickstartModelChoiceLabel(model); got != "qwen2.5:3b (KMX managed, already downloaded)" {
		t.Fatalf("completed label=%q", got)
	}
}

func TestModelChoiceIgnoresQueuedEnterUntilScreenIsReady(t *testing.T) {
	create, err := newCreateWizardModel(CreateOptions{descriptionDefault: "Hello world agent"})
	if err != nil {
		t.Fatal(err)
	}
	picks := make(chan *localModel, 1)
	m := quickstartWizardModel{create: create, agentStep: true, modelStep: true, modelPick: picks,
		models: []localModel{{Provider: "bundled", Model: "qwen2.5:3b"}, {Provider: "ollama", Model: "qwen3:8b"}}}
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(quickstartWizardModel)
	if cmd != nil || !m.modelStep || m.chosen != nil {
		t.Fatalf("start transition skipped model choice: modelStep=%v chosen=%#v", m.modelStep, m.chosen)
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(quickstartWizardModel)
	if m.chosen != nil || len(picks) != 0 {
		t.Fatal("queued Enter selected a model before the choice screen became ready")
	}
	updated, _ = m.Update(quickstartSetupEvent{step: 1, status: "done", models: m.models})
	m = updated.(quickstartWizardModel)
	m.selection = 1
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(quickstartWizardModel)
	if m.chosen == nil || m.chosen.Model != "qwen3:8b" || len(picks) != 1 {
		t.Fatalf("explicit selection was not retained: %#v", m.chosen)
	}
}

func TestModelStepShowsNoOptionsUntilDetectionCompletes(t *testing.T) {
	create, err := newCreateWizardModel(CreateOptions{descriptionDefault: "Hello world agent"})
	if err != nil {
		t.Fatal(err)
	}
	m := quickstartWizardModel{create: create, modelStep: true}
	view := ansi.Strip(m.agentPanel(80))
	if !strings.Contains(view, "Detecting models") || strings.Contains(view, "Choose a model") || strings.Contains(view, "KMX managed") {
		t.Fatalf("model options appeared before detection completed:\n%s", view)
	}
}

func TestNoDetectedAlternativeSkipsModelQuestion(t *testing.T) {
	create, err := newCreateWizardModel(CreateOptions{descriptionDefault: "Hello world agent"})
	if err != nil {
		t.Fatal(err)
	}
	picks := make(chan *localModel, 1)
	fallback := localModel{Provider: "bundled", Model: "qwen2.5:3b"}
	m := quickstartWizardModel{create: create, modelStep: true, modelPick: picks, defaultModel: fallback}
	updated, _ := m.Update(quickstartSetupEvent{step: 1, status: "done", models: []localModel{fallback}})
	m = updated.(quickstartWizardModel)
	if m.modelStep || m.chosen == nil || m.chosen.Model != fallback.Model || len(picks) != 1 || m.create.step != createDescription {
		t.Fatalf("single default did not auto-advance: modelStep=%v chosen=%#v step=%d picks=%d", m.modelStep, m.chosen, m.create.step, len(picks))
	}
}

func TestDetectedAlternativesUnlockStableModelPicker(t *testing.T) {
	create, err := newCreateWizardModel(CreateOptions{descriptionDefault: "Hello world agent"})
	if err != nil {
		t.Fatal(err)
	}
	picks := make(chan *localModel, 1)
	models := []localModel{{Provider: "bundled", Model: "qwen2.5:3b"}, {Provider: "ollama", Model: "qwen3:8b", Size: 8_000_000_000}}
	m := quickstartWizardModel{create: create, modelStep: true, modelPick: picks}
	updated, _ := m.Update(quickstartSetupEvent{step: 1, status: "done", models: models})
	m = updated.(quickstartWizardModel)
	view := ansi.Strip(m.agentPanel(80))
	if !m.modelStep || len(picks) != 0 || !strings.Contains(view, "Choose a model") || !strings.Contains(view, "KMX managed") || !strings.Contains(view, "Ollama managed") {
		t.Fatalf("detected alternatives were not offered stably:\n%s", view)
	}
}

func TestQuickstartInfrastructureRowsExplainEstimatedSetupSize(t *testing.T) {
	m := quickstartWizardModel{models: []localModel{{Provider: "bundled", Model: "qwen2.5:3b"}}}
	want := []string{"~1.3 GB node image", "host runtimes", "~1.1 GB image", "~1.9 GB model", "weights into memory", "~860 MB images"}
	for step, detail := range want {
		if got := m.infrastructureSize(step); got != detail {
			t.Fatalf("step %d detail=%q, want %q", step, got, detail)
		}
	}
	m.chosen = &localModel{Provider: "ollama", Model: "qwen3:8b", Size: 8_000_000_000}
	if got := m.infrastructureSize(2); got != "skipped; host runtime" {
		t.Fatalf("host runtime detail=%q", got)
	}
	if got := m.infrastructureSize(3); got != "8.0 GB already installed" {
		t.Fatalf("host model detail=%q", got)
	}
}

func TestManagedOllamaKeepsPrewarmedModelResident(t *testing.T) {
	body, err := manifest("ollama.yaml")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, "name: OLLAMA_KEEP_ALIVE") || !strings.Contains(text, `value: "1h"`) {
		t.Fatalf("managed Ollama does not retain prewarmed weights:\n%s", text)
	}
}

func TestQuickstartStartsWithExistingAgentOrNewChoice(t *testing.T) {
	create, err := newCreateWizardModel(CreateOptions{descriptionDefault: "Hello world agent"})
	if err != nil {
		t.Fatal(err)
	}
	m := quickstartWizardModel{create: create, agentStep: true, modelStep: true,
		existing: []quickstartExistingAgent{{Name: "existing", Namespace: OrkaNamespace}},
		models:   []localModel{{Provider: "bundled", Model: "qwen2.5:3b"}}}
	view := ansi.Strip(m.View().Content)
	for _, want := range []string{"AGENT SETUP", "STEP 1 OF 8", "Create a new agent", `Use existing Agent "existing" (orka-system)`} {
		if !strings.Contains(view, want) {
			t.Fatalf("start choice missing %q:\n%s", want, view)
		}
	}
}

func TestQuickstartWizardVimKeysNavigateSelections(t *testing.T) {
	create, err := newCreateWizardModel(CreateOptions{descriptionDefault: "Hello world agent"})
	if err != nil {
		t.Fatal(err)
	}
	m := quickstartWizardModel{create: create, agentStep: true, modelStep: true,
		existing: []quickstartExistingAgent{{Name: "existing", Namespace: OrkaNamespace}},
		models:   []localModel{{Provider: "bundled", Model: "qwen2.5:3b"}}}
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'j'})
	m = updated.(quickstartWizardModel)
	if m.selection != 1 {
		t.Fatalf("j selection=%d, want 1", m.selection)
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: 'k'})
	if got := updated.(quickstartWizardModel).selection; got != 0 {
		t.Fatalf("k selection=%d, want 0", got)
	}

	ready := quickstartReadyModel{name: "existing"}
	updatedReady, _ := ready.Update(tea.KeyPressMsg{Code: 'j'})
	if got := updatedReady.(quickstartReadyModel).selection; got != 1 {
		t.Fatalf("ready j selection=%d, want 1", got)
	}
	updatedReady, _ = updatedReady.(quickstartReadyModel).Update(tea.KeyPressMsg{Code: 'k'})
	if got := updatedReady.(quickstartReadyModel).selection; got != 0 {
		t.Fatalf("ready k selection=%d, want 0", got)
	}
}

func TestQuickstartWizardPanelsFitNarrowTerminal(t *testing.T) {
	create, err := newCreateWizardModel(CreateOptions{Model: "qwen2.5:3b"})
	if err != nil {
		t.Fatal(err)
	}
	m := quickstartWizardModel{create: create, width: 52, setup: [6]string{"done", "active"}}
	for _, line := range strings.Split(m.View().Content, "\n") {
		if got := lipgloss.Width(line); got > 52 {
			t.Fatalf("narrow view line is %d cells wide: %q", got, ansi.Strip(line))
		}
	}
}

func TestQuickstartWizardOnlyFocusedQuestionHasBorder(t *testing.T) {
	create, err := newCreateWizardModel(CreateOptions{Model: "qwen2.5:3b"})
	if err != nil {
		t.Fatal(err)
	}
	m := quickstartWizardModel{create: create, width: 80}
	if section := m.infrastructurePanel(78); strings.Contains(ansi.Strip(section), "╭") {
		t.Fatalf("passive infrastructure is outlined:\n%s", section)
	}
	if panel := m.agentPanel(78); !strings.Contains(panel, "\x1b[35m╭") {
		t.Fatalf("focused agent border is not magenta:\n%s", panel)
	}
	m.formDone = true
	if section := m.agentPanel(78); strings.Contains(ansi.Strip(section), "╭") {
		t.Fatalf("waiting agent section is outlined:\n%s", section)
	}
	m.setupErr = fmt.Errorf("failed")
	if section := m.agentPanel(78); strings.Contains(ansi.Strip(section), "╭") || !strings.Contains(section, "\x1b[31m") {
		t.Fatalf("failed agent section has wrong focus treatment:\n%s", section)
	}
}

func TestQuickstartDescriptionStartsWithHelloWorldAgent(t *testing.T) {
	m, err := newCreateWizardModel(CreateOptions{descriptionDefault: "Hello world agent"})
	if err != nil {
		t.Fatal(err)
	}
	if m.step != createDescription || m.input.Value() != "Hello world agent" {
		t.Fatalf("step=%d value=%q", m.step, m.input.Value())
	}
}

func TestQuickstartReadyScreenDefaultsToChat(t *testing.T) {
	m := quickstartReadyModel{name: "hello-world-agent"}
	raw := m.View().Content
	view := ansi.Strip(raw)
	for _, want := range []string{"QUICKSTART COMPLETE", "READY", "NEXT STEP", "› Chat with agent", `Agent "hello-world-agent" is ready`} {
		if !strings.Contains(view, want) {
			t.Fatalf("ready screen missing %q:\n%s", want, view)
		}
	}
	if !strings.Contains(view, "The model and Orka runtime are available") {
		t.Fatalf("ready screen does not continue into chat by default:\n%s", view)
	}
	firstBorder := strings.Index(view, "╭")
	readyText := strings.Index(view, `Agent "hello-world-agent" is ready`)
	if firstBorder < 0 || readyText < 0 || readyText > firstBorder || strings.Count(view, "╭") != 1 || strings.Count(view, "╯") != 1 {
		t.Fatalf("passive READY is outlined or focused NEXT STEP is not unique:\n%s", view)
	}
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil || updated.(quickstartReadyModel).selection != 0 {
		t.Fatal("default ready action did not select chat")
	}
}

func TestQuickstartReadyScreenFitsNarrowTerminal(t *testing.T) {
	m := quickstartReadyModel{name: "hello-world-agent", width: 44}
	for _, line := range strings.Split(m.View().Content, "\n") {
		if got := lipgloss.Width(line); got > 44 {
			t.Fatalf("ready screen line is %d cells wide: %q", got, ansi.Strip(line))
		}
	}
}

func TestQuickstartReadyScreenRestoresAlternateScreenBeforeChat(t *testing.T) {
	var out bytes.Buffer
	chat, err := runQuickstartReadyScreen(strings.NewReader("\r"), &out, "hello-world-agent")
	if err != nil {
		t.Fatal(err)
	}
	if !chat {
		t.Fatal("default completion action was not chat")
	}
	rendered := out.String()
	if !strings.Contains(rendered, "\x1b[?1049h") || !strings.Contains(rendered, "\x1b[?1049l") {
		t.Fatalf("completion did not enter and restore the alternate screen: %q", rendered)
	}
}

func TestStableTerminalSizeIsNoopForNonterminal(t *testing.T) {
	if err := waitForStableTerminalSize(nil, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
}
