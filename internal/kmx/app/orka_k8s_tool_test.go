package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
	"go.yaml.in/yaml/v3"
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

func TestQuickstartK8sToolUsesExactGatewayPolicy(t *testing.T) {
	body, err := manifest("orka-k8s-tool.yaml")
	if err != nil {
		t.Fatal(err)
	}

	decoder := yaml.NewDecoder(bytes.NewReader(body))
	var policyOK, toolOK bool
	for {
		var document map[string]any
		if err := decoder.Decode(&document); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		metadata, _ := document["metadata"].(map[string]any)
		spec, _ := document["spec"].(map[string]any)
		switch {
		case document["kind"] == "OutboundAccessPolicy" && metadata["name"] == quickstartK8sToolPolicy:
			gateway, _ := spec["gateway"].(map[string]any)
			serviceRef, _ := gateway["serviceRef"].(map[string]any)
			policyOK = gateway["scheme"] == "http" &&
				serviceRef["name"] == "kmx-k8s-tool" &&
				serviceRef["port"] == 8080
		case document["kind"] == "Tool" && metadata["name"] == quickstartK8sTool:
			http, _ := spec["http"].(map[string]any)
			policyRef, _ := http["outboundAccessPolicyRef"].(map[string]any)
			toolOK = http["url"] == quickstartK8sToolAuthority &&
				http["method"] == "POST" &&
				policyRef["name"] == quickstartK8sToolPolicy
		}
	}
	if !policyOK {
		t.Fatal("OutboundAccessPolicy does not route to the exact managed Tool Service")
	}
	if !toolOK {
		t.Fatal("Tool does not reference the exact managed gateway policy and authority")
	}
}

func TestWaitOrkaResourceConditionRejectsAStaleGeneration(t *testing.T) {
	dir := t.TempDir()
	countFile := filepath.Join(dir, "count")
	fakeTool(t, dir, "kubectl", fmt.Sprintf(`
count=0
[ ! -f %[1]q ] || count=$(/bin/cat %[1]q)
count=$((count + 1))
printf '%%s' "$count" > %[1]q
if [ "$count" -eq 1 ]; then
  printf '%%s' '{"metadata":{"generation":2},"status":{"conditions":[{"type":"Available","status":"True","observedGeneration":1}]}}'
else
  printf '%%s' '{"metadata":{"generation":2},"status":{"conditions":[{"type":"Available","status":"True","observedGeneration":2}]}}'
fi
`, countFile))
	t.Setenv("PATH", dir)
	a := &App{
		Cfg: &config.Config{KubeContext: "kind-demo"},
		Run: &run.Runner{},
	}

	if err := a.waitOrkaResourceCondition("tools.core.orka.ai", quickstartK8sTool, "Available"); err != nil {
		t.Fatal(err)
	}
	count, err := os.ReadFile(countFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(count) != "2" {
		t.Fatalf("status reads = %s, want 2; stale generation was accepted", count)
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
