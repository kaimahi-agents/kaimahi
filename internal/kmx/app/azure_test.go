package app

import (
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

func TestAzureAPIKeyConnectorReadsDeploymentAndVersion(t *testing.T) {
	pane := &consoleInferencePane{}
	pane.setFields("apikey")
	pane.fields[0].input.SetValue("azure-openai")
	pane.ensureAzureFields()
	for i, value := range []string{"azure-openai", "https://example.openai.azure.com", "chat-prod", "target-key", "api-key", "chat-prod", "2024-10-21"} {
		if i >= len(pane.fields) {
			t.Fatalf("connector lacks Azure field %d", i)
		}
		pane.fields[i].input.SetValue(value)
	}
	if err := pane.readFields(); err != nil {
		t.Fatal(err)
	}
	if pane.source.AzureDeployment != "chat-prod" || pane.source.AzureAPIVersion != "2024-10-21" {
		t.Fatalf("lost Azure connector options: %+v", pane.source)
	}
	pane.fields[1].input.SetValue("https://example.openai.azure.com/openai/v1")
	if err := pane.readFields(); err == nil || !strings.Contains(err.Error(), "use openai") {
		t.Fatalf("v1 endpoint accepted: %v", err)
	}
}

func TestConsoleAzureClusterEditingRetired(t *testing.T) {
	a := &App{Cfg: &config.Config{}, Run: &run.Runner{}}
	env := agentTUIEnvironment{Name: "test"}
	agent := agentTUIAgent{Name: "sample", Namespace: "orka-system"}
	for _, tc := range []struct {
		name, providerDefault, override string
	}{
		{"override", "chat-prod", "other-deployment"},
		{"provider default", "other-deployment", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := consoleInferenceSource{Kind: "cluster", Name: "azure", Provider: "azure-openai", Model: tc.providerDefault, AzureDeployment: "chat-prod", Endpoint: "https://example.openai.azure.com"}
			err := a.consoleSaveInference(t.Context(), env, agent, consoleInferenceSnapshot{}, source, tc.override)
			if err == nil || !strings.Contains(err.Error(), "authored member/workspace") || !strings.Contains(err.Error(), "eval → push → lift") {
				t.Fatalf("retired Provider edit not refused before patch: %v", err)
			}
		})
	}
}
