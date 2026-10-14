package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

func TestQuickstartWizardViewKeepsInfrastructureAboveNumberedAgentStep(t *testing.T) {
	create, err := newQuickstartSourceForm(CreateOptions{Model: "qwen2.5:3b"})
	if err != nil {
		t.Fatal(err)
	}
	m := quickstartWizardModel{create: create, setup: [6]string{"done", "done", "active", "pending", "pending", "pending"}, frame: 3,
		target: quickstartTarget{Context: "kind-demo", Source: "kmx ctx", Server: "127.0.0.1", Namespaces: "orka-system, ollama", Posture: "local kind"}}
	view := ansi.Strip(m.View().Content)
	for _, want := range []string{"kind-demo", "local kind", "Setup 2/6", "Model runtime", "3/4", "Description"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q:\n%s", want, view)
		}
	}
	if strings.Index(view, "Setup 2/6") > strings.Index(view, "Description") {
		t.Fatalf("infrastructure progress is not at the top:\n%s", view)
	}
}

func TestQuickstartWizardWaitsForSetupAfterReview(t *testing.T) {
	create, err := newQuickstartSourceForm(CreateOptions{
		Name: "demo", Description: "Demo", Namespace: OrkaNamespace,
		ProviderType: "openai", Model: "qwen2.5:3b", Secret: "kickstart-provider-key", InstructionText: "Answer briefly.",
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
	if !strings.Contains(got.View().Content, "Waiting for infrastructure") {
		t.Fatalf("waiting state is not visible:\n%s", got.View().Content)
	}
	_, cmd = got.Update(quickstartSetupEvent{step: -1, status: "complete"})
	if cmd == nil {
		t.Fatal("setup completion did not release the wizard")
	}
}

func TestCompletedSetupLeavesQueuedFrameWithoutWaitingForChannelClose(t *testing.T) {
	create, err := newQuickstartSourceForm(CreateOptions{Name: "demo", Description: "Demo", Namespace: OrkaNamespace, ProviderType: "openai", Model: "qwen2.5:3b", Secret: "key"})
	if err != nil {
		t.Fatal(err)
	}
	m := quickstartWizardModel{create: create, formDone: true, setup: [6]string{"done", "done", "done", "done", "done", "active"}}
	updated, cmd := m.Update(quickstartSetupEvent{step: 5, status: "done"})
	got := updated.(quickstartWizardModel)
	if cmd == nil || !got.setupDone || !got.setupComplete() {
		t.Fatalf("completed setup remained queued: done=%v complete=%v cmd=%v", got.setupDone, got.setupComplete(), cmd)
	}
}

func TestQuickstartViewReportsCompletedSetup(t *testing.T) {
	create, err := newQuickstartSourceForm(CreateOptions{Model: "qwen2.5:3b"})
	if err != nil {
		t.Fatal(err)
	}
	model := localModel{Provider: "bundled", Model: "qwen2.5:3b"}
	m := quickstartWizardModel{create: create, chosen: &model, setup: [6]string{"done", "done", "done", "done", "done", "done"}}
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "Setup 6/6") || !strings.Contains(view, "✓ ready") {
		t.Fatalf("completed setup is not visible:\n%s", view)
	}
}

func TestQuickstartProgressBarsDistinguishEveryState(t *testing.T) {
	states := []string{"pending", "active", "done", "failed", "waiting", "skipped"}
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

func TestQuickstartModelPickerShowsSelectedProviderDetails(t *testing.T) {
	models := []localModel{
		{Provider: "bundled", Model: "qwen2.5:3b"},
		{Provider: "ollama", Model: "qwen3:8b", Size: 8_000_000_000},
		{Provider: "copilot", Model: "auto"},
		{Provider: "foundry"},
		{Provider: "existing"},
	}
	for i, tc := range []struct {
		name string
		want []string
	}{
		{"bundled", []string{"› Local Orka Model (available to install)", "requires ~1.9 GB download"}},
		{"ollama", []string{"› (detected) qwen3:8b", "Ollama managed", "8.0 GB"}},
		{"copilot", []string{"› Copilot CLI - auto (detected)", "auto model · no local model download"}},
		{"foundry", []string{"› Azure Foundry (Azure login)", "Hosted model · local HTTP tools"}},
		{"existing", []string{"› Local Orka Model (detected)", "Uses this Agent's configured model and endpoint"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := quickstartWizardModel{width: 120, modelStep: true, detectDone: true, models: models, selection: i}
			view := ansi.Strip(m.View().Content)
			var detailRows []string
			for _, line := range strings.Split(view, "\n") {
				if !strings.Contains(line, "› ") {
					detailRows = append(detailRows, line)
				}
			}
			details := strings.Join(detailRows, "\n")
			for i, want := range tc.want {
				text := details
				if i == 0 {
					text = view
				}
				if !strings.Contains(text, want) {
					t.Fatalf("selected provider missing %q:\n%s", want, view)
				}
			}
		})
	}
}

func TestModelChoiceIgnoresQueuedEnterUntilScreenIsReady(t *testing.T) {
	create, err := newQuickstartSourceForm(CreateOptions{descriptionDefault: "Hello world agent"})
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
	create, err := newQuickstartSourceForm(CreateOptions{descriptionDefault: "Hello world agent"})
	if err != nil {
		t.Fatal(err)
	}
	m := quickstartWizardModel{create: create, modelStep: true, models: []localModel{{Provider: "bundled", Model: "qwen2.5:3b"}}}
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "Detecting models") || strings.Contains(view, "Local Orka Model") || strings.Contains(view, "› ") {
		t.Fatalf("model options appeared before detection completed:\n%s", view)
	}
}

func TestSingleDetectedOptionStillRequiresSelection(t *testing.T) {
	create, err := newQuickstartSourceForm(CreateOptions{descriptionDefault: "Hello world agent"})
	if err != nil {
		t.Fatal(err)
	}
	picks := make(chan *localModel, 1)
	fallback := localModel{Provider: "bundled", Model: "qwen2.5:3b"}
	m := quickstartWizardModel{create: create, modelStep: true, modelPick: picks, defaultModel: fallback}
	updated, _ := m.Update(quickstartSetupEvent{step: 1, status: "done", models: []localModel{fallback}})
	m = updated.(quickstartWizardModel)
	if !m.modelStep || m.chosen != nil || len(picks) != 0 {
		t.Fatal("single option was automatically selected")
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if updated.(quickstartWizardModel).modelStep || len(picks) != 1 {
		t.Fatal("explicit selection did not advance")
	}
}

func TestExistingAgentMustChooseNativeInferenceAfterDetection(t *testing.T) {
	create, err := newQuickstartSourceForm(CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	picks := make(chan *localModel, 1)
	m := quickstartWizardModel{create: create, agentStep: true, modelStep: true, selection: 1, modelPick: picks, existing: []quickstartExistingAgent{{Name: "existing", Namespace: OrkaNamespace}}}
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(quickstartWizardModel)
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(quickstartWizardModel)
	if len(picks) != 0 || m.formDone {
		t.Fatal("existing agent bypassed detection")
	}
	updated, _ = m.Update(quickstartSetupEvent{step: 0, status: "done", models: []localModel{{Provider: "bundled", Model: "local"}, {Provider: "copilot", Model: "fast"}}})
	m = updated.(quickstartWizardModel)
	if len(picks) != 0 || len(m.models) != 1 || m.models[0].Provider != "existing" {
		t.Fatal("detection auto-selected or changed existing Provider")
	}
	m.selection = 0
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(quickstartWizardModel)
	if !m.formDone || m.modelStep || (<-picks).Provider != "existing" {
		t.Fatal("explicit existing Provider choice lost")
	}
}

func TestDetectedAlternativesUnlockStableModelPicker(t *testing.T) {
	create, err := newQuickstartSourceForm(CreateOptions{descriptionDefault: "Hello world agent"})
	if err != nil {
		t.Fatal(err)
	}
	picks := make(chan *localModel, 1)
	models := []localModel{{Provider: "bundled", Model: "qwen2.5:3b"}, {Provider: "ollama", Model: "qwen3:8b", Size: 8_000_000_000}}
	m := quickstartWizardModel{create: create, modelStep: true, modelPick: picks}
	updated, _ := m.Update(quickstartSetupEvent{step: 1, status: "done", models: models})
	m = updated.(quickstartWizardModel)
	view := ansi.Strip(m.View().Content)
	if !m.modelStep || len(picks) != 0 || !strings.Contains(view, "Inference Provider") || !strings.Contains(view, "Local Orka Model") || !strings.Contains(view, "Ollama managed") {
		t.Fatalf("detected alternatives were not offered stably:\n%s", view)
	}
}

func TestQuickstartViewReportsCurrentSetupState(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup [6]string
		want  []string
	}{
		{"pending", [6]string{}, []string{"Setup 0/6", "Detect models", "pending"}},
		{"active", [6]string{"done", "done", "active"}, []string{"Setup 2/6", "Model runtime", "active"}},
		{"waiting", [6]string{"done", "done", "waiting"}, []string{"Setup 2/6", "Awaiting your choice", "waiting"}},
		{"failed", [6]string{"done", "done", "failed"}, []string{"Setup 2/6", "Model runtime", "failed"}},
		{"skipped", [6]string{"done", "done", "skipped", "active"}, []string{"Setup 3/6", "Download model", "active"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := quickstartWizardModel{setup: tc.setup}
			view := ansi.Strip(m.View().Content)
			for _, want := range tc.want {
				if !strings.Contains(view, want) {
					t.Fatalf("setup state missing %q:\n%s", want, view)
				}
			}
		})
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
	create, err := newQuickstartSourceForm(CreateOptions{descriptionDefault: "Hello world agent"})
	if err != nil {
		t.Fatal(err)
	}
	m := quickstartWizardModel{create: create, agentStep: true, modelStep: true,
		existing: []quickstartExistingAgent{{Name: "existing", Namespace: OrkaNamespace}},
		models:   []localModel{{Provider: "bundled", Model: "qwen2.5:3b"}}}
	view := ansi.Strip(m.View().Content)
	for _, want := range []string{"1/4", "Author a new suite workspace", `Use existing Agent existing`} {
		if !strings.Contains(view, want) {
			t.Fatalf("start choice missing %q:\n%s", want, view)
		}
	}
}

func TestQuickstartWizardVimKeysNavigateSelections(t *testing.T) {
	create, err := newQuickstartSourceForm(CreateOptions{descriptionDefault: "Hello world agent"})
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

func TestQuickstartWizardInterruptCancelsUIAndSetup(t *testing.T) {
	create, err := newQuickstartSourceForm(CreateOptions{descriptionDefault: "Hello world agent"})
	if err != nil {
		t.Fatal(err)
	}
	picks := make(chan *localModel, 1)
	cancelled := false
	m := quickstartWizardModel{create: create, agentStep: true, modelStep: true, modelPick: picks,
		defaultModel: localModel{Provider: "bundled", Model: "qwen2.5:3b"}, cancelSetup: func() { cancelled = true }}
	filtered := cancelQuickstartWizard(m, tea.InterruptMsg{})
	updated, cmd := m.Update(filtered)
	got := updated.(quickstartWizardModel)
	if !got.create.cancelled || !got.formDone || !cancelled || cmd == nil || len(picks) != 0 {
		t.Fatalf("interrupt did not cancel all owners: cancelled=%v formDone=%v setup=%v cmd=%v picks=%d", got.create.cancelled, got.formDone, cancelled, cmd, len(picks))
	}
}

func TestQuickstartWizardPanelsFitNarrowTerminal(t *testing.T) {
	create, err := newQuickstartSourceForm(CreateOptions{Model: "qwen2.5:3b"})
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
	create, err := newQuickstartSourceForm(CreateOptions{Model: "qwen2.5:3b"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		formDone bool
		setupErr error
		want     string
	}{
		{"editing", false, nil, "Description"},
		{"waiting", true, nil, "Waiting for infrastructure"},
		{"failed", true, errors.New("failed"), "Setup failed; restoring terminal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := quickstartWizardModel{create: create, width: 80, formDone: tc.formDone, setupErr: tc.setupErr}
			raw := m.View().Content
			view := ansi.Strip(raw)
			border := strings.Index(view, "╭")
			question := strings.Index(view, tc.want)
			if border < 0 || question < border || strings.Count(view, "╭") != 1 || strings.Count(view, "╯") != 1 {
				t.Fatalf("question is not the sole outlined section:\n%s", view)
			}
			if !strings.Contains(raw, "\x1b[34m╭") {
				t.Fatalf("question border is not dark blue:\n%s", raw)
			}
			setup := strings.Index(view, "Setup 0/6")
			if setup < 0 || setup > border {
				t.Fatalf("passive setup is not above the question:\n%s", view)
			}
		})
	}
}

func TestQuickstartDescriptionStartsWithHelloWorldAgent(t *testing.T) {
	m, err := newQuickstartSourceForm(CreateOptions{descriptionDefault: "Hello world agent"})
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
	for _, want := range []string{"setup complete (3/3)", "Ready", "NEXT STEP", "› Chat with agent", "agent:hello-world-agent", "location:current cluster"} {
		if !strings.Contains(view, want) {
			t.Fatalf("ready screen missing %q:\n%s", want, view)
		}
	}
	if lipgloss.Height(raw) > 8 {
		t.Fatalf("ready screen is too tall:\n%s", view)
	}
	firstBorder := strings.Index(view, "╭")
	readyText := strings.Index(view, "status: Ready")
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

func TestQuickstartCancellationAcrossScreens(t *testing.T) {
	for _, key := range []tea.KeyPressMsg{{Code: 'c', Mod: tea.ModCtrl}, {Code: tea.KeyEsc}} {
		for _, screen := range []string{"agent", "detecting", "models", "form", "waiting", "ready"} {
			t.Run(screen+"/"+key.String(), func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				create, err := newQuickstartSourceForm(CreateOptions{})
				if err != nil {
					t.Fatal(err)
				}
				m := quickstartWizardModel{create: create, cancelSetup: cancel}
				switch screen {
				case "agent":
					m.agentStep = true
				case "detecting":
					m.modelStep = true
				case "models":
					m.modelStep, m.detectDone = true, true
					m.models = []localModel{{Model: "test"}}
				case "waiting":
					m.formDone = true
				}
				var model tea.Model = m
				if screen == "ready" {
					model = quickstartReadyModel{}
				}
				updated, cmd := model.Update(key)
				if cmd == nil {
					t.Fatal("cancel key ignored")
				}
				if _, ok := cancelQuickstartWizard(updated, cmd()).(tea.QuitMsg); !ok {
					t.Fatal("quit was intercepted, preventing terminal restoration")
				}
				if screen != "ready" && ctx.Err() == nil {
					t.Fatal("background work was not cancelled")
				}
			})
		}
	}
}

func TestQuickstartNormalCompletionIsNotCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	picks := make(chan *localModel, 1)
	opt := CreateOptions{Namespace: OrkaNamespace, ProviderType: "openai", Model: "test", Secret: "key"}
	fallback := localModel{Provider: "bundled", Model: "test"}
	var out bytes.Buffer
	// Existing-agent selection avoids coupling this lifecycle test to form timing.
	in, writer := io.Pipe()
	defer in.Close()
	go func() {
		defer writer.Close()
		_, _ = io.WriteString(writer, "\r")
		time.Sleep(100 * time.Millisecond)
		_, _ = io.WriteString(writer, "\r")
	}()
	_, imported, cancelled, setupErr, err := runQuickstartWizard(in, &out, opt,
		[]quickstartExistingAgent{{Name: "existing", Namespace: OrkaNamespace}}, quickstartTarget{}, fallback, picks, cancel,
		func(report func(quickstartSetupEvent)) error {
			report(quickstartSetupEvent{step: 0, status: "done", models: []localModel{fallback}})
			select {
			case <-picks:
			case <-ctx.Done():
				return ctx.Err()
			}
			for i := 1; i < 6; i++ {
				report(quickstartSetupEvent{step: i, status: "done"})
			}
			return nil
		})
	if err != nil || setupErr != nil || cancelled || imported == nil {
		t.Fatalf("normal completion: imported=%v cancelled=%v setup=%v err=%v", imported, cancelled, setupErr, err)
	}
	if !strings.Contains(out.String(), "\x1b[?1049l") {
		t.Fatal("alternate screen not restored")
	}
}

func TestQuickstartCancellationDrainsSetupReporter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, _, cancelled, setupErr, err := runQuickstartWizard(strings.NewReader("\x03"), &bytes.Buffer{}, CreateOptions{}, nil,
		quickstartTarget{}, localModel{}, make(chan *localModel, 1), cancel, func(report func(quickstartSetupEvent)) error {
			<-ctx.Done()
			// More events than the buffer must never strand a worker after UI exit.
			for i := 0; i < 40; i++ {
				report(quickstartSetupEvent{step: 0, status: "failed"})
			}
			return ctx.Err()
		})
	if !cancelled || !errors.Is(setupErr, context.Canceled) || err != nil {
		t.Fatalf("cancelled=%v setup=%v err=%v", cancelled, setupErr, err)
	}
}

func TestQuickstartReadyCtrlCDoesNotStartChat(t *testing.T) {
	chat, err := runQuickstartReadyScreen(strings.NewReader("\x03"), &bytes.Buffer{}, "demo")
	if err != nil || chat {
		t.Fatalf("chat=%v err=%v", chat, err)
	}
}

type cancellingQuickstartDetector struct{ cancel context.CancelFunc }

func (d cancellingQuickstartDetector) Detect(ctx context.Context) ([]localModel, error) {
	d.cancel()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(time.Second):
		return nil, errors.New("detector did not inherit cancellation")
	}
}

func TestQuickstartDetectionCancellationStopsBeforeCluster(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := &App{Cfg: &config.Config{Model: "test"}, Run: &run.Runner{Context: ctx},
		localModels: &localModelEnvironment{Detectors: []localModelDetector{cancellingQuickstartDetector{cancel}}}}
	var events []quickstartSetupEvent
	err := a.quickstartWizardSetup(make(chan *localModel), func(event quickstartSetupEvent) { events = append(events, event) })
	if !errors.Is(err, context.Canceled) || len(events) != 1 || events[0].step != 0 || events[0].status != "active" {
		t.Fatalf("err=%v events=%+v", err, events)
	}
}

func TestQuickstartFailureExitsWithoutWaitingForForm(t *testing.T) {
	m := quickstartWizardModel{agentStep: true}
	updated, cmd := m.Update(quickstartSetupEvent{step: 1, status: "failed", err: errors.New("cluster failed")})
	if cmd == nil || updated.(quickstartWizardModel).setupErr == nil {
		t.Fatal("failed infrastructure left the form open")
	}
	if _, ok := cancelQuickstartWizard(updated, cmd()).(tea.QuitMsg); !ok {
		t.Fatal("failure exit became cancellation")
	}
}

func TestQuickstartSetupReportsSequentialWork(t *testing.T) {
	for _, route := range []string{"bundled", "host", "fallback"} {
		t.Run(route, func(t *testing.T) {
			f := newOrkaFixture(t, nil)
			// An empty listing exercises cluster creation, then serving checks.
			if err := os.WriteFile(filepath.Join(f.dir, "kind"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			a := f.app
			a.Cfg.Model = "bundled-model"
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			a.Run.Context = ctx
			detector := &staticLocalDetector{}
			a.localModels = &localModelEnvironment{Detectors: []localModelDetector{detector}, Verify: func(model *localModel) error {
				if route == "fallback" {
					return errors.New("unreachable")
				}
				model.Endpoint = "http://host:11434"
				return nil
			}}
			choice := &localModel{Provider: "bundled", Model: "bundled-model"}
			if route != "bundled" {
				choice = &localModel{Provider: "ollama", Model: "host-model"}
			}
			picks := make(chan *localModel, 1)
			picks <- choice
			var events []quickstartSetupEvent
			err := a.quickstartWizardSetup(picks, func(event quickstartSetupEvent) {
				events = append(events, event)
				if event.step == 4 && event.status == "active" {
					cancel() // All routing is now resolved; stop before inference.
				}
			})
			if err == nil || detector.calls != 1 {
				t.Fatalf("err=%v detector calls=%d", err, detector.calls)
			}
			last, active := -1, -1
			var resolved *localModel
			for _, event := range events {
				if event.step < last {
					t.Fatalf("progress moved backwards: %+v", events)
				}
				last = event.step
				if event.status == "active" {
					if active != -1 && active != event.step {
						t.Fatalf("overlapping animations: %+v", events)
					}
					active = event.step
				} else if event.status == "done" || event.status == "failed" {
					active = -1
				}
				if event.model != nil {
					resolved = event.model
				}
			}
			want := "bundled-model"
			if route == "host" {
				want = "host-model"
			}
			if resolved == nil || resolved.Model != want || choice.Endpoint != "" {
				t.Fatalf("resolved=%+v choice mutated=%+v", resolved, choice)
			}
			if route == "fallback" && !strings.Contains(events[6].note, "falling back") {
				t.Fatalf("fallback was not visible: %+v", events)
			}
		})
	}
}

func TestQuickstartInstallerDownloadRespectsCancellation(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := &App{Run: &run.Runner{Context: ctx}, Err: &bytes.Buffer{}, orkaInstaller: server.URL}
	done := make(chan error, 1)
	go func() { _, err := a.fetchOrkaInstaller(); done <- err }()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("installer download ignored cancellation")
	}
}

func TestQuickstartRerunDefaultsToExistingAgent(t *testing.T) {
	for _, tc := range []struct {
		agents []quickstartExistingAgent
		want   int
	}{
		{nil, 0},
		{[]quickstartExistingAgent{{Name: "custom"}}, 1},
		{[]quickstartExistingAgent{{Name: "custom"}, {Name: "hello-world-agent"}}, 2},
	} {
		if got := quickstartInitialAgentSelection(tc.agents); got != tc.want {
			t.Fatalf("selection=%d want=%d", got, tc.want)
		}
	}
}

func TestQuickstartHostModelKeepsSourceFreeOfProviderBindings(t *testing.T) {
	create, err := newQuickstartSourceForm(CreateOptions{Name: "demo", Description: "Demo", Namespace: OrkaNamespace, Model: "initial", InstructionText: "Report verified facts."})
	if err != nil {
		t.Fatal(err)
	}
	m := quickstartWizardModel{create: create, modelStep: true, modelPick: make(chan *localModel, 1)}
	choice := localModel{Provider: "ollama", Model: "host-model", Endpoint: "http://host.docker.internal:11434"}
	m.chooseModel(choice)
	choice.Endpoint = "http://172.18.0.1:11434"
	updated, _ := m.Update(quickstartSetupEvent{step: 2, status: "done", model: &choice})
	opt := updated.(quickstartWizardModel).create.opt
	if opt.Model != "host-model" || opt.BaseURL != "" || opt.Secret != "" || opt.Out != "" {
		t.Fatalf("Provider/bundle leaked into source options: %+v", opt)
	}
	if err := validateQuickstartSourceOptions(opt); err != nil {
		t.Fatal(err)
	}
}

func TestQuickstartDuplicateNameStaysInForm(t *testing.T) {
	create, err := newQuickstartSourceForm(CreateOptions{Namespace: OrkaNamespace, ProviderType: "openai", Model: "test", Secret: "key", descriptionDefault: "Hello world agent"})
	if err != nil {
		t.Fatal(err)
	}
	m := quickstartWizardModel{create: create, existing: []quickstartExistingAgent{{Name: "hello-world-agent", Namespace: OrkaNamespace}}}
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(quickstartWizardModel)
	if m.create.step != createName {
		t.Fatalf("step=%v", m.create.step)
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(quickstartWizardModel)
	if m.create.step != createName || m.formDone || m.create.err == nil || !strings.Contains(m.create.err.Error(), "already exists") {
		t.Fatalf("duplicate accepted: %+v", m.create)
	}
	m.create.input.SetValue("another-agent")
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(quickstartWizardModel)
	if m.create.step != quickstartInstructionsPurpose || m.create.err != nil {
		t.Fatalf("new name rejected: step=%v err=%v", m.create.step, m.create.err)
	}
}

func TestCompactQuickstartFitsSmallTerminalsAndKeepsSelection(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {60, 16}, {40, 12}, {28, 10}} {
		for _, screen := range []string{"agents", "models", "description", "review"} {
			t.Run(fmt.Sprintf("%dx%d/%s", size[0], size[1], screen), func(t *testing.T) {
				create, err := newQuickstartSourceForm(CreateOptions{Namespace: OrkaNamespace, Model: "qwen2.5:3b"})
				if err != nil {
					t.Fatal(err)
				}
				m := quickstartWizardModel{create: create, width: size[0], height: size[1], setup: [6]string{"done", "done", "waiting"}, target: quickstartTarget{Context: "kind-kaimahi-p1", Posture: "local kind"}}
				switch screen {
				case "agents":
					m.agentStep = true
					for i := 0; i < 30; i++ {
						m.existing = append(m.existing, quickstartExistingAgent{Name: fmt.Sprintf("agent-%02d", i)})
					}
					m.selection = 25
				case "models":
					m.modelStep, m.detectDone = true, true
					for i := 0; i < 30; i++ {
						m.models = append(m.models, localModel{Provider: "ollama", Model: fmt.Sprintf("model-%02d", i)})
					}
					m.selection = 25
				case "review":
					m.create.step = createConfirm
				}
				view := m.View().Content
				if lipgloss.Height(view) > size[1] {
					t.Fatalf("too tall: %d > %d\n%s", lipgloss.Height(view), size[1], view)
				}
				for _, line := range strings.Split(view, "\n") {
					if lipgloss.Width(line) > size[0] {
						t.Fatalf("too wide: %d > %d\n%s", lipgloss.Width(line), size[0], view)
					}
				}
				if screen == "agents" || screen == "models" {
					if !strings.Contains(view, "› ") {
						t.Fatalf("selection hidden:\n%s", view)
					}
				}
				if screen == "description" && !strings.Contains(view, "> ") {
					t.Fatalf("input hidden:\n%s", view)
				}
				if screen == "review" && !strings.Contains(view, "Author source") {
					t.Fatalf("review choice hidden:\n%s", view)
				}
			})
		}
	}
}
