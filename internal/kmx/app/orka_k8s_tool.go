package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	kaimahi "github.com/kaimahi-agents/kaimahi"
)

const (
	quickstartK8sTool          = "k8s-get-resources"
	quickstartK8sToolPolicy    = "kmx-k8s-tool-gateway"
	quickstartK8sToolAuthority = "https://example.com/resources"
)

const quickstartK8sInstructions = "For questions about live Kubernetes resources, call k8s-get-resources and answer only from its output. Never invent resource names. Copy resource names exactly. This tool lists resources read-only; it cannot change the cluster."

func quickstartAgentTools(opt *CreateOptions) {
	if strings.TrimSpace(opt.Tools) == "" {
		opt.Tools = quickstartK8sTool
	}
}

func (a *App) installQuickstartK8sTool() error {
	script, err := kaimahi.Manifests.ReadFile("scripts/orka-k8s-tool.py")
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]any{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]string{"name": "kmx-k8s-tool", "namespace": OrkaNamespace},
		"data":     map[string]string{"server.py": string(script)},
	})
	if err != nil {
		return err
	}
	if err := a.applyBytes("Orka Kubernetes tool server", body); err != nil {
		return err
	}
	resources, err := manifest("orka-k8s-tool.yaml")
	if err != nil {
		return err
	}
	resources = []byte(strings.Replace(string(resources), "kmx-tool-code-checksum", fmt.Sprintf("%x", sha256.Sum256(script)), 1))
	if err := a.applyBytes("Orka Kubernetes tool resources", resources); err != nil {
		return err
	}
	if err := a.kubectlRun("-n", OrkaNamespace, "rollout", "status", "deploy/kmx-k8s-tool", "--timeout=180s"); err != nil {
		return err
	}
	if err := a.waitOrkaResourceCondition("outboundaccesspolicies.core.orka.ai",
		quickstartK8sToolPolicy, "Accepted"); err != nil {
		return err
	}
	return a.waitOrkaResourceCondition("tools.core.orka.ai", quickstartK8sTool, "Available")
}

func (a *App) waitOrkaResourceCondition(resource, name, condition string) error {
	generation, err := a.kubectlCapture("-n", OrkaNamespace, "get", resource, name,
		"-o", "jsonpath={.metadata.generation}")
	if err != nil {
		return fmt.Errorf("read %s/%s generation: %w", resource, name, err)
	}
	generation = strings.TrimSpace(generation)
	if generation == "" {
		return fmt.Errorf("%s/%s has no generation", resource, name)
	}
	observed := fmt.Sprintf(`jsonpath={.status.conditions[?(@.type=="%s")].observedGeneration}=%s`,
		condition, generation)
	if err := a.kubectlRun("-n", OrkaNamespace, "wait", "--for="+observed,
		resource+"/"+name, "--timeout=60s"); err != nil {
		return fmt.Errorf("wait for %s/%s current generation: %w", resource, name, err)
	}
	if err := a.kubectlRun("-n", OrkaNamespace, "wait", "--for=condition="+condition,
		resource+"/"+name, "--timeout=60s"); err != nil {
		return fmt.Errorf("wait for %s/%s %s: %w", resource, name, condition, err)
	}
	return nil
}

// Preserve existing references and instructions. A resourceVersion test prevents
// overwriting concurrent edits when upgrading a pre-tool quickstart agent.
func (a *App) attachQuickstartK8sTool(agent, namespace string) error {
	ctx, cancel := context.WithTimeout(a.operationContext(), time.Minute)
	defer cancel()
	raw, err := a.orkaCapture(ctx, nil, "-n", namespace, "get", "agents.core.orka.ai", agent, "-o", "json")
	if err != nil {
		return err
	}
	patch, err := quickstartK8sToolPatch(raw)
	if err != nil || patch == nil {
		return err
	}
	raw, err = a.orkaCapture(ctx, nil, "-n", namespace, "patch", "agents.core.orka.ai", agent, "--type=json", "-p", string(patch), "-o", "json")
	if err != nil {
		return err
	}
	var object orkaObject
	if err := json.Unmarshal(raw, &object); err != nil || object.Metadata.UID == "" || object.Metadata.Generation < 1 {
		return fmt.Errorf("updated Agent returned no valid identity")
	}
	return a.waitOrkaReady(ctx, namespace, orkaIdentity{Kind: "Agent", Name: agent, UID: object.Metadata.UID, Generation: object.Metadata.Generation})
}

func quickstartK8sToolPatch(raw []byte) ([]byte, error) {
	var agent struct {
		Metadata struct {
			ResourceVersion string `json:"resourceVersion"`
		} `json:"metadata"`
		Spec struct {
			Tools []map[string]any `json:"tools"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(raw, &agent); err != nil {
		return nil, err
	}
	if agent.Metadata.ResourceVersion == "" {
		return nil, fmt.Errorf("Agent has no resourceVersion; cannot attach Kubernetes tool")
	}
	for _, tool := range agent.Spec.Tools {
		if tool["name"] == quickstartK8sTool {
			return nil, nil // Also respect an explicitly disabled reference.
		}
	}
	tools := append(agent.Spec.Tools, map[string]any{"name": quickstartK8sTool})
	return json.Marshal([]map[string]any{
		{"op": "test", "path": "/metadata/resourceVersion", "value": agent.Metadata.ResourceVersion},
		{"op": "add", "path": "/spec/tools", "value": tools},
	})
}
