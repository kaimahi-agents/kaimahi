package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func TestAzureLiftPreservesReadyDestinationAndRefusesDifferentDeployment(t *testing.T) {
	for _, tc := range []struct {
		name, deployment string
		plan, accepted   bool
	}{
		{"matching-plan", "gpt-4o-mini", true, true},
		{"matching-lift", "gpt-4o-mini", false, true},
		{"mismatch-plan", "chat-prod", true, false},
		{"mismatch-lift", "chat-prod", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, opt, dir, _ := liftBundleFixture(t)
			t.Setenv("KMX_LIFT_SELECTED_AZURE", "1")
			t.Setenv("KMX_LIFT_AZURE_DEPLOYMENT", tc.deployment)
			opt.Plan = tc.plan
			err := a.LiftAgentBundle(opt)
			if !tc.accepted {
				if err == nil || !strings.Contains(err.Error(), "gpt-4o-mini") || !strings.Contains(err.Error(), "chat-prod") || !strings.Contains(err.Error(), "evaluation") {
					t.Fatalf("mismatched deployment not explained: %v", err)
				}
				assertNoLiftWrites(t, dir, opt.BundleDir)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, call := range orkaCalls(t, dir) {
				if call.Document == nil || call.Document["kind"] != "Provider" {
					continue
				}
				spec := call.Document["spec"].(map[string]any)
				azure, ok := spec["azure"].(map[string]any)
				if !ok || azure["deploymentName"] != tc.deployment || azure["apiVersion"] != "2024-02-15-preview" || spec["defaultModel"] != tc.deployment {
					t.Fatalf("Azure destination not retained: %#v", spec)
				}
				found = true
			}
			if !found {
				t.Fatal("no rendered Provider inspected")
			}
		})
	}
}

func TestAzureStatusIgnoresServerDefaultedAPIVersion(t *testing.T) {
	a, opt, dir, _, name := bundleStatusFixture(t)
	binding := agentruntime.OrkaBindings{Namespace: "orka-system", Provider: agentruntime.OrkaProviderBindings{
		Type: "azure-openai", BaseURL: "https://example.openai.azure.com", Azure: agentruntime.OrkaAzureBindings{DeploymentName: "gpt-4o-mini"}, SecretRef: agentruntime.OrkaSecretRefBindings{Name: "model-key", Key: "api-key"},
	}}
	data, err := agentruntime.EncodeOrkaBindings(binding)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(opt.BundleDir, "bindings.yaml"), data, 0600); err != nil {
		t.Fatal(err)
	}
	rendered, err := RenderOrkaBundleFile(opt.BundleDir, binding)
	if err != nil {
		t.Fatal(err)
	}
	seedBundleLiveResources(t, dir, rendered, name, nil)
	raw, err := os.ReadFile(filepath.Join(dir, "providers.core.orka.ai.json"))
	if err != nil {
		t.Fatal(err)
	}
	var provider map[string]any
	if err := json.Unmarshal(raw, &provider); err != nil {
		t.Fatal(err)
	}
	provider["spec"].(map[string]any)["azure"].(map[string]any)["apiVersion"] = "2024-02-15-preview"
	seedReconcile(t, dir, provider)
	opt.Context, opt.Namespace = "kind-test", "orka-system"
	report, err := a.bundleStatusReport(opt)
	if err != nil {
		t.Fatal(err)
	}
	if report.Targets[0].State != bundleStateInSync {
		t.Fatalf("server default counted as drift: %+v", report.Targets[0])
	}
}

func TestAzureLegacyLiftRefusesSourceModelMismatch(t *testing.T) {
	source, err := createOrkaBundle(azureCreateOptions())
	if err != nil {
		t.Fatal(err)
	}
	source.Agent["spec"].(map[string]any)["model"] = map[string]any{"name": "other-deployment"}
	if _, err := portableLiftBundle(source.Agent, source.Provider, "sample", "orka-system"); err == nil || !strings.Contains(err.Error(), "other-deployment") || !strings.Contains(err.Error(), "chat-prod") {
		t.Fatalf("source model override accepted: %v", err)
	}
	delete(source.Agent["spec"].(map[string]any), "model")
	destination := map[string]any{"type": "azure-openai", "baseURL": "https://example.openai.azure.com", "defaultModel": "chat-prod", "azure": map[string]any{"deploymentName": "other-deployment"}, "secretRef": map[string]any{"name": "target-key", "key": "api-key"}}
	if err := useLiftProvider(source, destination); err == nil || !strings.Contains(err.Error(), "chat-prod") || !strings.Contains(err.Error(), "other-deployment") {
		t.Fatalf("selected Provider changed the source model: %v", err)
	}
}
