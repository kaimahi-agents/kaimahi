package app

import (
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestQuickstartWizardRetiredRequestsRefuseBeforeSetup(t *testing.T) {
	for _, tc := range []struct {
		name   string
		opt    QuickstartWizardOptions
		remedy []string
	}{
		{"foundry", QuickstartWizardOptions{Inference: "foundry"}, []string{"Ready", "Orka", "openai", "exact model", "eval", "push", "lift"}},
		{"copilot", QuickstartWizardOptions{Inference: "copilot"}, []string{"Ready", "Orka", "openai", "exact model", "eval", "push", "lift"}},
		{"tools", QuickstartWizardOptions{Create: CreateOptions{Tools: "k8s-get-resources"}}, []string{"ToolProvider", "suite", "future"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newOrkaFixture(t, nil)
			err := f.app.QuickstartWizard(tc.opt)
			if err == nil {
				t.Fatal("retired request accepted")
			}
			for _, want := range tc.remedy {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("missing remedy %q: %v", want, err)
				}
			}
			if calls := f.calls(t); calls != "" {
				t.Fatalf("commands before refusal: %s", calls)
			}
		})
	}
}

func TestQuickstartWizardHostSelectionRefusesBeforeCluster(t *testing.T) {
	for _, provider := range []string{"foundry", "copilot"} {
		t.Run(provider, func(t *testing.T) {
			f := newOrkaFixture(t, nil)
			f.app.localModels = &localModelEnvironment{Detectors: []localModelDetector{}}
			picks := make(chan *localModel, 1)
			picks <- &localModel{Provider: provider, Model: "host-model"}
			err := f.app.quickstartWizardSetup(picks, func(quickstartSetupEvent) {})
			if err == nil || !strings.Contains(err.Error(), "openai") {
				t.Errorf("wrong refusal: %v", err)
			}
			if calls := f.calls(t); calls != "" {
				t.Fatalf("setup before refusal: %s", calls)
			}
		})
	}
}

func TestQuickstartWizardOrkaDoesNotInstallKubernetesTools(t *testing.T) {
	f := newOrkaFixture(t, orkaChart(t))
	if err := os.WriteFile(f.stdin, []byte("kind: Provider\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f.app.orkaInstallerDigest = digestOf(orkaChart(t))
	if err := f.app.quickstartWizardOrka(func(quickstartSetupEvent) {}); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"kmx-k8s-tool", "k8s-get-resources", "orka-ai-worker"} {
		if strings.Contains(f.applied(t), forbidden) {
			t.Errorf("automatic tool/grant installed: %s", forbidden)
		}
	}
	if strings.Contains(f.calls(t), "tools.core.orka.ai") {
		t.Fatalf("automatic tool setup: %s", f.calls(t))
	}
}

func TestQuickstartWizardDetectionExcludesHostModes(t *testing.T) {
	f := newOrkaFixture(t, nil)
	f.app.localModels = &localModelEnvironment{Detectors: []localModelDetector{}}
	for _, model := range f.app.quickstartWizardModels() {
		if model.Provider == "foundry" || model.Provider == "copilot" {
			t.Fatalf("retired choice detected: %+v", model)
		}
	}
}

func TestQuickstartWizardExistingAgentKeepsNativeProvider(t *testing.T) {
	picks := make(chan *localModel, 1)
	m := quickstartWizardModel{modelStep: true, detectDone: true, modelPick: picks, imported: &quickstartExistingAgent{Name: "existing", Namespace: OrkaNamespace},
		models: []localModel{{Provider: "bundled"}, {Provider: "copilot"}, {Provider: "foundry"}}}
	m.filterInferenceChoices()
	if len(m.models) != 1 || m.models[0].Provider != "existing" {
		t.Fatalf("existing agent offers mutation/host choices: %+v", m.models)
	}
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(quickstartWizardModel)
	if !m.formDone || m.modelStep || len(picks) != 1 {
		t.Fatal("native existing-agent choice did not proceed to chat")
	}
}
