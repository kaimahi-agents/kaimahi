package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestAgentCreateAzureOfflineCLIEmitsOnlyReferencedSecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agents", "sample")
	args := []string{"agent", "create", "sample", "--namespace", "orka-system", "--provider-type", "azure-openai", "--model", "chat-prod", "--azure-deployment", "chat-prod", "--base-url", "https://example.openai.azure.com", "--secret", "target-key", "--out", "-", "--bundle-path", path}
	var out, diagnostics bytes.Buffer
	deps, _ := testDependencies(&out, &diagnostics)
	if err := execute(args, deps); err != nil {
		t.Fatal(err)
	}
	decoder := yaml.NewDecoder(&out)
	var secret, provider map[string]any
	if err := decoder.Decode(&secret); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Decode(&provider); err != nil {
		t.Fatal(err)
	}
	if len(secret) != 3 || secret["kind"] != "Secret" {
		t.Fatalf("value-bearing Secret: %#v", secret)
	}
	spec := provider["spec"].(map[string]any)
	if spec["type"] != "azure-openai" || spec["defaultModel"] != "chat-prod" || spec["baseURL"] != "https://example.openai.azure.com" {
		t.Fatalf("wrong Provider: %#v", spec)
	}
	azure := spec["azure"].(map[string]any)
	if azure["deploymentName"] != "chat-prod" {
		t.Fatalf("deployment missing: %#v", azure)
	}
	if _, ok := azure["apiVersion"]; ok {
		t.Fatalf("implicit version in Provider: %#v", azure)
	}
	binding, err := os.ReadFile(filepath.Join(path, "bindings.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(binding), "deploymentName: chat-prod") || strings.Contains(string(binding), "apiVersion: 202") {
		t.Fatalf("bindings mismatch: %s", binding)
	}
}
