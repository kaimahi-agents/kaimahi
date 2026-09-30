package app

import (
	"bufio"
	"bytes"
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func azureCreateOptions() CreateOptions {
	return CreateOptions{Name: "sample", Description: "Azure agent", Namespace: "orka-system", ProviderType: "azure-openai", Model: "chat-prod", AzureDeployment: "chat-prod", BaseURL: "https://example.openai.azure.com", Secret: "model-key", NoApply: true}
}

func TestAzureCreatePortableBindingsAndRender(t *testing.T) {
	opt := azureCreateOptions()
	portable, err := portableOrkaSource(opt)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(portable, []byte("azure")) || bytes.Contains(portable, []byte("openai.azure.com")) {
		t.Fatalf("target bindings leaked to portable revision: %s", portable)
	}
	bindings := orkaBindingsFromCreate(opt)
	if bindings.Provider.Azure.DeploymentName != "chat-prod" {
		t.Fatalf("lost deployment: %#v", bindings)
	}
	rendered, err := (&orkaRuntimeAdapter{create: &opt, bindings: &bindings}).Render(context.Background(), portable, agentruntime.RenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := orkaBundleFromRendered(rendered)
	if err != nil {
		t.Fatal(err)
	}
	azure := bundle.Provider["spec"].(map[string]any)["azure"].(map[string]any)
	if azure["deploymentName"] != "chat-prod" {
		t.Fatalf("lost deployment in Provider: %v", azure)
	}
	if _, ok := azure["apiVersion"]; ok {
		t.Fatal("unexpected default API version")
	}
}

func TestAzureLineWizardCollectsRequiredFields(t *testing.T) {
	opt := azureCreateOptions()
	opt.AzureDeployment, opt.BaseURL, opt.AzureAPIVersion = "", "", ""
	var out bytes.Buffer
	completed, err := collectCreateOptions(bufio.NewScanner(strings.NewReader("\nchat-prod\nhttps://example.openai.azure.com\n\n")), &out, opt)
	if err != nil {
		t.Fatal(err)
	}
	if completed.AzureDeployment != "chat-prod" || completed.BaseURL != "https://example.openai.azure.com" || completed.AzureAPIVersion != "" {
		t.Fatalf("wizard lost fields: %+v", completed)
	}
}

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

func TestAzureBubbleWizardCollectsRequiredFields(t *testing.T) {
	opt := azureCreateOptions()
	opt.AzureDeployment, opt.BaseURL = "", ""
	wizard, err := newCreateWizardModel(opt)
	if err != nil {
		t.Fatal(err)
	}
	if wizard.step != createAzureDeployment {
		t.Fatalf("first missing field step: %v", wizard.step)
	}
	wizard.input.SetValue("chat-prod")
	model, _ := wizard.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	wizard = model.(createWizardModel)
	if wizard.step != createAzureBaseURL {
		t.Fatalf("second missing field step: %v", wizard.step)
	}
	wizard.input.SetValue("https://example.openai.azure.com")
	model, _ = wizard.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	wizard = model.(createWizardModel)
	if wizard.step != createAzureAPIVersion || wizard.err != nil || wizard.opt.AzureDeployment != "chat-prod" || wizard.opt.BaseURL != "https://example.openai.azure.com" {
		t.Fatalf("wizard: %+v", wizard)
	}
	model, _ = wizard.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	wizard = model.(createWizardModel)
	if wizard.step != createDone || wizard.err != nil || wizard.opt.AzureAPIVersion != "" {
		t.Fatalf("optional version was not omitted: %+v", wizard)
	}
}
