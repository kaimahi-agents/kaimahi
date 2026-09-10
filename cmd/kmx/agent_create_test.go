package main

import (
	"bytes"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func orkaCreateArgs() []string {
	return []string{"agent", "create", "sample", "--namespace", "orka-system", "--provider-type", "openai", "--model", "qwen2.5:3b", "--secret", "local-provider-key", "--base-url", "http://ollama.ollama.svc.cluster.local:11434/v1", "--out", "-"}
}

func TestAgentCreateOrkaOfflineWithoutToolsOnPATH(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	var out, diagnostics bytes.Buffer
	deps, _ := testDependencies(&out, &diagnostics)
	if err := execute(orkaCreateArgs(), deps); err != nil {
		t.Fatal(err)
	}
	decoder := yaml.NewDecoder(&out)
	for _, kind := range []string{"Secret", "Provider", "Agent"} {
		var doc map[string]any
		if err := decoder.Decode(&doc); err != nil {
			t.Fatal(err)
		}
		if doc["kind"] != kind {
			t.Fatalf("got %v, want %s", doc, kind)
		}
		if kind == "Provider" && doc["spec"].(map[string]any)["defaultModel"] != "qwen2.5:3b" {
			t.Fatal(doc)
		}
	}
	if !strings.Contains(diagnostics.String(), "not applied") {
		t.Fatal(diagnostics.String())
	}
}

func TestAgentCreateRejectsLegacyFlagsAndSyntax(t *testing.T) {
	for _, extra := range [][]string{{"--image", "example/image"}, {"--isolation", "none"}, {"--run-as-user", "1000"}, {"--tools", "server:tool"}, {"--agent-requests-per-minute", "0"}, {"--provider-tokens-per-minute", "0"}, {"--dry-run"}} {
		var out, diagnostics bytes.Buffer
		deps, _ := testDependencies(&out, &diagnostics)
		if err := execute(append(orkaCreateArgs(), extra...), deps); err == nil {
			t.Fatalf("accepted %v", extra)
		}
		if out.Len() != 0 {
			t.Fatalf("emitted bytes for invalid flags %v", extra)
		}
	}
}

func TestAgentCreateHelpExplainsOrkaBoundary(t *testing.T) {
	var out, diagnostics bytes.Buffer
	deps, loads := testDependencies(&out, &diagnostics)
	if err := execute([]string{"agent", "create", "--help"}, deps); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"Orka", "Provider", "watches", "kagent", "ServiceAccount", "v0.1.3", "full", "--schema-target", "--skills"} {
		if !strings.Contains(out.String(), text) {
			t.Errorf("help lacks %q", text)
		}
	}
	if *loads != 0 {
		t.Fatal("help loaded config")
	}
}

func TestAgentCreateUnnamedReachesWizardRatherThanRequiredFlags(t *testing.T) {
	var out, diagnostics bytes.Buffer
	deps, _ := testDependencies(&out, &diagnostics)
	err := execute([]string{"agent", "create"}, deps)
	if err == nil || !strings.Contains(err.Error(), "non-interactive") {
		t.Fatalf("wizard unreachable: %v", err)
	}
}
