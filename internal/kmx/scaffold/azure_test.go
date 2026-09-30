package scaffold

import (
	"strings"
	"testing"
)

func TestAzureProviderGeneratesDeploymentWithoutImplicitVersion(t *testing.T) {
	s := orkaSpec()
	s.ProviderType, s.Model, s.AzureDeployment, s.BaseURL = "azure-openai", "chat-prod", "chat-prod", "https://example.openai.azure.com/"
	b, err := GenerateOrka(s)
	if err != nil {
		t.Fatal(err)
	}
	provider := b.Provider["spec"].(map[string]any)
	azure := provider["azure"].(map[string]any)
	if azure["deploymentName"] != "chat-prod" || provider["defaultModel"] != "chat-prod" {
		t.Fatalf("wrong deployment: %#v", provider)
	}
	if _, ok := azure["apiVersion"]; ok {
		t.Fatalf("implicit version: %#v", azure)
	}
	s.AzureAPIVersion = "2024-10-21"
	b, err = GenerateOrka(s)
	if err != nil {
		t.Fatal(err)
	}
	if got := b.Provider["spec"].(map[string]any)["azure"].(map[string]any)["apiVersion"]; got != "2024-10-21" {
		t.Fatalf("version: %v", got)
	}
}

func TestAzureProviderValidationBoundaries(t *testing.T) {
	base := orkaSpec()
	base.ProviderType, base.Model, base.AzureDeployment, base.BaseURL = "azure-openai", "deployment", "deployment", "https://example.openai.azure.com"
	cases := []struct {
		name   string
		mutate func(*OrkaSpec)
		want   string
	}{
		{"deployment", func(s *OrkaSpec) { s.AzureDeployment = "" }, "deployment"},
		{"model", func(s *OrkaSpec) { s.Model = "" }, "model"},
		{"resource root", func(s *OrkaSpec) { s.BaseURL = "" }, "baseURL"},
		{"compatible endpoint", func(s *OrkaSpec) { s.BaseURL += "/openai/v1/" }, "use openai"},
		{"non-root endpoint", func(s *OrkaSpec) { s.BaseURL += "/openai/deployments/chat-prod" }, "resource root"},
		{"escaped compatible endpoint", func(s *OrkaSpec) { s.BaseURL += "/%6fpenai/v1" }, "use openai"},
		{"different model", func(s *OrkaSpec) { s.Model = "gpt-4o" }, "deployment"},
		{"non-azure deployment", func(s *OrkaSpec) { s.ProviderType = "openai" }, "azure"},
		{"non-azure version", func(s *OrkaSpec) {
			s.ProviderType = "anthropic"
			s.AzureDeployment = ""
			s.AzureAPIVersion = "2024-10-21"
		}, "azure"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := base
			tc.mutate(&s)
			if _, err := GenerateOrka(s); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("missing %s refusal: %v", tc.want, err)
			}
		})
	}
	secretModel := "sk-" + strings.Repeat("a", 48)
	if err := ValidateOrkaProvider("azure-openai", secretModel, base.BaseURL, base.AzureDeployment, ""); err == nil || strings.Contains(err.Error(), secretModel) {
		t.Fatalf("credential-shaped model leaked in mismatch: %v", err)
	}
	b, err := GenerateOrka(base)
	if err != nil {
		t.Fatal(err)
	}
	delete(b.Provider["spec"].(map[string]any)["azure"].(map[string]any), "deploymentName")
	if err := b.Validate(); err == nil {
		t.Fatal("mutated Azure bundle accepted")
	}
	b, err = GenerateOrka(base)
	if err != nil {
		t.Fatal(err)
	}
	b.Provider["spec"].(map[string]any)["azure"].(map[string]any)["apiVersion"] = true
	if err := b.Validate(); err == nil {
		t.Fatal("malformed Azure API version accepted")
	}
}
