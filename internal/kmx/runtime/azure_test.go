package runtime

import (
	"strings"
	"testing"
)

func TestAzureBindingsRoundTripAndRejectMissingDeployment(t *testing.T) {
	yaml := strings.Replace(validOrkaBindingsYAML, "  type: openai\n", "  type: azure-openai\n  azure:\n    deploymentName: chat-prod\n", 1)
	bindings, err := ParseOrkaBindings([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	if bindings.Provider.Azure.DeploymentName != "chat-prod" || bindings.Provider.Azure.APIVersion != "" {
		t.Fatalf("lost Azure bindings: %#v", bindings.Provider)
	}
	encoded, err := EncodeOrkaBindings(bindings)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "apiVersion: 202") {
		t.Fatalf("default version emitted: %s", encoded)
	}
	if _, err := ParseOrkaBindings(encoded); err != nil {
		t.Fatal(err)
	}
	invalid := strings.Replace(yaml, "    deploymentName: chat-prod\n", "", 1)
	if _, err := ParseOrkaBindings([]byte(invalid)); err == nil {
		t.Fatal("missing deployment accepted")
	}
}
