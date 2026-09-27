package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestQuickstartExistingAgentToolAttachmentDoesNotCreateBundle(t *testing.T) {
	a, _, _, _, dir := orkaCreateFixture(t, "")
	root := t.TempDir()
	t.Chdir(root)
	body := []byte(`{"kind":"Agent","metadata":{"name":"sample","namespace":"orka-system","resourceVersion":"1"},"spec":{"tools":[{"name":"k8s-get-resources"}]}}`)
	if err := os.WriteFile(filepath.Join(dir, "sample-agents.core.orka.ai.json"), body, 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.attachQuickstartK8sTool("sample", "orka-system"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join("agents", "sample")); !os.IsNotExist(err) {
		t.Fatalf("existing-agent attachment wrote an agent bundle: %v", err)
	}
}

func TestQuickstartK8sToolPatchPreservesExistingTools(t *testing.T) {
	raw := []byte(`{"metadata":{"resourceVersion":"123"},"spec":{"tools":[{"name":"web_fetch","enabled":false}],"systemPrompt":{"inline":"custom"}}}`)
	patch, err := quickstartK8sToolPatch(raw)
	if err != nil {
		t.Fatal(err)
	}
	var ops []struct {
		Op, Path string
		Value    json.RawMessage
	}
	if err := json.Unmarshal(patch, &ops); err != nil || len(ops) != 2 || ops[0].Op != "test" || ops[0].Path != "/metadata/resourceVersion" || ops[1].Path != "/spec/tools" {
		t.Fatalf("patch=%s err=%v", patch, err)
	}
	if !strings.Contains(string(ops[1].Value), `"enabled":false`) || !strings.Contains(string(ops[1].Value), quickstartK8sTool) {
		t.Fatalf("lost existing tool configuration: %s", patch)
	}
	for _, enabled := range []string{"true", "false"} {
		patch, err := quickstartK8sToolPatch([]byte(`{"metadata":{"resourceVersion":"123"},"spec":{"tools":[{"name":"k8s-get-resources","enabled":` + enabled + `}]}}`))
		if err != nil || patch != nil {
			t.Fatalf("existing tool changed: %s %v", patch, err)
		}
	}
}

func TestQuickstartToolDefaultAndCustomInstructions(t *testing.T) {
	opt := CreateOptions{Name: "demo", Description: "My agent", Namespace: OrkaNamespace, ProviderType: "openai", Model: "test", Secret: "key"}
	quickstartAgentTools(&opt)
	if err := finishCreateWizardOptions(&opt); err != nil {
		t.Fatal(err)
	}
	if opt.Tools != quickstartK8sTool || !strings.Contains(opt.InstructionText, "My agent") || !strings.Contains(opt.InstructionText, quickstartK8sInstructions) {
		t.Fatalf("default options=%+v", opt)
	}
	opt.Tools, opt.InstructionText = "web_fetch", "Custom instructions"
	quickstartAgentTools(&opt)
	if opt.Tools != "web_fetch" || opt.InstructionText != "Custom instructions" {
		t.Fatal("custom configuration replaced")
	}
}
