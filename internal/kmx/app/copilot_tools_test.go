package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestCopilotModelsUsesAvailableCatalog(t *testing.T) {
	models, err := parseCopilotModels([]byte(`{"models":[{"id":"auto"},{"id":"fast","policy":{"state":"enabled"}},{"id":"blocked","policy":{"state":"disabled"}},{"id":"fast"}]}`))
	if err != nil || len(models) != 2 || models[1].Model != "fast" {
		t.Fatalf("models=%v err=%v", models, err)
	}
}

func TestCopilotToolLoopExecutesAndFeedsResult(t *testing.T) {
	c := jsonschema.NewCompiler()
	_ = c.AddResource("urn:test", map[string]any{"type": "object", "properties": map[string]any{"resource": map[string]any{"type": "string"}}, "required": []any{"resource"}, "additionalProperties": false})
	schema, err := c.Compile("urn:test")
	if err != nil {
		t.Fatal(err)
	}
	tools := []copilotTool{{Name: "k8s-get-resources", schema: schema}}
	for _, action := range []string{`{"type":"tool_call","name":"k8s-get-resources","arguments":{"resource":"pods"}}`, `{"type":"tool_call","name":"unknown","arguments":{}}`, `{"type":"tool_call","name":"k8s-get-resources","arguments":{}}`, `not json`} {
		calls, executions := 0, 0
		answer, err := copilotToolLoop(t.Context(), "instructions", "pods?", tools, func(_ context.Context, prompt string) (string, error) {
			calls++
			if calls == 1 {
				return action, nil
			}
			if !strings.Contains(prompt, "pod-one") {
				t.Fatal("tool result missing")
			}
			return `{"type":"final","text":"pod-one"}`, nil
		}, func(_ context.Context, tool copilotTool, args []byte) (string, error) {
			executions++
			return "pod-one", nil
		})
		if strings.Contains(action, `"resource":"pods"`) {
			if err != nil || answer != "pod-one" || executions != 1 {
				t.Fatalf("answer=%q err=%v executions=%d", answer, err, executions)
			}
		} else if err == nil || executions != 0 {
			t.Fatalf("invalid action executed: %s", action)
		}
	}
}

func TestCopilotToolPathRejectsUnsupportedAuthority(t *testing.T) {
	for _, raw := range []string{`{"http":{"url":"http://example.com/x"}}`, `{"http":{"url":"http://svc.other.svc:8080/x"}}`, `{"http":{"url":"http://svc.orka-system.svc:8080/x","authSecretRef":{"name":"key"}}}`} {
		if _, err := copilotToolPath(json.RawMessage(raw), OrkaNamespace); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}

	path, err := copilotToolPath(json.RawMessage(`{"http":{"url":"http://kmx-k8s-tool.orka-system.svc.cluster.local:8080/resources","method":"POST"}}`), OrkaNamespace)
	if err != nil || path != "/api/v1/namespaces/orka-system/services/http:kmx-k8s-tool:8080/proxy/resources" {
		t.Fatalf("path=%q err=%v", path, err)
	}
}

func TestCopilotToolPathAcceptsOnlyManagedKMXGatewayPolicy(t *testing.T) {
	raw := `{"http":{"url":"https://example.com/resources","method":"POST","outboundAccessPolicyRef":{"name":"kmx-k8s-tool-gateway"}}}`
	path, err := copilotToolPath(json.RawMessage(raw), OrkaNamespace)
	if err != nil || path != "/api/v1/namespaces/orka-system/services/http:kmx-k8s-tool:8080/proxy/resources" {
		t.Fatalf("path=%q err=%v", path, err)
	}
	for _, changed := range []string{
		strings.Replace(raw, "kmx-k8s-tool-gateway", "other-policy", 1),
		strings.Replace(raw, "https://example.com/resources", "https://other.example/resources", 1),
	} {
		if _, err := copilotToolPath(json.RawMessage(changed), OrkaNamespace); err == nil {
			t.Fatalf("accepted unmanaged policy Tool: %s", changed)
		}
	}
}
